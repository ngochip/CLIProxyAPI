package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// feedEvents sends a sequence of SSE lines through ConvertClaudeResponseToOpenAI
// and returns all emitted chunks.
func feedEvents(t *testing.T, lines []string) [][]byte {
	t.Helper()
	ctx := context.Background()
	var param any
	var chunks [][]byte
	for _, line := range lines {
		out := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, []byte(line), &param)
		chunks = append(chunks, out...)
	}
	return chunks
}

// messageStartLine is a reusable message_start SSE event that initializes deltaPrefix.
const messageStartLine = `data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`

func TestStreamUnicode_EmojiBMP(t *testing.T) {
	lines := []string{
		messageStartLine,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\u2705 Done"}}`,
	}
	chunks := feedEvents(t, lines)
	// message_start + text_delta = 2 chunks
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if !gjson.ValidBytes(last) {
		t.Fatalf("output chunk is not valid JSON: %s", last)
	}
	content := gjson.GetBytes(last, "choices.0.delta.content").String()
	if content != "\u2705 Done" {
		t.Errorf("expected content %q, got %q", "\u2705 Done", content)
	}
}

func TestStreamUnicode_EmojiSupplementary(t *testing.T) {
	lines := []string{
		messageStartLine,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\ud83d\udce6 Package"}}`,
	}
	chunks := feedEvents(t, lines)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if !gjson.ValidBytes(last) {
		t.Fatalf("output chunk is not valid JSON: %s", last)
	}
	content := gjson.GetBytes(last, "choices.0.delta.content").String()
	expected := "\U0001F4E6 Package"
	if content != expected {
		t.Errorf("expected content %q, got %q", expected, content)
	}
}

func TestStreamUnicode_Vietnamese(t *testing.T) {
	lines := []string{
		messageStartLine,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Đang xử lý → hoàn tất"}}`,
	}
	chunks := feedEvents(t, lines)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if !gjson.ValidBytes(last) {
		t.Fatalf("output chunk is not valid JSON: %s", last)
	}
	content := gjson.GetBytes(last, "choices.0.delta.content").String()
	expected := "Đang xử lý → hoàn tất"
	if content != expected {
		t.Errorf("expected content %q, got %q", expected, content)
	}
}

func TestStreamUnicode_MissMessageStart(t *testing.T) {
	// Simulate missed message_start: deltaPrefix will be empty.
	// The fallback builder must still emit valid OpenAI chunks.
	lines := []string{
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
	}
	chunks := feedEvents(t, lines)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks from fallback path, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if !gjson.ValidBytes(chunk) {
			t.Fatalf("chunk[%d] is not valid JSON: %s", i, chunk)
		}
		c := gjson.GetBytes(chunk, "choices.0.delta.content").String()
		if i == 0 && c != "hello" {
			t.Errorf("chunk[0] content: expected %q, got %q", "hello", c)
		}
		if i == 1 && c != " world" {
			t.Errorf("chunk[1] content: expected %q, got %q", " world", c)
		}
	}
}

func TestStreamUnicode_CRLFTrailing(t *testing.T) {
	// Simulate a line that had \r\n (after executor trims \r, only the JSON remains).
	// Verify that TrimSpace in the translator handles any residual whitespace.
	lines := []string{
		messageStartLine,
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}  ",
	}
	chunks := feedEvents(t, lines)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if !gjson.ValidBytes(last) {
		t.Fatalf("output chunk is not valid JSON: %s", last)
	}
	content := gjson.GetBytes(last, "choices.0.delta.content").String()
	if content != "ok" {
		t.Errorf("expected content %q, got %q", "ok", content)
	}
}

func TestStreamUnicode_HeartbeatComment(t *testing.T) {
	// SSE comment lines (starting with :) should produce no output chunks.
	lines := []string{
		messageStartLine,
		":heartbeat",
	}
	chunks := feedEvents(t, lines)
	// Only message_start should produce a chunk, heartbeat should not.
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (message_start only), got %d", len(chunks))
	}
}
