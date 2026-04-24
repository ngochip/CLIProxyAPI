package chat_completions

import (
	"bytes"
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
	// Verify raw bytes contain UTF-8 checkmark, not \u2705 escape
	if bytes.Contains(last, []byte(`\u2705`)) {
		t.Errorf("output should contain raw UTF-8, not \\u2705 escape: %s", last)
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
	// Verify raw bytes contain UTF-8 package emoji, not surrogate pair escapes
	if bytes.Contains(last, []byte(`\ud83d`)) || bytes.Contains(last, []byte(`\udce6`)) {
		t.Errorf("output should contain raw UTF-8, not surrogate pair escapes: %s", last)
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

func TestStreamUnicode_EscapeNormalizedToUTF8(t *testing.T) {
	// Upstream sends \uXXXX escapes; output must contain raw UTF-8, not escapes.
	// This verifies the normalization in buildDeltaChunk.
	lines := []string{
		messageStartLine,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\u0110ang x\u1eed l\u00fd"}}`,
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
	expected := "\u0110ang x\u1eed l\u00fd" // "Đang xử lý"
	if content != expected {
		t.Errorf("expected content %q, got %q", expected, content)
	}
	// Raw bytes must contain UTF-8 encoded Vietnamese, not \uXXXX escapes
	if bytes.Contains(last, []byte(`\u0110`)) || bytes.Contains(last, []byte(`\u1eed`)) {
		t.Errorf("output should contain raw UTF-8, not \\uXXXX escapes: %s", last)
	}
	// Verify the raw UTF-8 bytes are present
	if !bytes.Contains(last, []byte("Đang")) {
		t.Errorf("output should contain raw UTF-8 'Đang': %s", last)
	}
}

// === Tests cho normalizeUnicodeEscapes ===

func TestNormalizeUnicodeEscapes_SurrogatePair(t *testing.T) {
	// ud83dudd0d = 🔍 (MAGNIFYING GLASS)
	input := `ud83dudd0d hello`
	got := normalizeUnicodeEscapes(input)
	if got != "🔍 hello" {
		t.Errorf("surrogate pair: expected %q, got %q", "🔍 hello", got)
	}
}

func TestNormalizeUnicodeEscapes_PackageEmoji(t *testing.T) {
	// ud83dudce6 = 📦 (PACKAGE)
	input := `ud83dudce6 Shipped`
	got := normalizeUnicodeEscapes(input)
	if got != "📦 Shipped" {
		t.Errorf("package emoji: expected %q, got %q", "📦 Shipped", got)
	}
}

func TestNormalizeUnicodeEscapes_Vietnamese(t *testing.T) {
	// u1ec7 = ệ, u00f3 = ó, u0110 = Đ, u01b0 = ư, u1ea3 = ả
	input := `hiu1ec7n cu00f3 u0110ang chu01b0a bu1ea3n`
	got := normalizeUnicodeEscapes(input)
	expected := "hiện có Đang chưa bản"
	if got != expected {
		t.Errorf("vietnamese: expected %q, got %q", expected, got)
	}
}

func TestNormalizeUnicodeEscapes_Mixed(t *testing.T) {
	// Trường hợp thực tế từ prod log
	input := `ud83dudd0d ` + "`warp-nodejs-biz-route`" + ` hiu1ec7n v0.3.163, cu00f3 1 commit mu1edbi chu01b0a publish. u0110ang build vu00e0 publish phiu00ean bu1ea3n mu1edbi...`
	got := normalizeUnicodeEscapes(input)
	if !bytes.Contains([]byte(got), []byte("🔍")) {
		t.Errorf("mixed: should contain 🔍, got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("hiện")) {
		t.Errorf("mixed: should contain 'hiện', got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("Đang")) {
		t.Errorf("mixed: should contain 'Đang', got %q", got)
	}
	if bytes.Contains([]byte(got), []byte("u1ec7")) {
		t.Errorf("mixed: should NOT contain literal 'u1ec7', got %q", got)
	}
}

func TestNormalizeUnicodeEscapes_NoChange(t *testing.T) {
	// Không match pattern nào
	input := `hello world 123`
	got := normalizeUnicodeEscapes(input)
	if got != input {
		t.Errorf("no-change: expected %q, got %q", input, got)
	}
}

func TestNormalizeUnicodeEscapes_URL(t *testing.T) {
	// URL chứa "u" + hex — không nên bị convert nếu < 0x80
	input := `https://example.com/u0041path`
	got := normalizeUnicodeEscapes(input)
	// u0041 = 'A' nhưng < 0x80 nên giữ nguyên
	if got != input {
		t.Errorf("url: expected %q (unchanged), got %q", input, got)
	}
}

func TestNormalizeUnicodeEscapes_ToolCallJSON(t *testing.T) {
	// Simulate full tool call arguments JSON
	input := `{"action": "send", "channel": "slack", "message": "ud83dudce6 Bu1eaft u0111u1ea7u deploy"}`
	got := normalizeUnicodeEscapes(input)
	if !bytes.Contains([]byte(got), []byte("📦")) {
		t.Errorf("tool-call: should contain 📦, got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("Bắt")) {
		t.Errorf("tool-call: should contain 'Bắt', got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("đầu")) {
		t.Errorf("tool-call: should contain 'đầu', got %q", got)
	}
	// Phải vẫn là valid JSON structure
	if !gjson.Valid(got) {
		t.Errorf("tool-call: result should be valid JSON, got %q", got)
	}
}
