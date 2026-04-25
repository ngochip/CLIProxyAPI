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
// Anthropic allows up to 4 explicit breakpoints per request.
//
// Strategy aligned with opencode (4 breakpoints, all ephemeral/5m):
//  1. First system block - caches the main system prompt
//  2. Second system block (if present) - caches additional instructions
//  3. Second-to-last message (any role) - conversation history cache
//  4. Last message (any role) - rolling cache for the current turn
//
// Tools are NOT explicitly cached here; they are auto-cached by
// Anthropic via the top-level cache_control added in the caller.
// Caching messages regardless of role (including assistant) ensures
// thinking blocks (often thousands of tokens) are not reprocessed.
func applyCacheControlMarkers(requestJSON string) string {
	cc := map[string]string{"type": "ephemeral"}
	breakpointsUsed := 0
	const maxBreakpoints = 4

	// BP1-2: first 2 valid system blocks
	systemResult := gjson.Get(requestJSON, "system")
	if systemResult.Exists() {
		if systemResult.IsArray() {
			systemArray := systemResult.Array()
			cached := 0
			for i := 0; i < len(systemArray) && cached < 2 && breakpointsUsed < maxBreakpoints; i++ {
				blockType := systemArray[i].Get("type").String()
				if blockType == "thinking" || blockType == "redacted_thinking" {
					continue
				}
				if blockType == "text" && systemArray[i].Get("text").String() == "" {
					continue
				}
				path := fmt.Sprintf("system.%d.cache_control", i)
				requestJSON, _ = sjson.Set(requestJSON, path, cc)
				breakpointsUsed++
				cached++
			}
		} else if systemResult.Type == gjson.String && systemResult.String() != "" {
			systemArray := []map[string]interface{}{
				{
					"type":          "text",
					"text":          systemResult.String(),
					"cache_control": cc,
				},
			}
			requestJSON, _ = sjson.Set(requestJSON, "system", systemArray)
			breakpointsUsed++
		}
	}

	// BP3-4: last 2 messages regardless of role
	messagesResult := gjson.Get(requestJSON, "messages")
	if messagesResult.Exists() && messagesResult.IsArray() {
		messages := messagesResult.Array()
		count := len(messages)
		start := count - 2
		if start < 0 {
			start = 0
		}
		for i := start; i < count && breakpointsUsed < maxBreakpoints; i++ {
			content := messages[i].Get("content")
			if content.IsArray() {
				idx := findLastCacheableContentIdx(content.Array())
				if idx >= 0 {
					path := fmt.Sprintf("messages.%d.content.%d.cache_control", i, idx)
					requestJSON, _ = sjson.Set(requestJSON, path, cc)
					breakpointsUsed++
				}
			}
		}
	}

	return requestJSON
}

// findLastCacheableContentIdx returns the index of the last cacheable content block.
// Skips thinking, redacted_thinking, and empty text blocks.
func findLastCacheableContentIdx(contentArray []gjson.Result) int {
	for i := len(contentArray) - 1; i >= 0; i-- {
		blockType := contentArray[i].Get("type").String()

		if blockType == "thinking" || blockType == "redacted_thinking" {
			continue
		}


		if blockType == "text" && contentArray[i].Get("text").String() == "" {
			continue
		}

		return i
	}
	return -1
}
