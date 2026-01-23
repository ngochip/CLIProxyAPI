// Package openai provides request translation functionality for OpenAI to Claude Code API compatibility.
// It handles parsing and transforming OpenAI Chat Completions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Claude Code API's expected format.
package chat_completions

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"

	// log "github.com/sirupsen/logrus"
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
	// Pattern cho thinkId marker: ```plaintext:thinkId:xxx```
	thinkIdRegex = regexp.MustCompile("```plaintext:thinkId:([a-f0-9]+)```")
	// Legacy patterns cho backward compatibility
	legacyThinkingRegex  = regexp.MustCompile("```plaintext:Thinking\\n([\\s\\S]*?)```")
	legacySignatureRegex = regexp.MustCompile("```plaintext:Signature:([\\s\\S]*?)```")
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

	// Tìm assistant message cuối cùng
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	// Nếu không có assistant message, không cần fix
	if lastAssistantIdx == -1 {
		return requestJSON
	}

	// Kiểm tra content của assistant message cuối
	lastAssistant := messages[lastAssistantIdx]
	content := lastAssistant.Get("content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return requestJSON
	}

	contentArray := content.Array()
	firstContentType := contentArray[0].Get("type").String()

	// Nếu đã bắt đầu bằng thinking hoặc redacted_thinking, OK
	if firstContentType == "thinking" || firstContentType == "redacted_thinking" {
		return requestJSON
	}

	// Nếu không có thinking block → Disable thinking tạm thời
	// (Claude sẽ báo lỗi nếu thinking enabled mà không có thinking content)
	result, _ := sjson.Delete(requestJSON, "thinking")

	// log.Warnf("⚠ Disabled thinking for request (assistant message has no thinking block)")

	return result
}

// extractThinkingFromContent trích xuất thinking từ text content
// Hỗ trợ 2 formats:
// 1. New format: thinkId marker ```plaintext:thinkId:xxx``` -> lookup cache
// 2. Legacy format: ```plaintext:Thinking\n...\n``` + ```plaintext:Signature:...```
func extractThinkingFromContent(text string) []interface{} {
	// Thử tìm thinkId marker trước (new format)
	idMatch := thinkIdRegex.FindStringSubmatch(text)
	if len(idMatch) > 1 {
		thinkingID := idMatch[1]
		entry := cache.GetCachedThinking(thinkingID)

		// Nếu tìm thấy cache với valid signature → restore thinking block
		if entry != nil && cache.HasValidSignature(entry.Signature) {
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

	// Thử legacy format (backward compatibility)
	thinkingMatch := legacyThinkingRegex.FindStringSubmatch(text)
	signatureMatch := legacySignatureRegex.FindStringSubmatch(text)
	if len(thinkingMatch) > 0 && len(signatureMatch) > 0 {
		thinkingText := thinkingMatch[1]
		signatureText := signatureMatch[1]

		// Unescape ``` trong thinking text

		// Xóa các blocks khỏi text gốc
		remainingText := legacyThinkingRegex.ReplaceAllString(text, "")
		remainingText = legacySignatureRegex.ReplaceAllString(remainingText, "")
		remainingText = strings.TrimSpace(remainingText)

		var parts []interface{}

		// Part 1: thinking block với thinking và signature
		thinkingPart := map[string]interface{}{
			"type":      "thinking",
			"thinking":  thinkingText,
			"signature": signatureText,
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

	// No valid thinking format found → clean up và return text only
	// Remove any orphan markers
	cleanText := thinkTagRegex.ReplaceAllString(text, "")
	cleanText = thinkIdRegex.ReplaceAllString(cleanText, "")
	cleanText = legacyThinkingRegex.ReplaceAllString(cleanText, "")
	cleanText = legacySignatureRegex.ReplaceAllString(cleanText, "")
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
	rawJSON := bytes.Clone(inputRawJSON)

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

	// Base Claude Code API template with default max_tokens value
	out := fmt.Sprintf(`{"model":"","max_tokens":64000,"messages":[],"metadata":{"user_id":"%s"}}`, userID)

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI reasoning_effort to Claude thinking config.
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			budget, ok := thinking.ConvertLevelToBudget(effort)
			if ok {
				switch budget {
				case 0:
					out, _ = sjson.Set(out, "thinking.type", "disabled")
				case -1:
					out, _ = sjson.Set(out, "thinking.type", "enabled")
				default:
					if budget > 0 {
						out, _ = sjson.Set(out, "thinking.type", "enabled")
						out, _ = sjson.Set(out, "thinking.budget_tokens", budget)
					}
				}
				// log.Debugf("Applied thinking from reasoning_effort=%s: type=%s, budget=%d", effort, gjson.Get(out, "thinking.type").String(), budget)
			} else {
				// log.Warnf("Failed to convert reasoning_effort=%s to budget for model=%s", effort, modelName)
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
	out, _ = sjson.Set(out, "model", modelName)

	// Max tokens configuration with fallback to default value
	//disable max_tokens for cursor unlimited mode
	// if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
	// 	out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	// }

	// Temperature setting for controlling response randomness
	// Khi thinking được bật từ request JSON, set temperature = 1
	// Note: Khi thinking được bật từ metadata (model alias), temperature sẽ được set trong claude_executor.go
	thinkingEnabled := gjson.Get(out, "thinking.type").String() == "enabled"
	if thinkingEnabled {
		out, _ = sjson.Set(out, "temperature", 1)
	} else if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.Set(out, "temperature", temp.Float())
	}

	// Top P setting for nucleus sampling
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "top_p", topP.Float())
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
				out, _ = sjson.Set(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.Set(out, "stop_sequences", []string{stop.String()})
		}
	}

	// Stream configuration to enable or disable streaming responses
	out, _ = sjson.Set(out, "stream", stream)
	if system := root.Get("system"); system.Exists() {
		out, _ = sjson.SetRaw(out, "system", system.Raw)
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
					systemMsg := `{"role":"user","content":[]}`
					out, _ = sjson.SetRaw(out, "messages.-1", systemMsg)
					systemMessageIndex = messageIndex
					messageIndex++
				}
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := `{"type":"text","text":""}`
					textPart, _ = sjson.Set(textPart, "text", contentResult.String())
					out, _ = sjson.SetRaw(out, fmt.Sprintf("messages.%d.content.-1", systemMessageIndex), textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := `{"type":"text","text":""}`
							textPart, _ = sjson.Set(textPart, "text", part.Get("text").String())
							out, _ = sjson.SetRaw(out, fmt.Sprintf("messages.%d.content.-1", systemMessageIndex), textPart)
						}
						return true
					})
				}
			case "user", "assistant":
				msg := `{"role":"","content":[]}`
				msg, _ = sjson.Set(msg, "role", role)

				// Handle content based on its type (string or array)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					parts := extractThinkingFromContent(contentResult.String())
					for _, part := range parts {
						msg, _ = sjson.Set(msg, "content.-1", part)
					}
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						partType := part.Get("type").String()

						switch partType {
						case "text":
							parts := extractThinkingFromContent(part.Get("text").String())
							for _, p := range parts {
								msg, _ = sjson.Set(msg, "content.-1", p)
							}

						case "image_url":
							// Convert OpenAI image format to Claude Code format
							imageURL := part.Get("image_url.url").String()
							if strings.HasPrefix(imageURL, "data:") {
								// Extract base64 data and media type from data URL
								parts := strings.Split(imageURL, ",")
								if len(parts) == 2 {
									mediaTypePart := strings.Split(parts[0], ";")[0]
									mediaType := strings.TrimPrefix(mediaTypePart, "data:")
									data := parts[1]

									imagePart := `{"type":"image","source":{"type":"base64","media_type":"","data":""}}`
									imagePart, _ = sjson.Set(imagePart, "source.media_type", mediaType)
									imagePart, _ = sjson.Set(imagePart, "source.data", data)
									msg, _ = sjson.SetRaw(msg, "content.-1", imagePart)
								}
							}

						case "image":
							// Hỗ trợ nhận ảnh base64 trực tiếp theo format Claude native
							// Request format: {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
							source := part.Get("source")
							if source.Exists() && source.Get("type").String() == "base64" {
								imagePart := `{"type":"image","source":{"type":"base64","media_type":"","data":""}}`
								imagePart, _ = sjson.Set(imagePart, "source.media_type", source.Get("media_type").String())
								imagePart, _ = sjson.Set(imagePart, "source.data", source.Get("data").String())
								msg, _ = sjson.SetRaw(msg, "content.-1", imagePart)
							}

						case "tool_use":
							// Handle tool use messages conversion
							toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
							toolUse, _ = sjson.Set(toolUse, "id", part.Get("id").String())
							toolUse, _ = sjson.Set(toolUse, "name", part.Get("name").String())
							toolUse, _ = sjson.SetRaw(toolUse, "input", part.Get("input").Raw)

							msg, _ = sjson.SetRaw(msg, "content.-1", toolUse)

						case "tool_result":
							// Handle tool result messages conversion
							toolResult := `{"type":"tool_result","tool_use_id":"","content":""}`
							toolResult, _ = sjson.Set(toolResult, "tool_use_id", part.Get("tool_use_id").String())
							toolResult, _ = sjson.Set(toolResult, "content", part.Get("content").String())
							msg, _ = sjson.SetRaw(msg, "content.-1", toolResult)
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
							toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
							toolUse, _ = sjson.Set(toolUse, "id", toolCallID)
							toolUse, _ = sjson.Set(toolUse, "name", function.Get("name").String())

							// Parse arguments for the tool call
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" && gjson.Valid(argsStr) {
									argsJSON := gjson.Parse(argsStr)
									if argsJSON.IsObject() {
										toolUse, _ = sjson.SetRaw(toolUse, "input", argsJSON.Raw)
									} else {
										toolUse, _ = sjson.SetRaw(toolUse, "input", "{}")
									}
								} else {
									toolUse, _ = sjson.SetRaw(toolUse, "input", "{}")
								}
							} else {
								toolUse, _ = sjson.SetRaw(toolUse, "input", "{}")
							}

							msg, _ = sjson.SetRaw(msg, "content.-1", toolUse)
						}
						return true
					})
				}

				out, _ = sjson.SetRaw(out, "messages.-1", msg)
				messageIndex++

			case "tool":
				// Handle tool result messages conversion
				toolCallID := message.Get("tool_call_id").String()
				content := message.Get("content").String()

				msg := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":""}]}`
				msg, _ = sjson.Set(msg, "content.0.tool_use_id", toolCallID)
				msg, _ = sjson.Set(msg, "content.0.content", content)
				out, _ = sjson.SetRaw(out, "messages.-1", msg)
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
				anthropicTool := `{"name":"","description":""}`
				anthropicTool, _ = sjson.Set(anthropicTool, "name", function.Get("name").String())
				anthropicTool, _ = sjson.Set(anthropicTool, "description", function.Get("description").String())

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRaw(anthropicTool, "input_schema", parameters.Raw)
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRaw(anthropicTool, "input_schema", parameters.Raw)
				}

				out, _ = sjson.SetRaw(out, "tools.-1", anthropicTool)
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

				out, _ = sjson.Set(out, "tools.-1", anthropicTool)
				hasAnthropicTools = true
			}
			return true
		})

		if !hasAnthropicTools {
			out, _ = sjson.Delete(out, "tools")
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
				out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"auto"}`)
			case "required":
				out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"any"}`)
			}
		case gjson.JSON:
			// Specific tool choice mapping
			if toolChoice.Get("type").String() == "function" {
				functionName := toolChoice.Get("function.name").String()
				toolChoiceJSON := `{"type":"tool","name":""}`
				toolChoiceJSON, _ = sjson.Set(toolChoiceJSON, "name", functionName)
				out, _ = sjson.SetRaw(out, "tool_choice", toolChoiceJSON)
			}
		default:
		}
	}

	// Fix assistant messages when thinking is enabled
	// Claude API yêu cầu: "When thinking is enabled, a final assistant message must start
	// with a thinking block (preceeding the lastmost set of tool_use and tool_result blocks)"
	out = ensureAssistantThinkingBlock(out)

	// Apply cache_control markers để tối ưu prompt caching
	// Anthropic cho phép tối đa 4 breakpoints, đặt ở cuối các phần ổn định
	out = applyCacheControlMarkers(out)

	return []byte(out)
}

// applyCacheControlMarkers thêm cache_control markers vào request để tối ưu prompt caching
// Anthropic prompt caching cho phép tối đa 4 breakpoints
// Chiến lược đặt breakpoints:
// 1. System instructions (cuối cùng) - ổn định nhất, ít thay đổi
// 2. Tools array (cuối cùng) - thường không thay đổi giữa các requests
// 3. Messages đầu tiên (user message đầu) - conversation history ổn định
// 4. Messages cuối (user message cuối cùng trước assistant) - context gần nhất
func applyCacheControlMarkers(requestJSON string) string {
	cacheControl := map[string]string{"type": "ephemeral"}
	breakpointsUsed := 0
	const maxBreakpoints = 4

	// Breakpoint 1: System instructions (cuối cùng)
	// System thường là phần ổn định nhất, ít thay đổi giữa các requests
	systemResult := gjson.Get(requestJSON, "system")
	if systemResult.Exists() && systemResult.IsArray() {
		systemArray := systemResult.Array()
		if len(systemArray) > 0 && breakpointsUsed < maxBreakpoints {
			lastIdx := len(systemArray) - 1
			path := fmt.Sprintf("system.%d.cache_control", lastIdx)
			requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
			breakpointsUsed++
		}
	}

	// Breakpoint 2: Tools array (cuối cùng)
	// Tools declaration thường không thay đổi trong một session
	toolsResult := gjson.Get(requestJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolsArray := toolsResult.Array()
		if len(toolsArray) > 0 && breakpointsUsed < maxBreakpoints {
			lastIdx := len(toolsArray) - 1
			path := fmt.Sprintf("tools.%d.cache_control", lastIdx)
			requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
			breakpointsUsed++
		}
	}

	// Breakpoint 3 & 4: Messages
	// Đặt cache_control ở các vị trí chiến lược trong message history
	messagesResult := gjson.Get(requestJSON, "messages")
	if messagesResult.Exists() && messagesResult.IsArray() {
		messages := messagesResult.Array()
		if len(messages) > 0 && breakpointsUsed < maxBreakpoints {
			// Tìm các vị trí tốt để đặt breakpoint trong messages
			// Ưu tiên: user messages với content dài hoặc ở vị trí chiến lược

			// Chiến lược: đặt breakpoint ở user message cuối cùng trước assistant cuối
			// Điều này giúp cache phần lớn conversation history
			lastUserMsgIdx := -1
			for i := len(messages) - 1; i >= 0; i-- {
				role := messages[i].Get("role").String()
				if role == "user" {
					lastUserMsgIdx = i
					break
				}
			}

			if lastUserMsgIdx >= 0 && breakpointsUsed < maxBreakpoints {
				// Đặt cache_control ở content block cuối của user message
				content := messages[lastUserMsgIdx].Get("content")
				if content.IsArray() {
					contentArray := content.Array()
					if len(contentArray) > 0 {
						lastContentIdx := len(contentArray) - 1
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", lastUserMsgIdx, lastContentIdx)
						requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
						breakpointsUsed++
					}
				}
			}

			// Nếu còn breakpoint, đặt thêm ở user message đầu tiên (nếu khác với message cuối)
			// Điều này giúp cache system context và initial user prompt
			if breakpointsUsed < maxBreakpoints && len(messages) > 2 {
				firstUserMsgIdx := -1
				for i := 0; i < len(messages); i++ {
					role := messages[i].Get("role").String()
					if role == "user" {
						firstUserMsgIdx = i
						break
					}
				}

				if firstUserMsgIdx >= 0 && firstUserMsgIdx != lastUserMsgIdx {
					content := messages[firstUserMsgIdx].Get("content")
					if content.IsArray() {
						contentArray := content.Array()
						if len(contentArray) > 0 {
							lastContentIdx := len(contentArray) - 1
							path := fmt.Sprintf("messages.%d.content.%d.cache_control", firstUserMsgIdx, lastContentIdx)
							requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
							breakpointsUsed++
						}
					}
				}
			}
		}
	}

	return requestJSON
}
