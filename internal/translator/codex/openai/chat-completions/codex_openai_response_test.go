package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI_StreamSetsModelFromResponseCreated(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.3-codex"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.Get(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_FirstChunkUsesRequestModelName(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.Get(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}


func TestConvertCodexResponseToOpenAI_CustomToolCallStreaming(t *testing.T) {
	ctx := context.Background()
	var param any
	modelName := "gpt-5.4"
	originalReq := []byte(`{"tools":[{"type":"custom","name":"ApplyPatch","description":"patch tool"}]}`)

	out := ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.output_item.added","item":{"id":"ctc_1","type":"custom_tool_call","status":"in_progress","call_id":"call_1","input":"","name":"ApplyPatch"},"output_index":1}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk for custom tool call add, got %d", len(out))
	}
	if got := gjson.Get(out[0], "choices.0.delta.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("expected tool call id call_1, got %q: %s", got, out[0])
	}
	if got := gjson.Get(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected tool name ApplyPatch, got %q: %s", got, out[0])
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.custom_tool_call_input.delta","delta":"*** Begin Patch\n","item_id":"ctc_1","output_index":1}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk for custom tool call delta, got %d", len(out))
	}
	if got := gjson.Get(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "*** Begin Patch\n" {
		t.Fatalf("expected custom tool delta in arguments, got %q: %s", got, out[0])
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ctc_1","type":"custom_tool_call","status":"completed","call_id":"call_1","input":"*** Begin Patch\n*** End Patch\n","name":"ApplyPatch"},"output_index":1}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no extra chunk for announced custom tool done, got %d: %v", len(out), out)
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk for response.completed, got %d", len(out))
	}
	if got := gjson.Get(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q: %s", got, out[0])
	}
}
