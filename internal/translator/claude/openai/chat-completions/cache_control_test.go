package chat_completions

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyCacheControlMarkers_AllSections(t *testing.T) {
	input := fmt.Sprintf(`{
		"tools": [
			{"name": "Read", "description": "Read file", "input_schema": {"type": "object"}},
			{"name": "Write", "description": "Write file", "input_schema": {"type": "object"}}
		],
		"system": [
			{"type": "text", "text": "You are Claude Code."},
			{"type": "text", "text": "Additional instructions."}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "First question"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Answer 1"}]},
			{"role": "user", "content": [{"type": "text", "text": "Second question"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Answer 2"}]},
			{"role": "user", "content": [{"type": "text", "text": "Third question"}]}
		]
	}`)

	result := applyCacheControlMarkers(input)

	// BP1: last tool
	if gjson.Get(result, "tools.1.cache_control.type").String() != "ephemeral" {
		t.Error("last tool should have cache_control")
	}
	if gjson.Get(result, "tools.0.cache_control").Exists() {
		t.Error("first tool should NOT have cache_control")
	}

	// BP2: last system
	if gjson.Get(result, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("last system element should have cache_control")
	}
	if gjson.Get(result, "system.0.cache_control").Exists() {
		t.Error("first system element should NOT have cache_control")
	}

	// BP3: second-to-last user (messages.2)
	if gjson.Get(result, "messages.2.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("second-to-last user message should have cache_control")
	}

	// BP4: last user (messages.4)
	if gjson.Get(result, "messages.4.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("last user message should have cache_control")
	}

	// Non-user messages should not have cache_control
	if gjson.Get(result, "messages.1.content.0.cache_control").Exists() {
		t.Error("assistant messages should NOT have cache_control")
	}
}

func TestApplyCacheControlMarkers_NoTTL1h(t *testing.T) {
	input := `{
		"tools": [{"name": "tool1", "description": "Tool", "input_schema": {"type": "object"}}],
		"system": [{"type": "text", "text": "System prompt"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "A1"}]},
			{"role": "user", "content": [{"type": "text", "text": "Q2"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// No block should have ttl=1h
	checkPaths := []string{
		"tools.0.cache_control.ttl",
		"system.0.cache_control.ttl",
		"messages.0.content.0.cache_control.ttl",
		"messages.2.content.0.cache_control.ttl",
	}
	for _, path := range checkPaths {
		val := gjson.Get(result, path)
		if val.Exists() && val.String() == "1h" {
			t.Errorf("path %q should NOT have ttl=1h", path)
		}
	}
}

func TestApplyCacheControlMarkers_LastUserCached(t *testing.T) {
	input := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Hi"}]},
			{"role": "user", "content": [{"type": "text", "text": "How are you?"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// Both user messages should have cache_control
	if gjson.Get(result, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("second-to-last user (messages.0) should have cache_control")
	}
	if gjson.Get(result, "messages.2.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("last user (messages.2) should have cache_control")
	}
}

func TestApplyCacheControlMarkers_SingleUser(t *testing.T) {
	input := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Only one user message"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// With only 1 user message, BP3 (second-to-last) is skipped, BP4 (last) applies
	if gjson.Get(result, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("single user message should have cache_control (as last user)")
	}
}

func TestApplyCacheControlMarkers_SkipThinking(t *testing.T) {
	input := `{
		"messages": [
			{"role": "user", "content": [
				{"type": "thinking", "thinking": "some thinking"},
				{"type": "text", "text": "Actual content"}
			]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// cache_control should be on the text block (idx 1), not the thinking block (idx 0)
	if gjson.Get(result, "messages.0.content.0.cache_control").Exists() {
		t.Error("thinking block should NOT have cache_control")
	}
	if gjson.Get(result, "messages.0.content.1.cache_control.type").String() != "ephemeral" {
		t.Error("text block after thinking should have cache_control")
	}
}

func TestApplyCacheControlMarkers_MaxBreakpoints(t *testing.T) {
	toolsJSON := `[`
	for i := 0; i < 50; i++ {
		if i > 0 {
			toolsJSON += ","
		}
		toolsJSON += fmt.Sprintf(`{"name": "tool%d", "description": "Tool %d", "input_schema": {"type": "object"}}`, i, i)
	}
	toolsJSON += `]`

	input := fmt.Sprintf(`{
		"tools": %s,
		"system": [{"type": "text", "text": "System prompt"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "A1"}]},
			{"role": "user", "content": [{"type": "text", "text": "Q2"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "A2"}]},
			{"role": "user", "content": [{"type": "text", "text": "Q3"}]}
		]
	}`, toolsJSON)

	result := applyCacheControlMarkers(input)

	// Count total cache_control breakpoints (should be exactly 4)
	count := 0

	// Tools
	gjson.Get(result, "tools").ForEach(func(_, item gjson.Result) bool {
		if item.Get("cache_control").Exists() {
			count++
		}
		return true
	})

	// System
	gjson.Get(result, "system").ForEach(func(_, item gjson.Result) bool {
		if item.Get("cache_control").Exists() {
			count++
		}
		return true
	})

	// Messages
	gjson.Get(result, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
		return true
	})

	if count != 4 {
		t.Errorf("expected exactly 4 breakpoints, got %d", count)
	}

	// Verify last tool (idx 49) has cache_control
	if gjson.Get(result, "tools.49.cache_control.type").String() != "ephemeral" {
		t.Error("last tool should have cache_control")
	}

	// Verify system has cache_control
	if gjson.Get(result, "system.0.cache_control.type").String() != "ephemeral" {
		t.Error("system should have cache_control")
	}

	// Verify second-to-last user (messages.2) and last user (messages.4)
	if gjson.Get(result, "messages.2.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("second-to-last user should have cache_control")
	}
	if gjson.Get(result, "messages.4.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("last user should have cache_control")
	}
}
