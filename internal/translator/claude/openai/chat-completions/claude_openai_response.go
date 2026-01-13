// Package openai provides response translation functionality for Claude Code to OpenAI API compatibility.
// This package handles the conversion of Claude Code API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
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
//   - []string: A slice of strings, each containing an OpenAI-compatible JSON response
func ConvertClaudeResponseToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertAnthropicResponseToOpenAIParams{
			CreatedAt:    0,
			ResponseID:   "",
			FinishReason: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return []string{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// Base OpenAI streaming response template
	template := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"response_metadata":{}},"finish_reason":null}]}`

	// Set model
	if modelName != "" {
		template, _ = sjson.Set(template, "model", modelName)
	}

	// Set response ID and creation time
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID != "" {
		template, _ = sjson.Set(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
	}
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt > 0 {
		template, _ = sjson.Set(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)
	}

	switch eventType {
	case "message_start":
		// Initialize response with message metadata when a new message begins
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID = message.Get("id").String()
			(*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt = time.Now().Unix()

			template, _ = sjson.Set(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
			template, _ = sjson.Set(template, "model", modelName)
			template, _ = sjson.Set(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)

			// Set initial role to assistant for the response
			template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")

			// Initialize tool calls accumulator for tracking tool call progress
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}
			// Initialize thinking accumulator for tracking thinking progress
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator = make(map[int]*ThinkingAccumulator)
			}
		}
		return []string{template}

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
				return []string{}
			} else if blockType == "thinking" {
				// Start of thinking block - initialize accumulator to track thinking and signature
				index := int(root.Get("index").Int())

				if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator == nil {
					(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator = make(map[int]*ThinkingAccumulator)
				}

				(*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index] = &ThinkingAccumulator{}

				// Stream opening <think> tag
				template, _ = sjson.Set(template, "choices.0.delta.content", "<think>\n")
				return []string{template}
			}
		}
		return []string{}

	case "content_block_delta":
		// Handle content delta (text, tool use arguments, or reasoning content)
		hasContent := false
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// Text content delta - send incremental text updates
				if text := delta.Get("text"); text.Exists() {
					template, _ = sjson.Set(template, "choices.0.delta.content", text.String())
					hasContent = true
				}
			case "thinking_delta":
				// Stream reasoning/thinking content ngay lập tức
				if thinking := delta.Get("thinking"); thinking.Exists() {
					index := int(root.Get("index").Int())
					// Lưu thinking text gốc (không escape) để cache
					originalThinkingText := thinking.String()
					// Escape ``` trong thinking để không break format khi hiển thị
					//escapedThinkingText := strings.ReplaceAll(originalThinkingText, "```", "\\`\\`\\`")
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
							// Lưu text gốc để cache signature đúng
							accumulator.Thinking.WriteString(originalThinkingText)
						}
					}
					// Stream escaped thinking delta để hiển thị
					template, _ = sjson.Set(template, "choices.0.delta.content", originalThinkingText)
					hasContent = true
				}
			case "signature_delta":
				// Accumulate signature for thinking block
				if signature := delta.Get("signature"); signature.Exists() {
					index := int(root.Get("index").Int())
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
							accumulator.Signature.WriteString(signature.String())
						}
					}
				}
				// Don't output signature delta
				return []string{}
			case "input_json_delta":
				// Tool use input delta - accumulate arguments for tool calls
				if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
					index := int(root.Get("index").Int())
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
				// Don't output anything yet - wait for complete tool call
				return []string{}
			}
		}
		if hasContent {
			return []string{template}
		} else {
			return []string{}
		}

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
				template, _ = sjson.Set(template, "choices.0.delta.tool_calls.0.index", index)
				template, _ = sjson.Set(template, "choices.0.delta.tool_calls.0.id", accumulator.ID)
				template, _ = sjson.Set(template, "choices.0.delta.tool_calls.0.type", "function")
				template, _ = sjson.Set(template, "choices.0.delta.tool_calls.0.function.name", accumulator.Name)
				template, _ = sjson.Set(template, "choices.0.delta.tool_calls.0.function.arguments", arguments)

				// Clean up the accumulator for this index
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator, index)

				return []string{template}
			}
		}

		// Check for thinking accumulator
		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
				// Lấy thinking text và signature đã accumulate
				thinkingText := accumulator.Thinking.String()
				signatureText := accumulator.Signature.String()

				// Generate thinkingID từ hash của thinking text
				thinkingID := cache.GenerateThinkingID(thinkingText)

				// Cache thinking với signature
				if thinkingText != "" {
					cache.CacheThinking(thinkingID, thinkingText, signatureText)
					// log.Debugf("Cached thinking block (thinkingID=%s, textLen=%d)", thinkingID, len(thinkingText))
				}

				// Stream closing </think> tag + hidden thinkId marker
				closingContent := "\n</think>\n```plaintext:thinkId:" + thinkingID + "```\n"
				template, _ = sjson.Set(template, "choices.0.delta.content", closingContent)

				// Clean up the accumulator for this index
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator, index)

				return []string{template}
			}
		}

		return []string{}

	case "message_delta":
		// Handle message-level changes including stop reason and usage
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason = mapAnthropicStopReasonToOpenAI(stopReason.String())
				template, _ = sjson.Set(template, "choices.0.finish_reason", (*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason)
			}
		}

		// Handle usage information for token counts
		if usage := root.Get("usage"); usage.Exists() {
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()
			cacheReadInputTokens := usage.Get("cache_read_input_tokens").Int()
			cacheCreationInputTokens := usage.Get("cache_creation_input_tokens").Int()
			template, _ = sjson.Set(template, "usage.input_tokens", inputTokens)
			template, _ = sjson.Set(template, "usage.output_tokens", outputTokens)
			template, _ = sjson.Set(template, "usage.cache_read_input_tokens", cacheReadInputTokens)
			template, _ = sjson.Set(template, "usage.cache_creation_input_tokens", cacheCreationInputTokens)
			log.Infof("Request Claude %s. input_tokens: %d, output_tokens: %d, cache_creation_input_tokens: %d, cache_read_input_tokens: %d, totalTokens: %d.", modelName, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens, inputTokens+outputTokens+cacheCreationInputTokens+cacheReadInputTokens)
		}
		return []string{template}

	case "message_stop":
		// Final message event - no additional output needed
		return []string{}

	case "ping":
		// Ping events for keeping connection alive - no output needed
		return []string{}

	case "error":
		// Error event - format and return error response
		if errorData := root.Get("error"); errorData.Exists() {
			errorJSON := `{"error":{"message":"","type":""}}`
			errorJSON, _ = sjson.Set(errorJSON, "error.message", errorData.Get("message").String())
			errorJSON, _ = sjson.Set(errorJSON, "error.type", errorData.Get("type").String())
			return []string{errorJSON}
		}
		return []string{}

	default:
		// Unknown event type - ignore
		return []string{}
	}
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
//   - string: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertClaudeResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	// log.Debug("ConvertClaudeResponseToOpenAINonStream called")
	chunks := make([][]byte, 0)

	lines := bytes.Split(rawJSON, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, bytes.TrimSpace(line[5:]))
	}

	// Base OpenAI non-streaming response template
	out := `{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`

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
				inputTokens := usage.Get("input_tokens").Int()
				outputTokens := usage.Get("output_tokens").Int()
				cacheReadInputTokens := usage.Get("cache_read_input_tokens").Int()
				cacheCreationInputTokens := usage.Get("cache_creation_input_tokens").Int()
				out, _ = sjson.Set(out, "usage.prompt_tokens", inputTokens+cacheCreationInputTokens)
				out, _ = sjson.Set(out, "usage.completion_tokens", outputTokens)
				out, _ = sjson.Set(out, "usage.total_tokens", inputTokens+outputTokens)
				out, _ = sjson.Set(out, "usage.prompt_tokens_details.cached_tokens", cacheReadInputTokens)
			}
		}
	}

	// Set basic response fields including message ID, creation time, and model
	out, _ = sjson.Set(out, "id", messageID)
	out, _ = sjson.Set(out, "created", createdAt)
	out, _ = sjson.Set(out, "model", model)

	// Set accumulated content
	if len(contentParts) > 0 {
		out, _ = sjson.Set(out, "choices.0.message.content", strings.Join(contentParts, ""))
	}

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

			out, _ = sjson.Set(out, idPath, accumulator.ID)
			out, _ = sjson.Set(out, typePath, "function")
			out, _ = sjson.Set(out, namePath, accumulator.Name)
			out, _ = sjson.Set(out, argumentsPath, arguments)
			toolCallsCount++
		}
		if toolCallsCount > 0 {
			out, _ = sjson.Set(out, "choices.0.finish_reason", "tool_calls")
		} else {
			out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
		}
	} else {
		out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
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
