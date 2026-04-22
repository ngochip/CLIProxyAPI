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

	// Check all tools got mcp__<server>__ prefix or are in rename map
	remappedTools := gjson.GetBytes(remapped, "tools")
	remappedTools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("name").String()
		// Must be either in oauthToolRenameMap values OR have double-underscore MCP prefix
		inRenameMap := false
		for _, v := range oauthToolRenameMap {
			if v == name {
				inRenameMap = true
				break
			}
		}
		if !inRenameMap && !strings.HasPrefix(name, "mcp__") {
			t.Errorf("tool %q has neither mcp__ prefix nor is in rename map", name)
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

		// Try reversing via mcp__<server>__ prefix strip
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

	// Tools in rename map should NOT go through mcp__ prefix
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

// TestMcpDoubleUnderscoreFormat verifies that the prefix uses the official
// Claude Code MCP format `mcp__<server>__<tool>` (double underscore). This is
// required by Anthropic's third-party detection since 2026-04-22; the older
// single-underscore `mcp_<name>` is rejected upstream.
func TestMcpDoubleUnderscoreFormat(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"exec", "mcp__proxy__exec"},
		{"agents_list", "mcp__proxy__agents_list"},
		{"lcm_grep", "mcp__proxy__lcm_grep"},
		{"web_search", "mcp__proxy__web_search"},
	}
	for _, c := range cases {
		got := oauthMcpToolPrefix(c.in)
		if got != c.want {
			t.Errorf("oauthMcpToolPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
		// Must contain exactly two "__" segments: prefix separator and server/tool separator.
		if strings.Count(got, "__") != 2 {
			t.Errorf("expected exactly 2 '__' separators in %q", got)
		}
	}
}

// TestReverseMcpPrefixAcceptsAnyServerSlug verifies that reverseMcpPrefix can
// recover the original tool name regardless of which server slug was used
// upstream. This matters because Claude may echo back tool_use with the exact
// server slug we sent, but the reverse logic must not hard-code "proxy".
func TestReverseMcpPrefixAcceptsAnyServerSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"mcp__proxy__exec", "exec"},
		{"mcp__openclaw__lcm_grep", "lcm_grep"},
		{"mcp__server1__agents_list", "agents_list"},
		// Legacy single-underscore form must no longer be accepted as a valid
		// MCP prefix (it was ambiguous and does not match Anthropic's format).
		{"mcp_exec", ""},
		// Missing tool segment after server slug.
		{"mcp__proxy__", ""},
		// Not an MCP prefix at all.
		{"Read", ""},
	}
	for _, c := range cases {
		got := reverseMcpPrefix(c.in)
		if got != c.want {
			t.Errorf("reverseMcpPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRemapDoesNotDoublePrefix verifies that tools already using the double
// underscore MCP format are not re-prefixed.
func TestRemapDoesNotDoublePrefix(t *testing.T) {
	body := map[string]interface{}{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 100,
		"tools": []map[string]interface{}{
			{"name": "mcp__proxy__exec", "description": "d", "input_schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
			{"name": "mcp__other__foo", "description": "d", "input_schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		},
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	}
	bodyBytes, _ := json.Marshal(body)
	remapped, _ := remapOAuthToolNames(bodyBytes)
	names := []string{}
	gjson.GetBytes(remapped, "tools").ForEach(func(_, tool gjson.Result) bool {
		names = append(names, tool.Get("name").String())
		return true
	})
	want := []string{"mcp__proxy__exec", "mcp__other__foo"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("tool[%d]: got %q, want %q (no re-prefix)", i, names[i], w)
		}
	}
}
