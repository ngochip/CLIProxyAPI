// Package chat_completions provides cache control functionality for Claude API requests.
// This file contains prompt caching logic to optimize API usage by reducing processing time and costs.
//
// Prompt caching allows resuming from specific prefixes in prompts, significantly reducing
// processing time (up to 85%) and costs (up to 90%) for repetitive tasks.
//
// Tách riêng để tránh conflict khi merge main branch vào cursor branch.
package chat_completions

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyCacheControlMarkers thêm cache_control markers vào request để tối ưu prompt caching
// Anthropic prompt caching cho phép tối đa 4 breakpoints
//
// QUAN TRỌNG: Thứ tự hierarchy của Claude API là: tools → system → messages
// Cache prefixes được tạo theo thứ tự này, nên ta đặt breakpoints theo đúng thứ tự
//
// Chiến lược đặt breakpoints:
// 1. Tools array (cuối cùng) - thường không thay đổi giữa các requests
// 2. System instructions (cuối cùng) - ổn định nhất, ít thay đổi
// 3. Messages đầu tiên (user message đầu) - conversation history ổn định
// 4. Messages cuối (user message cuối cùng) - context gần nhất
//
// Lưu ý:
// - Thinking blocks không thể được cache trực tiếp với cache_control
// - Empty text blocks không thể cached
// - Minimum cacheable tokens: 1024 (Sonnet/Opus 4), 2048 (Haiku 3), 4096 (Opus 4.5/Haiku 4.5)
// - Cache TTL mặc định: 5 phút, tự động refresh mỗi lần sử dụng
// - Cache write cost: 125% base input token price
// - Cache read cost: 10% base input token price
func applyCacheControlMarkers(requestJSON string) string {
	cacheControl := map[string]string{"type": "ephemeral"}
	breakpointsUsed := 0
	const maxBreakpoints = 4

	// Breakpoint 1: Tools array (cuối cùng)
	// Tools declaration thường không thay đổi trong một session
	// Đặt trước system vì theo hierarchy: tools → system → messages
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

	// Breakpoint 2: System instructions (cuối cùng)
	// System thường là phần ổn định nhất, ít thay đổi giữa các requests
	// Hỗ trợ cả array format và string format
	systemResult := gjson.Get(requestJSON, "system")
	if systemResult.Exists() && breakpointsUsed < maxBreakpoints {
		if systemResult.IsArray() {
			// System là array of content blocks
			systemArray := systemResult.Array()
			if len(systemArray) > 0 {
				// Tìm content block cuối có nội dung (skip empty blocks)
				lastValidIdx := -1
				for i := len(systemArray) - 1; i >= 0; i-- {
					blockType := systemArray[i].Get("type").String()
					// Skip thinking blocks (không thể cache trực tiếp)
					if blockType == "thinking" || blockType == "redacted_thinking" {
						continue
					}
					// Check có nội dung không
					if blockType == "text" && systemArray[i].Get("text").String() == "" {
						continue
					}
					lastValidIdx = i
					break
				}
				if lastValidIdx >= 0 {
					path := fmt.Sprintf("system.%d.cache_control", lastValidIdx)
					requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
					breakpointsUsed++
				}
			}
		} else if systemResult.Type == gjson.String && systemResult.String() != "" {
			// System là string đơn giản - convert sang array format để cache được
			systemText := systemResult.String()
			systemArray := []map[string]interface{}{
				{
					"type":          "text",
					"text":          systemText,
					"cache_control": cacheControl,
				},
			}
			requestJSON, _ = sjson.Set(requestJSON, "system", systemArray)
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

			// Chiến lược: đặt breakpoint ở user message cuối cùng
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
				// Skip thinking blocks và empty blocks
				content := messages[lastUserMsgIdx].Get("content")
				if content.IsArray() {
					contentArray := content.Array()
					lastValidIdx := findLastCacheableContentIdx(contentArray)
					if lastValidIdx >= 0 {
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", lastUserMsgIdx, lastValidIdx)
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
						lastValidIdx := findLastCacheableContentIdx(contentArray)
						if lastValidIdx >= 0 {
							path := fmt.Sprintf("messages.%d.content.%d.cache_control", firstUserMsgIdx, lastValidIdx)
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

// findLastCacheableContentIdx tìm index của content block cuối cùng có thể cache được
// Skip các blocks không thể cache: thinking, redacted_thinking, empty text
func findLastCacheableContentIdx(contentArray []gjson.Result) int {
	for i := len(contentArray) - 1; i >= 0; i-- {
		blockType := contentArray[i].Get("type").String()

		// Thinking blocks không thể cache trực tiếp
		if blockType == "thinking" || blockType == "redacted_thinking" {
			continue
		}

		// Empty text blocks không thể cached
		if blockType == "text" && contentArray[i].Get("text").String() == "" {
			continue
		}

		return i
	}
	return -1
}
