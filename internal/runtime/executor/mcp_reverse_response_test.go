package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestReverseRemapResponse(t *testing.T) {
	// Simulate Claude response with tool_use blocks using mcp_ prefixed names
	response := map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": "Let me check that.",
			},
			{
				"type":  "tool_use",
				"id":    "toolu_123",
				"name":  "mcp_exec",
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

	// Test with various tool names
	testCases := []struct {
		mcpName  string
		wantName string
	}{
		{"mcp_exec", "exec"},
		{"mcp_process", "process"},
		{"mcp_agents_list", "agents_list"},
		{"mcp_lcm_grep", "lcm_grep"},
		{"mcp_lcm_expand_query", "lcm_expand_query"},
		{"mcp_web_search", "web_search"},
		{"mcp_browser", "browser"},
		{"mcp_graphiti_search", "graphiti_search"},
		{"mcp_sessions_spawn", "sessions_spawn"},
		{"mcp_canvas", "canvas"},
		{"mcp_tts", "tts"},
		{"mcp_Read", "Read"},
		{"mcp_Write", "Write"},
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
