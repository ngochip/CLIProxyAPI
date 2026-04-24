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

	gotModel := gjson.GetBytes(out[0], "model").String()
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

	gotModel := gjson.GetBytes(out[0], "model").String()
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
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("expected tool call id call_1, got %q: %s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected tool name ApplyPatch, got %q: %s", got, out[0])
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.custom_tool_call_input.delta","delta":"*** Begin Patch\n","item_id":"ctc_1","output_index":1}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk for custom tool call delta, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "*** Begin Patch\n" {
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
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q: %s", got, out[0])
	}
}


func TestConvertCodexResponseToOpenAI_SuppressesTextAfterToolCall(t *testing.T) {
	ctx := context.Background()
	var param any
	modelName := "gpt-5.4"
	originalReq := []byte(`{"tools":[{"type":"function","function":{"name":"Subagent","parameters":{"type":"object"}}}]}`)

	out := ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_sub_1","arguments":"","name":"Subagent"},"output_index":0}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected tool call chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, originalReq, nil, []byte(`data: {"type":"response.output_text.delta","delta":"toi se tiep tuc noi du"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected text delta after tool call to be suppressed, got %d: %s", len(out), out[0])
	}
}

func TestConvertCodexResponseToOpenAINonStream_CustomToolCallFinishReason(t *testing.T) {
	raw := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_1",
			"created_at":1700000000,
			"model":"gpt-5.4",
			"status":"completed",
			"output":[
				{"type":"custom_tool_call","call_id":"call_patch_1","name":"ApplyPatch","input":"*** Begin Patch\n*** End Patch\n"}
			]
		}
	}`)
	originalReq := []byte(`{"tools":[{"type":"custom","name":"ApplyPatch"}]}`)
	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "", originalReq, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected custom tool name preserved, got %q: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String(); got != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("expected custom tool input mapped to arguments, got %q: %s", got, out)
	}
}

func TestConvertCodexResponseToOpenAI_StreamPartialImageEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`)

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 0 {
		t.Fatalf("expected duplicate image chunk to be suppressed, got %d", len(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamImageGenerationCallDoneEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"png","result":"aGVsbG8="}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected output_item.done to be suppressed when identical to last partial image, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"jpeg","result":"Ymll"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/jpeg;base64,Ymll" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/jpeg;base64,Ymll", gotURL, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamImageGenerationCallAddsMessageImages(t *testing.T) {
	ctx := context.Background()

	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"image_generation_call","output_format":"png","result":"aGVsbG8="}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)

	gotURL := gjson.GetBytes(out, "choices.0.message.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out))
	}
}
