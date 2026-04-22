package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestReverseRemapResponse(t *testing.T) {
	// Simulate Claude response with tool_use blocks using mcp__<server>__<tool>
	// format names (the format Anthropic mandates since 2026-04-22).
	response := map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": "Let me check that.",
			},
			{
				"type":  "tool_use",
				"id":    "toolu_123",
				"name":  "mcp__proxy__exec",
				"input": map[string]interface{}{"command": "ls"},
			},
		},
	}
	bodyBytes, _ := json.Marshal(response)

	// Reverse
	reversed := reverseRemapOAuthToolNames(bodyBytes)
	name := gjson.GetBytes(reversed, "content.1.name").String()
	if name != "exec" {
		t.Errorf("expected 'exec', got %q", name)
	}

	// Test with various tool names (double-underscore format).
	testCases := []struct {
		mcpName  string
		wantName string
	}{
		{"mcp__proxy__exec", "exec"},
		{"mcp__proxy__process", "process"},
		{"mcp__proxy__agents_list", "agents_list"},
		{"mcp__proxy__lcm_grep", "lcm_grep"},
		{"mcp__proxy__lcm_expand_query", "lcm_expand_query"},
		{"mcp__proxy__web_search", "web_search"},
		{"mcp__proxy__browser", "browser"},
		{"mcp__proxy__graphiti_search", "graphiti_search"},
		{"mcp__proxy__sessions_spawn", "sessions_spawn"},
		{"mcp__proxy__canvas", "canvas"},
		{"mcp__proxy__tts", "tts"},
		{"mcp__proxy__Read", "Read"},
		{"mcp__proxy__Write", "Write"},
		// Different server slugs must also reverse correctly (robustness).
		{"mcp__openclaw__exec", "exec"},
		{"mcp__server1__agents_list", "agents_list"},
	}

	for _, tc := range testCases {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "tool_use", "id": "toolu_1", "name": tc.mcpName, "input": map[string]interface{}{}},
			},
		}
		b, _ := json.Marshal(resp)
		result := reverseRemapOAuthToolNames(b)
		got := gjson.GetBytes(result, "content.0.name").String()
		if got != tc.wantName {
			t.Errorf("reverse %q: got %q, want %q", tc.mcpName, got, tc.wantName)
		}
	}

	// Test static rename map tools (bash -> Bash) also reverse correctly
	staticResp := map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "tool_use", "id": "toolu_2", "name": "Bash", "input": map[string]interface{}{"command": "echo hi"}},
		},
	}
	b, _ := json.Marshal(staticResp)
	result := reverseRemapOAuthToolNames(b)
	got := gjson.GetBytes(result, "content.0.name").String()
	if got != "bash" {
		t.Errorf("static reverse 'Bash': got %q, want 'bash'", got)
	}
}
