package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMcpPrefixRoundTrip(t *testing.T) {
	// Tool names from real OpenClaw request
	originalTools := []string{
		"Read", "Edit", "Write", "exec", "process",
		"canvas", "message", "tts", "agents_list",
		"sessions_list", "sessions_history", "sessions_send",
		"sessions_yield", "sessions_spawn", "subagents",
		"session_status", "web_search", "web_fetch",
		"image", "pdf", "browser", "memory_search",
		"graphiti_search", "lcm_grep", "lcm_expand_query",
	}

	// Build a minimal request body with tools
	tools := make([]map[string]interface{}, len(originalTools))
	for i, name := range originalTools {
		tools[i] = map[string]interface{}{
			"name":         name,
			"description":  "Tool " + name,
			"input_schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		}
	}
	body := map[string]interface{}{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 100,
		"tools":      tools,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hi"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	// Forward: remapOAuthToolNames
	remapped, renamed := remapOAuthToolNames(bodyBytes)
	if !renamed {
		t.Fatal("expected renamed=true")
	}

	// Check all tools got mcp_ prefix or are in rename map
	remappedTools := gjson.GetBytes(remapped, "tools")
	remappedTools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("name").String()
		// Must be either in oauthToolRenameMap values OR have mcp_ prefix
		inRenameMap := false
		for _, v := range oauthToolRenameMap {
			if v == name {
				inRenameMap = true
				break
			}
		}
		if !inRenameMap && !strings.HasPrefix(name, "mcp_") {
			t.Errorf("tool %q has neither mcp_ prefix nor is in rename map", name)
		}
		return true
	})

	// Simulate Claude response: tool_use with each remapped name
	remappedTools.ForEach(func(_, tool gjson.Result) bool {
		mcpName := tool.Get("name").String()

		// Try reversing via static map first
		if origName, ok := oauthToolRenameReverseMap[mcpName]; ok {
			// Static reverse worked
			_ = origName
			return true
		}

		// Try reversing via mcp_ prefix strip
		stripped := reverseMcpPrefix(mcpName)
		if stripped == "" {
			// This is a known Claude Code tool name (e.g. Read, Write, Edit)
			// that went through rename map and stays as-is
			return true
		}

		// Verify round-trip: stripped name must be in original tools
		found := false
		for _, orig := range originalTools {
			if orig == stripped {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("round-trip failed: %q -> %q -> %q not in original tools", mcpName, stripped, stripped)
		}
		return true
	})

	// Specific round-trip checks
	testCases := []struct {
		original string
		want     string
	}{
		{"exec", "exec"},
		{"process", "process"},
		{"agents_list", "agents_list"},
		{"lcm_grep", "lcm_grep"},
		{"lcm_expand_query", "lcm_expand_query"},
		{"web_search", "web_search"},
		{"browser", "browser"},
		{"graphiti_search", "graphiti_search"},
		{"sessions_spawn", "sessions_spawn"},
		{"canvas", "canvas"},
		{"tts", "tts"},
		{"image", "image"},
		{"pdf", "pdf"},
	}

	for _, tc := range testCases {
		mcpName := oauthMcpToolPrefix(tc.original)
		reversed := reverseMcpPrefix(mcpName)
		if reversed != tc.want {
			t.Errorf("round-trip %q: prefix=%q reverse=%q want=%q", tc.original, mcpName, reversed, tc.want)
		}
	}

	// Tools in rename map should NOT go through mcp_ prefix
	for orig, renamed := range oauthToolRenameMap {
		reversed, ok := oauthToolRenameReverseMap[renamed]
		if !ok {
			t.Errorf("rename map tool %q->%q has no reverse entry", orig, renamed)
			continue
		}
		if reversed != orig {
			t.Errorf("rename map round-trip: %q->%q->%q want %q", orig, renamed, reversed, orig)
		}
	}
}
