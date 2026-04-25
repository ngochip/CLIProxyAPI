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
// Evaluation order: tools -> system -> messages.
//
// Strategy (up to 4 explicit breakpoints, all ephemeral/5m TTL):
//  1. Tools (last) - caches all tool definitions
//  2. System (last valid block) - caches system prompt
//  3. Second-to-last user message - enables multi-turn cache reuse
//  4. Last user message - rolling cache for the current turn
//
// All breakpoints use ephemeral (5m default) TTL to avoid the 2x
// cache-write cost of long-lived (1h) TTL. A top-level cache_control
// is added separately in the caller for automatic caching.
func applyCacheControlMarkers(requestJSON string) string {
	cacheControl := map[string]string{"type": "ephemeral"}
	breakpointsUsed := 0
	const maxBreakpoints = 4

	// Breakpoint 1: last tool
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

	// Breakpoint 2: last valid system block
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
					requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
					breakpointsUsed++
				}
			}
		} else if systemResult.Type == gjson.String && systemResult.String() != "" {
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

	// Breakpoints 3 & 4: second-to-last and last user messages
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

			// BP3: second-to-last user message
			if len(userMsgIndices) >= 2 && breakpointsUsed < maxBreakpoints {
				secondToLastIdx := userMsgIndices[len(userMsgIndices)-2]
				content := messages[secondToLastIdx].Get("content")
				if content.IsArray() {
					contentArray := content.Array()
					lastValidIdx := findLastCacheableContentIdx(contentArray)
					if lastValidIdx >= 0 {
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", secondToLastIdx, lastValidIdx)
						requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
						breakpointsUsed++
					}
				}
			}

			// BP4: last user message
			if len(userMsgIndices) >= 1 && breakpointsUsed < maxBreakpoints {
				lastUserIdx := userMsgIndices[len(userMsgIndices)-1]
				content := messages[lastUserIdx].Get("content")
				if content.IsArray() {
					contentArray := content.Array()
					lastValidIdx := findLastCacheableContentIdx(contentArray)
					if lastValidIdx >= 0 {
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", lastUserIdx, lastValidIdx)
						requestJSON, _ = sjson.Set(requestJSON, path, cacheControl)
						breakpointsUsed++
					}
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
