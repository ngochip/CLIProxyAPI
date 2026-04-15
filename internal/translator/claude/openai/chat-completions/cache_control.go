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

// applyCacheControlMarkers adds cache_control markers to optimize prompt caching.
// Anthropic allows up to 4 breakpoints per request.
//
// Evaluation order: tools -> system -> messages.
//
// Strategy (3 explicit breakpoints + 1 automatic):
//  1. Tools (last) with TTL 1h - rarely change within a session
//  2. System (last valid block) with TTL 1h - most stable content
//  3. Second-to-last user message with default 5m TTL - enables
//     multi-turn cache reuse (per Anthropic docs)
//
// A top-level cache_control (automatic caching) is added separately
// in the caller so Anthropic auto-advances the breakpoint as the
// conversation grows, using the remaining slot.
//
// TTL ordering constraint: 1h blocks MUST precede 5m blocks in
// evaluation order. Since tools and system precede messages, this
// is naturally satisfied.
func applyCacheControlMarkers(requestJSON string) string {
	cacheControl1h := map[string]string{"type": "ephemeral", "ttl": "1h"}
	cacheControl5m := map[string]string{"type": "ephemeral"}
	breakpointsUsed := 0
	const maxBreakpoints = 3 // reserve 1 slot for top-level automatic caching

	// Breakpoint 1: last tool with 1h TTL
	toolsResult := gjson.Get(requestJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolsArray := toolsResult.Array()
		if len(toolsArray) > 0 && breakpointsUsed < maxBreakpoints {
			lastIdx := len(toolsArray) - 1
			path := fmt.Sprintf("tools.%d.cache_control", lastIdx)
			requestJSON, _ = sjson.Set(requestJSON, path, cacheControl1h)
			breakpointsUsed++
		}
	}

	// Breakpoint 2: last valid system block with 1h TTL
	systemResult := gjson.Get(requestJSON, "system")
	if systemResult.Exists() && breakpointsUsed < maxBreakpoints {
		if systemResult.IsArray() {
			systemArray := systemResult.Array()
			if len(systemArray) > 0 {
				lastValidIdx := -1
				for i := len(systemArray) - 1; i >= 0; i-- {
					blockType := systemArray[i].Get("type").String()
					if blockType == "thinking" || blockType == "redacted_thinking" {
						continue
					}
					if blockType == "text" && systemArray[i].Get("text").String() == "" {
						continue
					}
					lastValidIdx = i
					break
				}
				if lastValidIdx >= 0 {
					path := fmt.Sprintf("system.%d.cache_control", lastValidIdx)
					requestJSON, _ = sjson.Set(requestJSON, path, cacheControl1h)
					breakpointsUsed++
				}
			}
		} else if systemResult.Type == gjson.String && systemResult.String() != "" {
			systemText := systemResult.String()
			systemArray := []map[string]interface{}{
				{
					"type":          "text",
					"text":          systemText,
					"cache_control": cacheControl1h,
				},
			}
			requestJSON, _ = sjson.Set(requestJSON, "system", systemArray)
			breakpointsUsed++
		}
	}

	// Breakpoint 3: second-to-last user message with 5m TTL
	// Per Anthropic docs: caching the second-to-last user turn lets the
	// model reuse the earlier cache on subsequent turns.
	messagesResult := gjson.Get(requestJSON, "messages")
	if messagesResult.Exists() && messagesResult.IsArray() {
		messages := messagesResult.Array()
		if len(messages) > 0 && breakpointsUsed < maxBreakpoints {
			var userMsgIndices []int
			for i, msg := range messages {
				if msg.Get("role").String() == "user" {
					userMsgIndices = append(userMsgIndices, i)
				}
			}

			if len(userMsgIndices) >= 2 {
				secondToLastIdx := userMsgIndices[len(userMsgIndices)-2]
				content := messages[secondToLastIdx].Get("content")
				if content.IsArray() {
					contentArray := content.Array()
					lastValidIdx := findLastCacheableContentIdx(contentArray)
					if lastValidIdx >= 0 {
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", secondToLastIdx, lastValidIdx)
						requestJSON, _ = sjson.Set(requestJSON, path, cacheControl5m)
						breakpointsUsed++
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
