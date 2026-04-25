package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyCacheControlMarkers_AllSections(t *testing.T) {
	input := `{
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
	}`

	result := applyCacheControlMarkers(input)

	// BP1: first system block cached
	if gjson.Get(result, "system.0.cache_control.type").String() != "ephemeral" {
		t.Error("first system block should have cache_control")
	}

	// BP2: second system block cached
	if gjson.Get(result, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("second system block should have cache_control")
	}

	// BP3: second-to-last message (messages.3 = assistant)
	if gjson.Get(result, "messages.3.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("second-to-last message (assistant) should have cache_control")
	}

	// BP4: last message (messages.4 = user)
	if gjson.Get(result, "messages.4.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("last message (user) should have cache_control")
	}

	// Tools should NOT have cache_control
	if gjson.Get(result, "tools.0.cache_control").Exists() || gjson.Get(result, "tools.1.cache_control").Exists() {
		t.Error("tools should NOT have cache_control (auto-cached via top-level)")
	}

	// Earlier messages should NOT have cache_control
	if gjson.Get(result, "messages.0.content.0.cache_control").Exists() {
		t.Error("earlier messages should NOT have cache_control")
	}
}

func TestApplyCacheControlMarkers_AssistantMessageCached(t *testing.T) {
	input := `{
		"system": [{"type": "text", "text": "System prompt"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "Let me think..."},
				{"type": "text", "text": "Here is my response"}
			]},
			{"role": "user", "content": [{"type": "text", "text": "Follow up"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "More thinking..."},
				{"type": "text", "text": "Final answer"}
			]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// Last 2 messages: messages.2 (user) and messages.3 (assistant)
	if gjson.Get(result, "messages.2.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("second-to-last message (user) should have cache_control")
	}

	// Assistant's thinking block should NOT have cache_control; text block should
	if gjson.Get(result, "messages.3.content.0.cache_control").Exists() {
		t.Error("thinking block in assistant message should NOT have cache_control")
	}
	if gjson.Get(result, "messages.3.content.1.cache_control.type").String() != "ephemeral" {
		t.Error("text block in assistant message should have cache_control")
	}
}

func TestApplyCacheControlMarkers_TwoSystemFirstCached(t *testing.T) {
	input := `{
		"system": [
			{"type": "text", "text": "Main system prompt"},
			{"type": "text", "text": "Extra instructions"},
			{"type": "text", "text": "Third block - should NOT be cached"}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hi"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	if gjson.Get(result, "system.0.cache_control.type").String() != "ephemeral" {
		t.Error("first system block should have cache_control")
	}
	if gjson.Get(result, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("second system block should have cache_control")
	}
	if gjson.Get(result, "system.2.cache_control").Exists() {
		t.Error("third system block should NOT have cache_control")
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

	// Walk all possible cache_control locations and verify no 1h TTL
	paths := []string{
		"tools.0.cache_control.ttl",
		"system.0.cache_control.ttl",
		"messages.0.content.0.cache_control.ttl",
		"messages.1.content.0.cache_control.ttl",
		"messages.2.content.0.cache_control.ttl",
	}
	for _, path := range paths {
		val := gjson.Get(result, path)
		if val.Exists() && val.String() == "1h" {
			t.Errorf("path %q should NOT have ttl=1h", path)
		}
	}
}

func TestApplyCacheControlMarkers_SkipThinkingInSystem(t *testing.T) {
	input := `{
		"system": [
			{"type": "thinking", "thinking": "some thinking"},
			{"type": "text", "text": "Real system prompt"},
			{"type": "text", "text": "More instructions"}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// thinking block (idx 0) should be skipped
	if gjson.Get(result, "system.0.cache_control").Exists() {
		t.Error("thinking system block should NOT have cache_control")
	}
	// First 2 valid blocks: system.1 and system.2
	if gjson.Get(result, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("first valid system block should have cache_control")
	}
	if gjson.Get(result, "system.2.cache_control.type").String() != "ephemeral" {
		t.Error("second valid system block should have cache_control")
	}
}

func TestApplyCacheControlMarkers_SingleMessage(t *testing.T) {
	input := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Only message"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	if gjson.Get(result, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
		t.Error("single message should have cache_control")
	}
}

func TestApplyCacheControlMarkers_StringSystem(t *testing.T) {
	input := `{
		"system": "A string system prompt",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	// String system should be converted to array with cache_control
	if gjson.Get(result, "system.0.cache_control.type").String() != "ephemeral" {
		t.Error("string system prompt should be converted and cached")
	}
	if gjson.Get(result, "system.0.text").String() != "A string system prompt" {
		t.Error("system text should be preserved")
	}
}

func TestApplyCacheControlMarkers_MaxBreakpoints(t *testing.T) {
	input := `{
		"system": [
			{"type": "text", "text": "System 1"},
			{"type": "text", "text": "System 2"}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "A1"}]},
			{"role": "user", "content": [{"type": "text", "text": "Q2"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "A2"}]},
			{"role": "user", "content": [{"type": "text", "text": "Q3"}]}
		]
	}`

	result := applyCacheControlMarkers(input)

	count := 0

	gjson.Get(result, "system").ForEach(func(_, item gjson.Result) bool {
		if item.Get("cache_control").Exists() {
			count++
		}
		return true
	})

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
}
