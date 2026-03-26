package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAIToCodex_PreservesBuiltinTools(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search","search_context_size":"high"}],
		"tool_choice":{"type":"web_search"}
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("expected 1 tool, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("expected tools[0].type=web_search, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.search_context_size").String(); got != "high" {
		t.Fatalf("expected tools[0].search_context_size=high, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("expected tool_choice.type=web_search, got %q: %s", got, string(out))
	}
}

func TestOpenAIResponsesToOpenAI_PreservesNonFunctionToolsAndFiles(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"hi"},
				{"type":"input_image","image_url":"https://example.com/image.png"},
				{"type":"input_file","file_id":"file-123","filename":"spec.pdf"}
			]}
		],
		"tools":[
			{"type":"web_search","search_context_size":"low"},
			{"type":"custom","name":"ApplyPatch","description":"patch tool","format":{"type":"grammar","syntax":"lark"}},
			{"type":"function","name":"ReadFile","description":"read file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"strict":false}
		]
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 3 {
		t.Fatalf("expected 3 tools, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("expected tools[0].type=web_search, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.1.type").String(); got != "custom" {
		t.Fatalf("expected tools[1].type=custom, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected tools[1].name=ApplyPatch, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.2.function.strict").Bool(); got != false {
		t.Fatalf("expected function strict=false, got %v: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.image_url.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("expected image URL preserved, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.file.file_id").String(); got != "file-123" {
		t.Fatalf("expected file_id preserved, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.file.filename").String(); got != "spec.pdf" {
		t.Fatalf("expected filename preserved, got %q: %s", got, string(out))
	}
}
