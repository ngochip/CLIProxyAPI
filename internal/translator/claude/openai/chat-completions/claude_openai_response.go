// Package openai provides response translation functionality for Claude Code to OpenAI API compatibility.
// This package handles the conversion of Claude Code API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToOpenAIParams holds parameters for response conversion
type ConvertAnthropicResponseToOpenAIParams struct {
	CreatedAt    int64
	ResponseID   string
	FinishReason string
	// Tool calls accumulator for streaming
	ToolCallsAccumulator    map[int]*ToolCallAccumulator
	// Thinking accumulator for streaming
	ThinkingAccumulator     map[int]*ThinkingAccumulator
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

				// Stream opening tag ngay lập tức
				template, _ = sjson.Set(template, "choices.0.delta.content", "```plaintext:Thinking\n")
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
					thinkingText := strings.ReplaceAll(thinking.String(), "```", "\\`\\`\\`")
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
							accumulator.Thinking.WriteString(thinkingText)
						}
					}
					// Stream thinking delta ngay lập tức giống text_delta
					template, _ = sjson.Set(template, "choices.0.delta.content", thinkingText)
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

				toolCall := map[string]interface{}{
					"index": index,
					"id":    accumulator.ID,
					"type":  "function",
					"function": map[string]interface{}{
						"name":      accumulator.Name,
						"arguments": arguments,
					},
				}

				template, _ = sjson.Set(template, "choices.0.delta.tool_calls", []interface{}{toolCall})

				// Clean up the accumulator for this index
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator, index)

				return []string{template}
			}
		}
		
		// Check for thinking accumulator
		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ThinkingAccumulator[index]; exists {
				// Build closing tag với metadata
				// thinkingText := accumulator.Thinking.String()
				signatureText := accumulator.Signature.String()
				
				// Tạo JSON object cho reasoning metadata
				// reasoningJSON := map[string]interface{}{
				// 	"thinking":  thinkingText,
				// 	"signature": signatureText,
				// }
				// reasoningJSONBytes, _ := json.Marshal(reasoningJSON)
				
				// Stream metadata và closing tag
				// Format: {"thinking":"xxx","signature":"xxx"}</reasoning>
				closingContent := "```\n"
				signatureContent := "```plaintext:Signature:" + signatureText + "```\n"
				template, _ = sjson.Set(template, "choices.0.delta.content", closingContent + signatureContent)
				// template, _ = sjson.Set(template, "choices.0.delta.content", signatureContent)
			
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
			usageObj := map[string]interface{}{
				"prompt_tokens":     usage.Get("input_tokens").Int(),
				// "completion_tokens": usage.Get("output_tokens").Int(),
				// "output_tokens":     usage.Get("output_tokens").Int(),
				"completion_tokens": usage.Get("output_tokens").Int(),
				"total_tokens":      usage.Get("input_tokens").Int() + usage.Get("output_tokens").Int(),
			}
			template, _ = sjson.Set(template, "usage", usageObj)
			// Log thông tin token usage cho request Claude
			log.Infof("Request Claude %s. prompt_tokens: %d, completion_tokens: %d, totalTokens: %d.", modelName, usage.Get("input_tokens").Int(), usage.Get("output_tokens").Int(), usage.Get("input_tokens").Int() + usage.Get("output_tokens").Int())

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
			errorResponse := map[string]interface{}{
				"error": map[string]interface{}{
					"message": errorData.Get("message").String(),
					"type":    errorData.Get("type").String(),
				},
			}
			errorJSON, _ := json.Marshal(errorResponse)
			return []string{string(errorJSON)}
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
	var inputTokens, outputTokens int64
	var reasoningTokens int64
	var stopReason string
	var contentParts []string
	// Use map to track tool calls by index for proper merging
	toolCallsMap := make(map[int]map[string]interface{})
	// Track tool call arguments accumulation
	toolCallArgsMap := make(map[int]strings.Builder)
	// Track thinking blocks by index
	thinkingMap := make(map[int]map[string]interface{})
	// Track thinking and signature accumulation
	thinkingTextMap := make(map[int]strings.Builder)
	thinkingSignatureMap := make(map[int]strings.Builder)

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
				if usage := message.Get("usage"); usage.Exists() {
					inputTokens = usage.Get("input_tokens").Int()
				}
			}

		case "content_block_start":
			// Handle different content block types at the beginning
			if contentBlock := root.Get("content_block"); contentBlock.Exists() {
				blockType := contentBlock.Get("type").String()
				index := int(root.Get("index").Int())
				
				if blockType == "thinking" {
					// Initialize thinking block tracking for this index
					thinkingMap[index] = map[string]interface{}{
						"type": "reasoning",
						"text": "",
					}
					// Initialize thinking and signature builders
					thinkingTextMap[index] = strings.Builder{}
					thinkingSignatureMap[index] = strings.Builder{}
				} else if blockType == "tool_use" {
					// Initialize tool call tracking for this index
					toolCallsMap[index] = map[string]interface{}{
						"id":   contentBlock.Get("id").String(),
						"type": "function",
						"function": map[string]interface{}{
							"name":      contentBlock.Get("name").String(),
							"arguments": "",
						},
					}
					// Initialize arguments builder for this tool call
					toolCallArgsMap[index] = strings.Builder{}
				}
			}

		case "content_block_delta":
			// Process incremental content updates
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				index := int(root.Get("index").Int())
				
				switch deltaType {
				case "text_delta":
					// Accumulate text content
					if text := delta.Get("text"); text.Exists() {
						contentParts = append(contentParts, text.String())
					}
				case "thinking_delta":
					// Accumulate reasoning/thinking content
					if thinking := delta.Get("thinking"); thinking.Exists() {
						if builder, exists := thinkingTextMap[index]; exists {
							builder.WriteString(thinking.String())
							thinkingTextMap[index] = builder
						}
					}
				case "signature_delta":
					// Accumulate signature for thinking block
					if signature := delta.Get("signature"); signature.Exists() {
						if builder, exists := thinkingSignatureMap[index]; exists {
							builder.WriteString(signature.String())
							thinkingSignatureMap[index] = builder
						}
					}
				case "input_json_delta":
					// Accumulate tool call arguments
					if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
						if builder, exists := toolCallArgsMap[index]; exists {
							builder.WriteString(partialJSON.String())
							toolCallArgsMap[index] = builder
						}
					}
				}
			}

		case "content_block_stop":
			// Finalize tool call arguments or thinking block for this index when content block ends
			index := int(root.Get("index").Int())
			
			// Finalize tool call if exists
			if toolCall, exists := toolCallsMap[index]; exists {
				if builder, argsExists := toolCallArgsMap[index]; argsExists {
					// Set the accumulated arguments for the tool call
					arguments := builder.String()
					if arguments == "" {
						arguments = "{}"
					}
					toolCall["function"].(map[string]interface{})["arguments"] = arguments
				}
			}
			
			// Finalize thinking block if exists
			if thinkingBlock, exists := thinkingMap[index]; exists {
				thinkingText := ""
				signatureText := ""
				
				if builder, textExists := thinkingTextMap[index]; textExists {
					thinkingText = builder.String()
				}
				if builder, sigExists := thinkingSignatureMap[index]; sigExists {
					signatureText = builder.String()
				}
				
				// Tạo JSON object cho reasoning content
				reasoningJSON := map[string]interface{}{
					"thinking":  thinkingText,
					"signature": signatureText,
				}
				reasoningJSONBytes, _ := json.Marshal(reasoningJSON)
				
				// Set the JSON string as text
				thinkingBlock["text"] = string(reasoningJSONBytes)
			}

		case "message_delta":
			// Extract stop reason and output token count when message ends
			if delta := root.Get("delta"); delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
				}
			}
			if usage := root.Get("usage"); usage.Exists() {
				outputTokens = usage.Get("output_tokens").Int()
				// Estimate reasoning tokens from accumulated thinking content
				totalThinkingLength := 0
				for _, builder := range thinkingTextMap {
					totalThinkingLength += builder.Len()
				}
				if totalThinkingLength > 0 {
					reasoningTokens = int64(totalThinkingLength / 4) // Rough estimation
				}
			}
		}
	}

	// Set basic response fields including message ID, creation time, and model
	out, _ = sjson.Set(out, "id", messageID)
	out, _ = sjson.Set(out, "created", createdAt)
	out, _ = sjson.Set(out, "model", model)

	// Build content array with text and thinking blocks
	var contentArray []interface{}
	
	// Tìm max index để biết có bao nhiêu content blocks
	maxIndex := -1
	for index := range thinkingMap {
		if index > maxIndex {
			maxIndex = index
		}
	}
	
	// Nếu có thinking blocks, xây dựng content array
	if len(thinkingMap) > 0 {
		// Add text content first if exists
		if len(contentParts) > 0 {
			textContent := strings.Join(contentParts, "")
			contentArray = append(contentArray, map[string]interface{}{
				"type": "text",
				"text": textContent,
			})
		}
		
		// Add thinking blocks theo thứ tự index
		for i := 0; i <= maxIndex; i++ {
			if thinkingBlock, exists := thinkingMap[i]; exists {
				contentArray = append(contentArray, thinkingBlock)
			}
		}
		
		// Set content as array
		out, _ = sjson.Set(out, "choices.0.message.content", contentArray)
	} else {
		// Không có thinking blocks, set content as string như cũ
		messageContent := strings.Join(contentParts, "")
		out, _ = sjson.Set(out, "choices.0.message.content", messageContent)
	}

	// Set tool calls if any were accumulated during processing
	if len(toolCallsMap) > 0 {
		// Convert tool calls map to array, preserving order by index
		var toolCallsArray []interface{}
		// Find the maximum index to determine the range
		maxIndex := -1
		for index := range toolCallsMap {
			if index > maxIndex {
				maxIndex = index
			}
		}
		// Iterate through all possible indices up to maxIndex
		for i := 0; i <= maxIndex; i++ {
			if toolCall, exists := toolCallsMap[i]; exists {
				toolCallsArray = append(toolCallsArray, toolCall)
			}
		}
		if len(toolCallsArray) > 0 {
			out, _ = sjson.Set(out, "choices.0.message.tool_calls", toolCallsArray)
			out, _ = sjson.Set(out, "choices.0.finish_reason", "tool_calls")
		} else {
			out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
		}
	} else {
		out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
	}

	// Set usage information including prompt tokens, completion tokens, and total tokens
	totalTokens := inputTokens + outputTokens
	out, _ = sjson.Set(out, "usage.prompt_tokens", inputTokens)
	out, _ = sjson.Set(out, "usage.completion_tokens", outputTokens)
	out, _ = sjson.Set(out, "usage.total_tokens", totalTokens)

	
	// Add reasoning tokens to usage details if any reasoning content was processed
	if reasoningTokens > 0 {
		out, _ = sjson.Set(out, "usage.completion_tokens_details.reasoning_tokens", reasoningTokens)
	}

	// Log thông tin token usage cho request Claude
	log.Infof("Request Claude %s. prompt_tokens: %d, completion_tokens: %d, totalTokens: %d, reasoningTokens: %d.", model, inputTokens, outputTokens, totalTokens, reasoningTokens)


	return out
}
