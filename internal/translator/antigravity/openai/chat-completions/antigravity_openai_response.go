// Package openai provides response translation functionality for Gemini CLI to OpenAI API compatibility.
// This package handles the conversion of Gemini CLI API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertCliResponseToOpenAIChatParams holds parameters for response conversion.
type convertCliResponseToOpenAIChatParams struct {
	UnixTimestamp        int64
	FunctionIndex        int
	SawToolCall          bool   // Tracks if any tool call was seen in the entire stream
	UpstreamFinishReason string // Caches the upstream finish reason for final chunk
	SanitizedNameMap     map[string]string
	// Cursor thinking support: accumulate thinking text + signature across streaming chunks
	InThinking       bool
	ThinkingText     strings.Builder
	ThoughtSignature string
}

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// ConvertAntigravityResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini CLI API format to the OpenAI Chat Completions streaming format.
// It processes various Gemini CLI event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertAntigravityResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &convertCliResponseToOpenAIChatParams{
			UnixTimestamp:    0,
			FunctionIndex:    0,
			SanitizedNameMap: util.SanitizedToolNameMap(originalRequestRawJSON),
		}
	}
	if (*param).(*convertCliResponseToOpenAIChatParams).SanitizedNameMap == nil {
		(*param).(*convertCliResponseToOpenAIChatParams).SanitizedNameMap = util.SanitizedToolNameMap(originalRequestRawJSON)
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return [][]byte{}
	}

	// Initialize the OpenAI SSE template.
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJSON, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			(*param).(*convertCliResponseToOpenAIChatParams).UnixTimestamp = t.Unix()
		}
		template, _ = sjson.SetBytes(template, "created", (*param).(*convertCliResponseToOpenAIChatParams).UnixTimestamp)
	} else {
		template, _ = sjson.SetBytes(template, "created", (*param).(*convertCliResponseToOpenAIChatParams).UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := gjson.GetBytes(rawJSON, "response.responseId"); responseIDResult.Exists() {
		template, _ = sjson.SetBytes(template, "id", responseIDResult.String())
	}

	// Cache the finish reason - do NOT set it in output yet (will be set on final chunk)
	if finishReasonResult := gjson.GetBytes(rawJSON, "response.candidates.0.finishReason"); finishReasonResult.Exists() {
		(*param).(*convertCliResponseToOpenAIChatParams).UpstreamFinishReason = strings.ToUpper(finishReasonResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJSON, "response.usageMetadata"); usageResult.Exists() {
		cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		template, _ = sjson.SetBytes(template, "usage.prompt_tokens", promptTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
		// Include cached token count if present (indicates prompt caching is working)
		if cachedTokenCount > 0 {
			var err error
			template, err = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokenCount)
			if err != nil {
				log.Warnf("antigravity openai response: failed to set cached_tokens: %v", err)
			}
		}
	}

	// Process the main content part of the response.
	partsResult := gjson.GetBytes(rawJSON, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			thoughtSignatureResult := partResult.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = partResult.Get("thought_signature")
			}
			inlineDataResult := partResult.Get("inlineData")
			if !inlineDataResult.Exists() {
				inlineDataResult = partResult.Get("inline_data")
			}

			hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
			hasContentPayload := partTextResult.Exists() || functionCallResult.Exists() || inlineDataResult.Exists()

			// Cache thoughtSignature cho thinking accumulator
			if hasThoughtSignature && !hasContentPayload {
				sig := thoughtSignatureResult.String()
				if sig != "" && sig != geminiCLIFunctionThoughtSignature {
					(*param).(*convertCliResponseToOpenAIChatParams).ThoughtSignature = sig
				}
				continue
			}

			if partTextResult.Exists() {
				textContent := partTextResult.String()
				p := (*param).(*convertCliResponseToOpenAIChatParams)

				if partResult.Get("thought").Bool() {
					// Cursor thinking: stream thinking qua content field với <think> tags
					var thinkContent string
					if !p.InThinking {
						p.InThinking = true
						thinkContent = "<think>\n" + textContent
					} else {
						thinkContent = textContent
					}
					p.ThinkingText.WriteString(textContent)
					// Cache thoughtSignature nếu có trong part này
					sig := partResult.Get("thoughtSignature").String()
					if sig == "" {
						sig = partResult.Get("thought_signature").String()
					}
					if sig != "" && sig != geminiCLIFunctionThoughtSignature {
						p.ThoughtSignature = sig
					}
					template, _ = sjson.SetBytes(template, "choices.0.delta.content", thinkContent)
				} else {
					// Transition từ thinking sang normal text: close <think> tag + thinkId
					if p.InThinking {
						p.InThinking = false
						thinkingText := p.ThinkingText.String()
						thinkingID := cache.GenerateThinkingID(thinkingText)
						if thinkingText != "" {
							cache.CacheThinking(thinkingID, thinkingText, p.ThoughtSignature)
						}
						closingTag := "\n</think>\n<!--thinkId:" + thinkingID + "-->\n" + textContent
						template, _ = sjson.SetBytes(template, "choices.0.delta.content", closingTag)
					} else {
						template, _ = sjson.SetBytes(template, "choices.0.delta.content", textContent)
					}
				}
				template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			} else if functionCallResult.Exists() {
				// Handle function call content.
				(*param).(*convertCliResponseToOpenAIChatParams).SawToolCall = true // Persist across chunks
				toolCallsResult := gjson.GetBytes(template, "choices.0.delta.tool_calls")
				functionCallIndex := (*param).(*convertCliResponseToOpenAIChatParams).FunctionIndex
				(*param).(*convertCliResponseToOpenAIChatParams).FunctionIndex++
				if toolCallsResult.Exists() && toolCallsResult.IsArray() {
					functionCallIndex = len(toolCallsResult.Array())
				} else {
					template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
				}

				functionCallTemplate := []byte(`{"id": "","index": 0,"type": "function","function": {"name": "","arguments": ""}}`)
				fcName := util.RestoreSanitizedToolName((*param).(*convertCliResponseToOpenAIChatParams).SanitizedNameMap, functionCallResult.Get("name").String())
				// Ưu tiên dùng id từ upstream Antigravity nếu có, nếu không thì generate mới
				upstreamID := functionCallResult.Get("id").String()
				if upstreamID != "" {
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "id", upstreamID)
				} else {
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "id", fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1)))
				}
				functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "index", functionCallIndex)
				functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.arguments", fcArgsResult.Raw)
				}
				template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
				template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallTemplate)
			} else if inlineDataResult.Exists() {
				data := inlineDataResult.Get("data").String()
				if data == "" {
					continue
				}
				mimeType := inlineDataResult.Get("mimeType").String()
				if mimeType == "" {
					mimeType = inlineDataResult.Get("mime_type").String()
				}
				if mimeType == "" {
					mimeType = "image/png"
				}
				imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
				imagesResult := gjson.GetBytes(template, "choices.0.delta.images")
				if !imagesResult.Exists() || !imagesResult.IsArray() {
					template, _ = sjson.SetRawBytes(template, "choices.0.delta.images", []byte(`[]`))
				}
				imageIndex := len(gjson.GetBytes(template, "choices.0.delta.images").Array())
				imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
				imagePayload, _ = sjson.SetBytes(imagePayload, "index", imageIndex)
				imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)
				template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
				template, _ = sjson.SetRawBytes(template, "choices.0.delta.images.-1", imagePayload)
			}
		}
	}

	// Close thinking nếu vẫn đang mở (thinking -> finish hoặc thinking -> tool_call)
	params := (*param).(*convertCliResponseToOpenAIChatParams)
	if params.InThinking {
		hasFunctionCallInChunk := false
		if partsResult.IsArray() {
			for _, pr := range partsResult.Array() {
				if pr.Get("functionCall").Exists() {
					hasFunctionCallInChunk = true
					break
				}
			}
		}
		finishReasonExists := gjson.GetBytes(rawJSON, "response.candidates.0.finishReason").Exists()
		if hasFunctionCallInChunk || finishReasonExists {
			params.InThinking = false
			thinkingText := params.ThinkingText.String()
			thinkingID := cache.GenerateThinkingID(thinkingText)
			if thinkingText != "" {
				cache.CacheThinking(thinkingID, thinkingText, params.ThoughtSignature)
			}
			closingTag := "\n</think>\n<!--thinkId:" + thinkingID + "-->\n"
			// Prepend closing tag vào content hiện tại nếu có
			existingContent := gjson.GetBytes(template, "choices.0.delta.content").String()
			template, _ = sjson.SetBytes(template, "choices.0.delta.content", closingTag+existingContent)
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		}
	}

	// Determine finish_reason only on the final chunk (has both finishReason and usage metadata)
	upstreamFinishReason := params.UpstreamFinishReason
	sawToolCall := params.SawToolCall

	usageExists := gjson.GetBytes(rawJSON, "response.usageMetadata").Exists()
	isFinalChunk := upstreamFinishReason != "" && usageExists

	if isFinalChunk {
		var finishReason string
		if sawToolCall {
			finishReason = "tool_calls"
		} else if upstreamFinishReason == "MAX_TOKENS" {
			finishReason = "max_tokens"
		} else {
			finishReason = "stop"
		}
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
		template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", strings.ToLower(upstreamFinishReason))
	}

	return [][]byte{template}
}

// ConvertAntigravityResponseToOpenAINonStream converts a non-streaming Gemini CLI response to a non-streaming OpenAI response.
// This function processes the complete Gemini CLI response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertAntigravityResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		return ConvertGeminiResponseToOpenAINonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, []byte(responseResult.Raw), param)
	}
	return []byte{}
}
