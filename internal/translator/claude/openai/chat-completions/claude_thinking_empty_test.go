package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestClaudeThinkingEmptyBlockSkipped verifies that empty thinking blocks
// (content_block_start followed immediately by content_block_stop, no deltas)
// emit NO delimiters at all, so Cursor does not render a ghost "Thought 1s"
// bubble with no content.
func TestClaudeThinkingEmptyBlockSkipped(t *testing.T) {
	ctx := context.Background()
	var param any

	events := []string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Quick answer"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}

	var aggregated strings.Builder
	for _, evt := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-7", nil, nil, []byte(evt), &param)
		for _, chunk := range chunks {
			content := gjson.GetBytes(chunk, "choices.0.delta.content").String()
			aggregated.WriteString(content)
		}
	}

	got := aggregated.String()
	t.Logf("Aggregated content: %q", got)

	if strings.Contains(got, "<!--thinking-start:") {
		t.Errorf("empty thinking block should NOT emit thinking-start marker, got: %q", got)
	}
	if strings.Contains(got, "<!--thinking-end:") {
		t.Errorf("empty thinking block should NOT emit thinking-end marker, got: %q", got)
	}
	if strings.Contains(got, "<!--thinkId:") {
		t.Errorf("empty thinking block should NOT emit thinkId marker, got: %q", got)
	}
	if !strings.Contains(got, "Quick answer") {
		t.Errorf("normal text delta should still be emitted, got: %q", got)
	}
}

// TestClaudeThinkingNormalBlockEmitsMarkers verifies that thinking blocks with
// actual content still emit both opening and closing delimiters correctly.
func TestClaudeThinkingNormalBlockEmitsMarkers(t *testing.T) {
	ctx := context.Background()
	var param any

	events := []string{
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude-opus-4-7"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me analyze."}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sigXYZ"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer."}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}

	var aggregated strings.Builder
	for _, evt := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-7", nil, nil, []byte(evt), &param)
		for _, chunk := range chunks {
			content := gjson.GetBytes(chunk, "choices.0.delta.content").String()
			aggregated.WriteString(content)
		}
	}

	got := aggregated.String()
	t.Logf("Aggregated content: %q", got)

	if !strings.Contains(got, "<!--thinking-start:") {
		t.Errorf("non-empty thinking block should emit thinking-start, got: %q", got)
	}
	if !strings.Contains(got, "<!--thinking-end:") {
		t.Errorf("non-empty thinking block should emit thinking-end, got: %q", got)
	}
	if !strings.Contains(got, "<!--thinkId:") {
		t.Errorf("non-empty thinking block should emit thinkId, got: %q", got)
	}
	if !strings.Contains(got, "Let me analyze.") {
		t.Errorf("thinking content should be emitted, got: %q", got)
	}
	if !strings.Contains(got, "Answer.") {
		t.Errorf("text content should be emitted, got: %q", got)
	}

	// Verify thinking content is BETWEEN start and end markers.
	startIdx := strings.Index(got, "<!--thinking-start:")
	endIdx := strings.Index(got, "<!--thinking-end:")
	thoughtIdx := strings.Index(got, "Let me analyze.")
	if !(startIdx < thoughtIdx && thoughtIdx < endIdx) {
		t.Errorf("thinking content must be between start (%d) and end (%d) markers, but content at %d",
			startIdx, endIdx, thoughtIdx)
	}

	// Verify text content comes AFTER end marker.
	answerIdx := strings.Index(got, "Answer.")
	if answerIdx < endIdx {
		t.Errorf("text content (at %d) must come after thinking-end marker (at %d)", answerIdx, endIdx)
	}
}

// TestClaudeThinkingInterleavedBlocks verifies multiple thinking blocks
// (interleaved-thinking beta) each emit their own nonce-based delimiters.
func TestClaudeThinkingInterleavedBlocks(t *testing.T) {
	ctx := context.Background()
	var param any

	events := []string{
		`data: {"type":"message_start","message":{"id":"msg_3","model":"claude-opus-4-7"}}`,
		// First thinking block with content
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		// Text block
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"mid text"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		// Empty thinking block (should be skipped)
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_stop","index":2}`,
		// Another thinking block with content
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":3,"delta":{"type":"thinking_delta","thinking":"second thought"}}`,
		`data: {"type":"content_block_stop","index":3}`,
		// Final text
		`data: {"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"final"}}`,
		`data: {"type":"content_block_stop","index":4}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}

	var aggregated strings.Builder
	for _, evt := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-7", nil, nil, []byte(evt), &param)
		for _, chunk := range chunks {
			content := gjson.GetBytes(chunk, "choices.0.delta.content").String()
			aggregated.WriteString(content)
		}
	}

	got := aggregated.String()
	t.Logf("Aggregated content: %q", got)

	startCount := strings.Count(got, "<!--thinking-start:")
	endCount := strings.Count(got, "<!--thinking-end:")
	if startCount != 2 {
		t.Errorf("expected 2 thinking-start markers (empty block skipped), got %d in: %q", startCount, got)
	}
	if endCount != 2 {
		t.Errorf("expected 2 thinking-end markers (empty block skipped), got %d in: %q", endCount, got)
	}
	if !strings.Contains(got, "first thought") {
		t.Errorf("missing first thought")
	}
	if !strings.Contains(got, "second thought") {
		t.Errorf("missing second thought")
	}
}
