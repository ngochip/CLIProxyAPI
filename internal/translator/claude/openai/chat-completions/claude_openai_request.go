// Package openai provides request translation functionality for OpenAI to Claude Code API compatibility.
// It handles parsing and transforming OpenAI Chat Completions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Claude Code API's expected format.
package chat_completions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	user    = ""
	account = ""
	session = ""

	// Regex patterns cho việc parse thinking content
	// Pattern cho <think> tag
	thinkTagRegex = regexp.MustCompile(`<think>([\s\S]*?)</think>`)
	// Pattern cho thinkId marker: <!--thinkId:xxx--> (HTML comment, ẩn trên Cursor UI)
	thinkIdRegex = regexp.MustCompile(`<!--thinkId:([a-f0-9]+)-->`)
)

// Note: deriveSessionID đã bị loại bỏ vì không cần thiết.
// Cache chỉ cần thinkingID là đủ để lookup.

// ensureAssistantThinkingBlock kiểm tra và fix assistant message khi thinking enabled
// Theo Claude API: "When thinking is enabled, a final assistant message must start with
// a thinking block (preceeding the lastmost set of tool_use and tool_result blocks)"
//
// Nếu assistant message không có thinking block → Disable thinking tạm thời
// (Vì Claude không thể regenerate thinking - sẽ báo lỗi "thinking required")
func ensureAssistantThinkingBlock(requestJSON string) string {
	// Kiểm tra xem thinking có enabled không
	thinkingType := gjson.Get(requestJSON, "thinking.type").String()
	if thinkingType != "enabled" {
		return requestJSON
	}

	// Lấy tất cả messages
	messagesResult := gjson.Get(requestJSON, "messages")
	if !messagesResult.IsArray() || len(messagesResult.Array()) == 0 {
		return requestJSON
	}

	messages := messagesResult.Array()

	// Scan TẤT CẢ assistant messages
	// Khi thinking enabled, Claude yêu cầu mọi assistant turn đều phải có thinking block
	for i := 0; i < len(messages); i++ {
		if messages[i].Get("role").String() != "assistant" {
			continue
		}

		content := messages[i].Get("content")

		// Case 1: content là string (không phải array) → không có thinking block
		if content.Type == gjson.String {
			// String content không thể có thinking block
			result, _ := sjson.Delete(requestJSON, "thinking")
			// log.Warnf("⚠ Disabled thinking: assistant message %d has string content (no thinking block)", i)
			return result
		}

		// Case 2: content không phải array hoặc empty
		if !content.IsArray() || len(content.Array()) == 0 {
			// Empty hoặc không phải array → không có thinking
			result, _ := sjson.Delete(requestJSON, "thinking")
			// log.Warnf("⚠ Disabled thinking: assistant message %d has empty/invalid content", i)
			return result
		}

		// Case 3: content là array, check phần tử đầu tiên
		contentArray := content.Array()
		firstContentType := contentArray[0].Get("type").String()

		// Nếu không bắt đầu bằng thinking hoặc redacted_thinking → disable
		if firstContentType != "thinking" && firstContentType != "redacted_thinking" {
			result, _ := sjson.Delete(requestJSON, "thinking")
			// log.Warnf("⚠ Disabled thinking: assistant message %d starts with %s (expected thinking)", i, firstContentType)
			return result
		}
	}

	// Tất cả assistant messages đều có thinking block → OK
	return requestJSON
}

// extractThinkingFromContent trích xuất thinking từ text content
// Format: thinkId marker <!--thinkId:xxx--> -> lookup cache
func extractThinkingFromContent(text string) []interface{} {
	// Thử tìm thinkId marker trước (new format)
	idMatch := thinkIdRegex.FindStringSubmatch(text)
	if len(idMatch) > 1 {
		thinkingID := idMatch[1]
		entry := cache.GetCachedThinking(thinkingID)

		// Nếu tìm thấy cache với valid signature → restore thinking block
		if entry != nil && cache.HasValidSignature("claude", entry.Signature) {
			// Found valid cache → restore thinking
			// log.Infof("✓ Restored cached thinking (thinkingID=%s, textLen=%d, sigLen=%d)",
			// 	thinkingID, len(entry.ThinkingText), len(entry.Signature))

			// Remove <think> tag và thinkId marker từ text
			remainingText := thinkTagRegex.ReplaceAllString(text, "")
			remainingText = thinkIdRegex.ReplaceAllString(remainingText, "")
			remainingText = strings.TrimSpace(remainingText)

			var parts []interface{}

			// Part 1: thinking block với thinking và signature từ cache
			thinkingPart := map[string]interface{}{
				"type":      "thinking",
				"thinking":  entry.ThinkingText,
				"signature": entry.Signature,
			}
			parts = append(parts, thinkingPart)

			// Part 2: phần text còn lại (nếu có)
			if remainingText != "" {
				textPart := map[string]interface{}{
					"type": "text",
					"text": remainingText,
				}
				parts = append(parts, textPart)
			}

			return parts
		}

		// Cache miss hoặc invalid signature - fallback: parse thinking từ <think> tag
		// Claude API sẽ regenerate signature mới
		// if entry != nil {
		// 	log.Warnf("✗ Thinking cache found but invalid signature (thinkingID=%s, sigLen=%d) - will regenerate signature",
		// 		thinkingID, len(entry.Signature))
		// } else {
		// 	log.Warnf("✗ Thinking cache miss (thinkingID=%s) - will regenerate signature",
		// 		thinkingID)
		// }

		// Fallback: extract thinking từ <think> tag
		thinkMatch := thinkTagRegex.FindStringSubmatch(text)
		if len(thinkMatch) > 1 {
			thinkingText := thinkMatch[1]

			// Unescape ``` trong thinking text (vì nó đã bị escape khi stream)
			thinkingText = strings.ReplaceAll(thinkingText, "\\`\\`\\`", "```")
			thinkingText = strings.TrimSpace(thinkingText)

			// Remove <think> tag và thinkId marker từ remaining text
			remainingText := thinkTagRegex.ReplaceAllString(text, "")
			remainingText = thinkIdRegex.ReplaceAllString(remainingText, "")
			remainingText = strings.TrimSpace(remainingText)

			var parts []interface{}

			// Part 1: thinking block KHÔNG CÓ signature (để Claude regenerate)
			thinkingPart := map[string]interface{}{
				"type":     "thinking",
				"thinking": thinkingText,
				// Không có signature → Claude API sẽ regenerate
			}
			parts = append(parts, thinkingPart)

			// Part 2: phần text còn lại (nếu có)
			if remainingText != "" {
				textPart := map[string]interface{}{
					"type": "text",
					"text": remainingText,
				}
				parts = append(parts, textPart)
			}

			// log.Infof("→ Fallback: extracted thinking from <think> tag (textLen=%d) - signature will be regenerated", len(thinkingText))
			return parts
		}
	}

	// No valid thinking format found → clean up và return text only
	cleanText := thinkTagRegex.ReplaceAllString(text, "")
	cleanText = thinkIdRegex.ReplaceAllString(cleanText, "")
	cleanText = strings.TrimSpace(cleanText)

	if cleanText == "" {
		return nil
	}

	return []interface{}{
		map[string]interface{}{
			"type": "text",
			"text": cleanText,
		},
	}
}

// ConvertOpenAIRequestToClaude parses and transforms an OpenAI Chat Completions API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and parameter extraction (max_tokens, temperature, top_p, etc.)
// 2. Message content conversion from OpenAI to Claude Code format
// 3. Tool call and tool result handling with proper ID mapping
// 4. Image data conversion from OpenAI data URLs to Claude Code base64 format
// 5. Stop sequence and streaming configuration handling
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	if account == "" {
		u, _ := uuid.NewRandom()
		account = u.String()
	}
	if session == "" {
		u, _ := uuid.NewRandom()
		session = u.String()
	}
	if user == "" {
		sum := sha256.Sum256([]byte(account + session))
		user = hex.EncodeToString(sum[:])
	}
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", user, account, session)

	// Lấy max_tokens từ model registry, fallback 64000 nếu không tìm thấy
	defaultMaxTokens := 64000
	if modelInfo := registry.LookupModelInfo(modelName, "claude"); modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		defaultMaxTokens = modelInfo.MaxCompletionTokens
	}

	// Base Claude Code API template with model-specific max_tokens
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":%d,"messages":[],"metadata":{"user_id":"%s"}}`, defaultMaxTokens, userID))

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI reasoning_effort to Claude thinking config.
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 supports adaptive thinking with output_config.effort.
			// MapToClaudeEffort normalizes levels (e.g. minimal→low, xhigh→high) to avoid
			// validation errors since validate treats same-provider unsupported levels as errors.
			if supportsAdaptive {
				switch effort {
				case "none":
					out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				case "auto":
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				default:
					if mapped, ok := thinking.MapToClaudeEffort(effort, supportsMax); ok {
						effort = mapped
					}
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.SetBytes(out, "output_config.effort", effort)
				}
			} else {
				// Legacy/manual thinking (budget_tokens).
				budget, ok := thinking.ConvertLevelToBudget(effort)
				if ok {
					switch budget {
					case 0:
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					case -1:
						out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
					default:
						if budget > 0 {
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				}
			}
		}
	}

	// Helper for generating tool call IDs in the form: toolu_<alphanum>
	// This ensures unique identifiers for tool calls in the Claude Code format
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 24 chars random suffix for uniqueness
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// Model mapping to specify which Claude Code model to use
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens configuration with fallback to default value
	//disable max_tokens for cursor unlimited mode
	// if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
	// 	out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	// }

	// Temperature setting for controlling response randomness
	// Khi thinking được bật từ request JSON, set temperature = 1
	// Note: Khi thinking được bật từ metadata (model alias), temperature sẽ được set trong claude_executor.go
	thinkingEnabled := gjson.GetBytes(out, "thinking.type").String() == "enabled"
	if thinkingEnabled {
		out, _ = sjson.SetBytes(out, "temperature", 1)
	} else if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() {
		// Top P setting for nucleus sampling (filtered out if temperature is set)
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	// Stop sequences configuration for custom termination conditions
	if stop := root.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			var stopSequences []string
			stop.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.SetBytes(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.SetBytes(out, "stop_sequences", []string{stop.String()})
		}
	}

	// Stream configuration to enable or disable streaming responses
	out, _ = sjson.SetBytes(out, "stream", stream)
	if system := root.Get("system"); system.Exists() {
		out, _ = sjson.SetRawBytes(out, "system", []byte(system.Raw))
	}

	// Process messages and transform them to Claude Code format
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messageIndex := 0
		systemMessageIndex := -1
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			case "system":
				if systemMessageIndex == -1 {
					systemMsg := []byte(`{"role":"user","content":[]}`)
					out, _ = sjson.SetRawBytes(out, "messages.-1", systemMsg)
					systemMessageIndex = messageIndex
					messageIndex++
				}
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", systemMessageIndex), textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", systemMessageIndex), textPart)
						}
						return true
					})
				}
			case "user", "assistant":
				msg := []byte(`{"role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", role)

				// Handle content based on its type (string or array)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					parts := extractThinkingFromContent(contentResult.String())
					for _, part := range parts {
						msg, _ = sjson.SetBytes(msg, "content.-1", part)
					}
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "text":
						parts := extractThinkingFromContent(part.Get("text").String())
						for _, p := range parts {
								msg, _ = sjson.SetBytes(msg, "content.-1", p)
						}
					default:
						claudePart := convertOpenAIContentPartToClaudePart(part)
						if claudePart != "" {
							msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(claudePart))
						}
						}
						return true
					})
				}

				// Handle tool calls (for assistant messages)
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = genToolCallID()
							}

							function := toolCall.Get("function")
							toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
							toolUse, _ = sjson.SetBytes(toolUse, "id", toolCallID)
							toolUse, _ = sjson.SetBytes(toolUse, "name", function.Get("name").String())

							// Parse arguments for the tool call
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" && gjson.Valid(argsStr) {
									argsJSON := gjson.Parse(argsStr)
									if argsJSON.IsObject() {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
									} else {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
									}
								} else {
									toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
								}
							} else {
								toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
							}

							msg, _ = sjson.SetRawBytes(msg, "content.-1", toolUse)
						}
						return true
					})
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				messageIndex++

			case "tool":
				// Handle tool result messages conversion
				toolCallID := message.Get("tool_call_id").String()
				toolContentResult := message.Get("content")

				msg := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":""}]}`)
				msg, _ = sjson.SetBytes(msg, "content.0.tool_use_id", toolCallID)
				toolResultContent, toolResultContentRaw := convertOpenAIToolResultContent(toolContentResult)
				if toolResultContentRaw {
					msg, _ = sjson.SetRawBytes(msg, "content.0.content", []byte(toolResultContent))
				} else {
					msg, _ = sjson.SetBytes(msg, "content.0.content", toolResultContent)
				}
				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				messageIndex++
			}
			return true
		})
	}

	// Tools mapping: OpenAI tools -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		hasAnthropicTools := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				anthropicTool := []byte(`{"name":"","description":""}`)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", function.Get("name").String())
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", function.Get("description").String())

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", []byte(parameters.Raw))
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", []byte(parameters.Raw))
				}

				out, _ = sjson.SetRawBytes(out, "tools.-1", anthropicTool)
				hasAnthropicTools = true
			} else if !tool.Get("type").Exists() {
				//compatible with cursor
				anthropicTool := map[string]interface{}{
					"name":        tool.Get("name").String(),
					"description": tool.Get("description").String(),
				}

				if parameters := tool.Get("input_schema"); parameters.Exists() {
					anthropicTool["input_schema"] = parameters.Value()
				} else if parameters = tool.Get("input_schema"); parameters.Exists() {
					anthropicTool["input_schema"] = parameters.Value()
				}

				out, _ = sjson.SetBytes(out, "tools.-1", anthropicTool)
				hasAnthropicTools = true
			}
			return true
		})

		if !hasAnthropicTools {
			out, _ = sjson.DeleteBytes(out, "tools")
		}
	}

	// Tool choice mapping from OpenAI format to Claude Code format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			choice := toolChoice.String()
			switch choice {
			case "none":
				// Don't set tool_choice, Claude Code will not use tools
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "required":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
			}
		case gjson.JSON:
			// Specific tool choice mapping
			if toolChoice.Get("type").String() == "function" {
				functionName := toolChoice.Get("function.name").String()
				toolChoiceJSON := []byte(`{"type":"tool","name":""}`)
				toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", functionName)
				out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
			}
		default:
		}
	}

	// Fix assistant messages when thinking is enabled
	// Claude API yêu cầu: "When thinking is enabled, a final assistant message must start
	// with a thinking block (preceeding the lastmost set of tool_use and tool_result blocks)"
	out = []byte(ensureAssistantThinkingBlock(string(out)))

	// Apply cache_control markers để tối ưu prompt caching
	// Anthropic cho phép tối đa 4 breakpoints, đặt ở cuối các phần ổn định
	out = []byte(applyCacheControlMarkers(string(out)))

	return out
}

// NOTE: Cache control logic đã được tách sang file cache_control.go
// để tránh conflict khi merge main branch vào cursor branch.
// Xem applyCacheControlMarkers() và findLastCacheableContentIdx() trong cache_control.go

func convertOpenAIContentPartToClaudePart(part gjson.Result) string {
	switch part.Get("type").String() {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
		return string(textPart)

	case "image_url":
		return convertOpenAIImageURLToClaudePart(part.Get("image_url.url").String())

	case "image":
		source := part.Get("source")
		if source.Exists() && source.Get("type").String() == "base64" {
			imagePart := `{"type":"image","source":{"type":"base64","media_type":"","data":""}}`
			imagePart, _ = sjson.Set(imagePart, "source.media_type", source.Get("media_type").String())
			imagePart, _ = sjson.Set(imagePart, "source.data", source.Get("data").String())
			return imagePart
		}

	case "file":
		fileData := part.Get("file.file_data").String()
		if strings.HasPrefix(fileData, "data:") {
			semicolonIdx := strings.Index(fileData, ";")
			commaIdx := strings.Index(fileData, ",")
			if semicolonIdx != -1 && commaIdx != -1 && commaIdx > semicolonIdx {
				mediaType := strings.TrimPrefix(fileData[:semicolonIdx], "data:")
				data := fileData[commaIdx+1:]
				docPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
				docPart, _ = sjson.SetBytes(docPart, "source.media_type", mediaType)
				docPart, _ = sjson.SetBytes(docPart, "source.data", data)
				return string(docPart)
			}
		}

	case "tool_use":
		toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
		toolUse, _ = sjson.Set(toolUse, "id", part.Get("id").String())
		toolUse, _ = sjson.Set(toolUse, "name", part.Get("name").String())
		if input := part.Get("input"); input.Exists() {
			toolUse, _ = sjson.SetRaw(toolUse, "input", input.Raw)
		}
		return toolUse

	case "tool_result":
		toolResult := `{"type":"tool_result","tool_use_id":"","content":""}`
		toolResult, _ = sjson.Set(toolResult, "tool_use_id", part.Get("tool_use_id").String())
		toolResult, _ = sjson.Set(toolResult, "content", part.Get("content").String())
		return toolResult
	}

	return ""
}

func convertOpenAIImageURLToClaudePart(imageURL string) string {
	if imageURL == "" {
		return ""
	}

	if strings.HasPrefix(imageURL, "data:") {
		parts := strings.SplitN(imageURL, ",", 2)
		if len(parts) != 2 {
			return ""
		}

		mediaTypePart := strings.SplitN(parts[0], ";", 2)[0]
		mediaType := strings.TrimPrefix(mediaTypePart, "data:")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		imagePart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		imagePart, _ = sjson.SetBytes(imagePart, "source.media_type", mediaType)
		imagePart, _ = sjson.SetBytes(imagePart, "source.data", parts[1])
		return string(imagePart)
	}

	imagePart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
	imagePart, _ = sjson.SetBytes(imagePart, "source.url", imageURL)
	return string(imagePart)
}

func convertOpenAIToolResultContent(content gjson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}

	if content.Type == gjson.String {
		return content.String(), false
	}

	if content.IsArray() {
		claudeContent := []byte("[]")
		partCount := 0

		content.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = sjson.SetBytes(textPart, "text", part.String())
				claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", textPart)
				partCount++
				return true
			}

			claudePart := convertOpenAIContentPartToClaudePart(part)
			if claudePart != "" {
				claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", []byte(claudePart))
				partCount++
			}
			return true
		})

		if partCount > 0 || len(content.Array()) == 0 {
			return string(claudeContent), true
		}

		return content.Raw, false
	}

	if content.IsObject() {
		claudePart := convertOpenAIContentPartToClaudePart(content)
		if claudePart != "" {
			claudeContent := []byte("[]")
			claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", []byte(claudePart))
			return string(claudeContent), true
		}
		return content.Raw, false
	}

	return content.Raw, false
}
