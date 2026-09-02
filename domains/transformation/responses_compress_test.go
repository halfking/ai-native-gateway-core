package transformation

import (
	"encoding/json"
	"testing"
)

func TestCompressResponsesInputIfNeededTrimsOldestAndPreservesTopLevelFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5","instructions":"keep this","input":[{"type":"message","role":"user","content":"old one xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"message","role":"assistant","content":"old two xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"message","role":"user","content":"latest"}],"tools":[{"type":"function","name":"f"}],"metadata":{"trace":"keep"}}`)

	out := CompressResponsesInputIfNeeded(body, 60, 0)
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatalf("test body is invalid JSON: %v", err)
	}
	for _, field := range []string{"model", "instructions", "tools", "metadata"} {
		if string(got[field]) != string(want[field]) {
			t.Errorf("top-level field %q changed: got %s, want %s", field, got[field], want[field])
		}
	}
	var items []map[string]any
	if err := json.Unmarshal(got["input"], &items); err != nil {
		t.Fatalf("input is not an array: %v", err)
	}
	if len(items) != 1 || items[0]["content"] != "latest" {
		t.Fatalf("input = %#v, want only latest item", items)
	}
}

func TestCompressResponsesInputIfNeededKeepsFunctionCallPairAtomic(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"old xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"function_call","call_id":"call_old","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_old","output":"result xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"message","role":"user","content":"latest"}]}`)

	out := CompressResponsesInputIfNeeded(body, 60, 0)
	var request struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &request); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if len(request.Input) != 1 || request.Input[0].Type != "message" {
		t.Fatalf("input = %#v, want only latest message after dropping old pair", request.Input)
	}

	// Force the latest item to be the output: its matching call must survive.
	body = []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"old xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"function_call","call_id":"call_latest","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_latest","output":"latest"}]}`)
	out = CompressResponsesInputIfNeeded(body, 30, 0)
	if err := json.Unmarshal(out, &request); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if len(request.Input) != 2 || request.Input[0].Type != "function_call" || request.Input[1].Type != "function_call_output" {
		t.Fatalf("input = %#v, want atomic latest function pair", request.Input)
	}
}

func TestCompressResponsesInputIfNeededFailOpen(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "string input", body: []byte(`{"model":"gpt-5","input":"hello"}`)},
		{name: "unknown item", body: []byte(`{"model":"gpt-5","input":[{"type":"custom_item","payload":"opaque"},{"type":"message","role":"user","content":"latest"}]}`)},
		{name: "malformed item", body: []byte(`{"model":"gpt-5","input":[{"type":"function_call","call_id":7},{"type":"message","role":"user","content":"latest"}]}`)},
		{name: "invalid JSON", body: []byte(`{"model":`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompressResponsesInputIfNeeded(tt.body, 1, 0); string(got) != string(tt.body) {
				t.Fatalf("fail-open output = %s, want original %s", got, tt.body)
			}
		})
	}
}

func TestCompressResponsesInputIfNeededPreservesMultimodalItems(t *testing.T) {
	body := []byte(`{"model":"gpt-5","instructions":"keep","input":[{"type":"message","role":"user","content":"old xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"input_image","image_url":"data:image/png;base64,opaque"},{"type":"input_audio","audio":{"id":"aud_1"}},{"type":"message","role":"user","content":"latest"}],"metadata":{"keep":true}}`)
	out := CompressResponsesInputIfNeeded(body, 40, 0)
	var got struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if len(got.Input) == 0 || got.Input[len(got.Input)-1]["content"] != "latest" {
		t.Fatalf("input = %#v, want latest item retained", got.Input)
	}
	for _, item := range got.Input {
		if typ, _ := item["type"].(string); typ == "input_image" || typ == "input_audio" {
			if item["image_url"] == nil && item["audio"] == nil {
				t.Fatalf("multimodal item payload lost: %#v", item)
			}
		}
	}
}

func TestCompressResponsesInputIfNeededRespectsOutputReserveAndLatest(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"old xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"type":"message","role":"user","content":"latest"}]}`)
	if got := CompressResponsesInputIfNeeded(body, 100000, 100001); string(got) != string(body) {
		t.Fatal("non-positive input budget should fail open")
	}
	out := CompressResponsesInputIfNeeded(body, 20, 0)
	var request struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &request); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if len(request.Input) != 1 || request.Input[0]["content"] != "latest" {
		t.Fatalf("input = %#v, want latest item retained", request.Input)
	}
}
