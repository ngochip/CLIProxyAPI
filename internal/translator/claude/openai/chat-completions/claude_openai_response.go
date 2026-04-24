// Package openai provides response translation functionality for Claude Code to OpenAI API compatibility.
// This package handles the conversion of Claude Code API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Note: deriveSessionIDFromRequest đã bị loại bỏ vì không cần thiết.
// Cache chỉ cần thinkingID là đủ để lookup.

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToOpenAIParams holds parameters for response conversion
type ConvertAnthropicResponseToOpenAIParams struct {
	CreatedAt    int64
	ResponseID   string
	FinishReason string
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Thinking accumulator for streaming
	ThinkingAccumulator map[int]*ThinkingAccumulator
	// Pre-built JSON fragments cho fast delta construction (tránh sjson.Set mỗi delta)
	deltaPrefix string
	deltaSuffix string
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// ThinkingAccumulator holds the state for accumulating thinking data
type ThinkingAccumulator struct {
	Thinking  strings.Builder
	Signature strings.Builder
	Nonce     string
}

func generateThinkingNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func calculateClaudeUsageTokens(usage gjson.Result) (promptTokens, completionTokens, totalTokens, cachedTokens, cacheCreationTokens, cacheReadTokens int64) {
	inputTokens := usage.Get("input_tokens").Int()
	completionTokens = usage.Get("output_tokens").Int()
	cacheReadTokens = usage.Get("cache_read_input_tokens").Int()
	cacheCreationTokens = usage.Get("cache_creation_input_tokens").Int()
	cachedTokens = cacheReadTokens

	promptTokens = inputTokens + cacheCreationTokens + cacheReadTokens
	totalTokens = promptTokens + completionTokens

	return
}

// ConvertClaudeResponseToOpenAI converts Claude Code streaming response format to OpenAI Chat Completions format.
// This function processes various Claude Code event types and transforms them into OpenAI-compatible JSON responses.
// It handles text content, tool calls, reasoning content, and usage metadata, outputting responses that match
// the OpenAI API format. The function supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertClaudeResponseToOpenAI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertAnthropicResponseToOpenAIParams{
			CreatedAt:    0,
			ResponseID:   "",
			FinishReason: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		if len(bytes.TrimSpace(rawJSON)) > 0 {
			log.Debugf("SSE line skipped (no data: prefix): %q", rawJSON)
		}
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	if !gjson.ValidBytes(rawJSON) {
		log.Debugf("invalid JSON in upstream SSE frame: %q", rawJSON)
		return [][]byte{}
	}

	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// Fast path: content_block_delta là event xuất hiện nhiều nhất khi streaming.
	// Xử lý trực tiếp không cần build JSON template để tránh overhead sjson.Set mỗi delta.
	if eventType == "content_block_delta" {
		return handleContentBlockDelta(root, (*param).(*ConvertAnthropicResponseToOpenAIParams))
	}

	// Base OpenAI streaming response template (chỉ build cho non-delta events)
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)

	// Set model
	if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	// Set response ID and creation time
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID != "" {
		template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
	}
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt > 0 {
		template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)
	}

	switch eventType {
	case "message_start":
		// Initialize response with message metadata when a new message begins
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID = message.Get("id").String()
			(*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt = time.Now().Unix()

			template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
			template, _ = sjson.SetBytes(template, "model", modelName)
			template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)

			// Pre-build JSON fragments cho fast streaming delta construction
			modelJSON, _ := json.Marshal(modelName)
			pState := (*param).(*ConvertAnthropicResponseToOpenAIParams)
			pState.deltaPrefix = `{"id":"` + pState.ResponseID +
				`","object":"chat.completion.chunk","created":` + strconv.FormatInt(pState.CreatedAt, 10) +
				`,"model":` + string(modelJSON) +
				`,"choices":[{"index":0,"delta":{"content":`
			pState.deltaSuffix = `,"response_metadata":{}},"finish_reason":null}]}`

			// Set initial role to assistant for the response
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")

			// Initialize tool calls accumulator for tracking tool call progress
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}
			// Initialize thinking accumulator for tracking thinking progress
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator = make(map[int]*ThinkingAccumulator)
			}
		}
		return [][]byte{template}

	case "content_block_start":
		// Start of a content block (text, tool use, or reasoning)
		if contentBlock := root.Get("content_block"); contentBlock.Exists() {
			blockType := contentBlock.Get("type").String()

			if blockType == "tool_use" {
				// Start of tool call - initialize accumulator to track arguments
				toolCallID := contentBlock.Get("id").String()
				toolName := contentBlock.Get("name").String()
				index := int(root.Get("index").Int())

				if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
					(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index] = &ToolCallAccumulator{
					ID:   toolCallID,
					Name: toolName,
				}

				// Don't output anything yet - wait for complete tool call
				return [][]byte{}
			} else if blockType == "thinking" {
				index := int(root.Get("index").Int())

				if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator == nil {
					(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator = make(map[int]*ThinkingAccumulator)
				}

				nonce := generateThinkingNonce()
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index] = &ThinkingAccumulator{Nonce: nonce}

				template, _ = sjson.SetBytes(template, "choices.0.delta.content", "<!--thinking-start:"+nonce+"-->\n")
				return [][]byte{template}
			}
		}
		return [][]byte{}

	// content_block_delta: đã xử lý bởi fast-path handleContentBlockDelta() ở trên

	case "content_block_stop":
		// End of content block - output complete tool call if it's a tool_use block or thinking if it's a thinking block
		index := int(root.Get("index").Int())

		// Check for tool call accumulator
		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
				// Build complete tool call with accumulated arguments
				arguments := accumulator.Arguments.String()
				if arguments == "" {
					arguments = "{}"
				}
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.index", index)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.id", accumulator.ID)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.type", "function")
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.name", accumulator.Name)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.arguments", arguments)

				// Clean up the accumulator for this index
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator, index)

				return [][]byte{template}
			}
		}

		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
				thinkingText := accumulator.Thinking.String()
				signatureText := accumulator.Signature.String()
				nonce := accumulator.Nonce

				thinkingID := cache.GenerateThinkingID(thinkingText)

				if thinkingText != "" {
					cache.CacheThinking(thinkingID, thinkingText, signatureText)
				}

				closingContent := "\n<!--thinking-end:" + nonce + "-->\n<!--thinkId:" + thinkingID + "-->\n"
				template, _ = sjson.SetBytes(template, "choices.0.delta.content", closingContent)

				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator, index)

				return [][]byte{template}
			}
		}

		return [][]byte{}

	case "message_delta":
		// Handle message-level changes including stop reason and usage
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason = mapAnthropicStopReasonToOpenAI(stopReason.String())
				template, _ = sjson.SetBytes(template, "choices.0.finish_reason", (*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason)
			}
		}

		// Handle usage information for token counts
		if usage := root.Get("usage"); usage.Exists() {
			promptTokens, completionTokens, totalTokens, cachedTokens, cacheCreation, cacheRead := calculateClaudeUsageTokens(usage)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", promptTokens)
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", completionTokens)
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokens)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
			inputTokens := promptTokens - cacheCreation - cacheRead
			log.Infof("Request Claude %s. input_tokens: %d, output_tokens: %d, cache_creation_input_tokens: %d, cache_read_input_tokens: %d, totalTokens: %d.", modelName, inputTokens, completionTokens, cacheCreation, cacheRead, totalTokens)
			logging.RecordTokenUsage(ctx, "input_tokens: %d, output_tokens: %d, cache_creation_input_tokens: %d, cache_read_input_tokens: %d, totalTokens: %d", inputTokens, completionTokens, cacheCreation, cacheRead, totalTokens)
		}
		return [][]byte{template}

	case "message_stop":
		// Final message event - no additional output needed
		return [][]byte{}

	case "ping":
		// Ping events for keeping connection alive - no output needed
		return [][]byte{}

	case "error":
		// Error event - format and return error response
		if errorData := root.Get("error"); errorData.Exists() {
			errorJSON := []byte(`{"error":{"message":"","type":""}}`)
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.message", errorData.Get("message").String())
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.type", errorData.Get("type").String())
			return [][]byte{errorJSON}
		}
		return [][]byte{}

	default:
		// Unknown event type - ignore
		return [][]byte{}
	}
}

// handleContentBlockDelta processes content_block_delta events with fast-path optimization.
// Uses pre-built JSON prefix/suffix + gjson Raw value to avoid sjson.Set overhead per delta.
// Falls back to sjson.SetRawBytes when deltaPrefix is empty (e.g. missed message_start).
func handleContentBlockDelta(root gjson.Result, p *ConvertAnthropicResponseToOpenAIParams) [][]byte {
	delta := root.Get("delta")
	if !delta.Exists() {
		return [][]byte{}
	}

	switch delta.Get("type").String() {
	case "text_delta":
		if text := delta.Get("text"); text.Exists() {
			return [][]byte{buildDeltaChunk(p, text.Raw)}
		}
	case "thinking_delta":
		if thinking := delta.Get("thinking"); thinking.Exists() {
			index := int(root.Get("index").Int())
			if p.ThinkingAccumulator != nil {
				if acc, exists := p.ThinkingAccumulator[index]; exists {
					acc.Thinking.WriteString(thinking.String())
				}
			}
			return [][]byte{buildDeltaChunk(p, thinking.Raw)}
		}
	case "signature_delta":
		if signature := delta.Get("signature"); signature.Exists() {
			index := int(root.Get("index").Int())
			if p.ThinkingAccumulator != nil {
				if acc, exists := p.ThinkingAccumulator[index]; exists {
					acc.Signature.WriteString(signature.String())
				}
			}
		}
	case "input_json_delta":
		if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
			index := int(root.Get("index").Int())
			if p.ToolCallsAccumulator != nil {
				if acc, exists := p.ToolCallsAccumulator[index]; exists {
					acc.Arguments.WriteString(partialJSON.String())
				}
			}
		}
	}
	return [][]byte{}
}

// buildDeltaChunk constructs an OpenAI streaming chunk from a raw JSON content value.
// Fast path: concat pre-built prefix/suffix when available (message_start already seen).
// Fallback: build full template via sjson when deltaPrefix is empty (missed message_start).
func buildDeltaChunk(p *ConvertAnthropicResponseToOpenAIParams, rawContent string) []byte {
	if p.deltaPrefix != "" {
		result := []byte(p.deltaPrefix + rawContent + p.deltaSuffix)
		if gjson.ValidBytes(result) {
			return result
		}
		log.Debugf("fast-path delta produced invalid JSON, falling back to sjson builder")
	}

	template := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
	if p.ResponseID != "" {
		template, _ = sjson.SetBytes(template, "id", p.ResponseID)
	}
	if p.CreatedAt > 0 {
		template, _ = sjson.SetBytes(template, "created", p.CreatedAt)
	}
	template, _ = sjson.SetRawBytes(template, "choices.0.delta.content", []byte(rawContent))
	return template
}

// mapAnthropicStopReasonToOpenAI maps Anthropic stop reasons to OpenAI stop reasons
func mapAnthropicStopReasonToOpenAI(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// ConvertClaudeResponseToOpenAINonStream converts a non-streaming Claude Code response to a non-streaming OpenAI response.
// This function processes the complete Claude Code response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertClaudeResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	chunks := make([][]byte, 0)

	lines := bytes.Split(rawJSON, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, bytes.TrimSpace(line[5:]))
	}

	// Base OpenAI non-streaming response template
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

	var messageID string
	var model string
	var createdAt int64
	var stopReason string
	var contentParts []string
	toolCallsAccumulator := make(map[int]*ToolCallAccumulator)

	for _, chunk := range chunks {
		root := gjson.ParseBytes(chunk)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			// Extract initial message metadata including ID, model, and input token count
			if message := root.Get("message"); message.Exists() {
				messageID = message.Get("id").String()
				model = message.Get("model").String()
				createdAt = time.Now().Unix()
			}

		case "content_block_start":
			// Handle different content block types at the beginning
			if contentBlock := root.Get("content_block"); contentBlock.Exists() {
				blockType := contentBlock.Get("type").String()
				// index := int(root.Get("index").Int())

				if blockType == "thinking" {

				} else if blockType == "tool_use" {
					// Initialize tool call accumulator for this index
					index := int(root.Get("index").Int())
					toolCallsAccumulator[index] = &ToolCallAccumulator{
						ID:   contentBlock.Get("id").String(),
						Name: contentBlock.Get("name").String(),
					}
				}
			}

		case "content_block_delta":
			// Process incremental content updates
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				// index := int(root.Get("index").Int())

				switch deltaType {
				case "text_delta":
					// Accumulate text content
					if text := delta.Get("text"); text.Exists() {
						contentParts = append(contentParts, text.String())
					}
				case "thinking_delta":
					// Accumulate reasoning/thinking content
					// if thinking := delta.Get("thinking"); thinking.Exists() {
					// 	if builder, exists := thinkingTextMap[index]; exists {
					// 		builder.WriteString(thinking.String())
					// 		thinkingTextMap[index] = builder
					// 	}
					// }
				case "signature_delta":
					// Accumulate signature for thinking block
					// if signature := delta.Get("signature"); signature.Exists() {
					// 	if builder, exists := thinkingSignatureMap[index]; exists {
					// 		builder.WriteString(signature.String())
					// 		thinkingSignatureMap[index] = builder
					// 	}
					// }
				case "input_json_delta":
					// Accumulate tool call arguments
					if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
						index := int(root.Get("index").Int())
						if accumulator, exists := toolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
			}

		case "content_block_stop":
			// Finalize tool call arguments or thinking block for this index when content block ends
			index := int(root.Get("index").Int())
			if accumulator, exists := toolCallsAccumulator[index]; exists {
				if accumulator.Arguments.Len() == 0 {
					accumulator.Arguments.WriteString("{}")
				}
			}

		case "message_delta":
			// Extract stop reason and output token count when message ends
			if delta := root.Get("delta"); delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
				}
			}
			if usage := root.Get("usage"); usage.Exists() {
				promptTokens, completionTokens, totalTokens, cachedTokens, cacheCreation, cacheRead := calculateClaudeUsageTokens(usage)
				out, _ = sjson.SetBytes(out, "usage.prompt_tokens", promptTokens)
				out, _ = sjson.SetBytes(out, "usage.completion_tokens", completionTokens)
				out, _ = sjson.SetBytes(out, "usage.total_tokens", totalTokens)
				out, _ = sjson.SetBytes(out, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
				inputTokens := promptTokens - cacheCreation - cacheRead
				log.Infof("Request Claude %s. input_tokens: %d, output_tokens: %d, cache_creation_input_tokens: %d, cache_read_input_tokens: %d, totalTokens: %d.", modelName, inputTokens, completionTokens, cacheCreation, cacheRead, totalTokens)
				logging.RecordTokenUsage(ctx, "input_tokens: %d, output_tokens: %d, cache_creation_input_tokens: %d, cache_read_input_tokens: %d, totalTokens: %d", inputTokens, completionTokens, cacheCreation, cacheRead, totalTokens)
			}
		}
	}

	// Set basic response fields including message ID, creation time, and model
	out, _ = sjson.SetBytes(out, "id", messageID)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	out, _ = sjson.SetBytes(out, "model", model)

	// Set message content by combining all text parts
	messageContent := strings.Join(contentParts, "")
	out, _ = sjson.SetBytes(out, "choices.0.message.content", messageContent)

	// Set tool calls if any were accumulated during processing
	if len(toolCallsAccumulator) > 0 {
		toolCallsCount := 0
		maxIndex := -1
		for index := range toolCallsAccumulator {
			if index > maxIndex {
				maxIndex = index
			}
		}

		for i := 0; i <= maxIndex; i++ {
			accumulator, exists := toolCallsAccumulator[i]
			if !exists {
				continue
			}

			arguments := accumulator.Arguments.String()

			idPath := fmt.Sprintf("choices.0.message.tool_calls.%d.id", toolCallsCount)
			typePath := fmt.Sprintf("choices.0.message.tool_calls.%d.type", toolCallsCount)
			namePath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.name", toolCallsCount)
			argumentsPath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.arguments", toolCallsCount)

			out, _ = sjson.SetBytes(out, idPath, accumulator.ID)
			out, _ = sjson.SetBytes(out, typePath, "function")
			out, _ = sjson.SetBytes(out, namePath, accumulator.Name)
			out, _ = sjson.SetBytes(out, argumentsPath, arguments)
			toolCallsCount++
		}
		if toolCallsCount > 0 {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
		} else {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
		}
	} else {
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
	}

	// // Set usage information including prompt tokens, completion tokens, and total tokens
	// totalTokens := inputTokens + outputTokens
	// out, _ = sjson.Set(out, "usage.prompt_tokens", inputTokens)
	// out, _ = sjson.Set(out, "usage.completion_tokens", outputTokens)
	// out, _ = sjson.Set(out, "usage.total_tokens", totalTokens)

	// // Add reasoning tokens to usage details if any reasoning content was processed
	// if reasoningTokens > 0 {
	// 	out, _ = sjson.Set(out, "usage.completion_tokens_details.reasoning_tokens", reasoningTokens)
	// }

	// // Log thông tin token usage cho request Claude
	// log.Infof("Request Claude %s. prompt_tokens: %d, completion_tokens: %d, totalTokens: %d, reasoningTokens: %d.", model, inputTokens, outputTokens, totalTokens, reasoningTokens)

	return out
}
