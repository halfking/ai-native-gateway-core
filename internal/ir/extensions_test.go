package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseOpenAI_Extensions(t *testing.T) {
	ir, _ := ParseOpenAI([]byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}],"reasoning_effort":"high"}`))
	if len(ir.Extensions) == 0 {
		t.Error("Extensions empty")
	}
}
func TestSerializeOpenAI_Extensions(t *testing.T) {
	ir := &InternalRequest{Model: "test", Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}, Extensions: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)}}
	body, _ := SerializeOpenAI(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	if out["reasoning_effort"] != "high" {
		t.Error("Extensions not restored")
	}
}

func TestAnthropicExtensionsRoundTrip(t *testing.T) {
	ir, err := ParseAnthropic([]byte(`{"model":"claude","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ir.Extensions["context_management"]; !ok {
		t.Fatal("context_management was not preserved")
	}
	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["context_management"]; !ok {
		t.Fatal("context_management was not restored")
	}
}

func TestUnknownContentBlockRoundTrip(t *testing.T) {
	openAI, err := ParseOpenAI([]byte(`{"model":"test","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"abc","format":"wav"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := SerializeOpenAI(openAI)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(body) || !containsJSON(body, `"input_audio"`) {
		t.Fatalf("OpenAI unknown content block was not restored: %s", body)
	}

	anthropic, err := ParseAnthropic([]byte(`{"model":"claude","max_tokens":32,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"abc"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err = SerializeAnthropic(anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(body) || !containsJSON(body, `"document"`) || !containsJSON(body, `"application/pdf"`) {
		t.Fatalf("Anthropic document block was not restored: %s", body)
	}
}

func TestOpenAIParallelToolCallsRoundTrip(t *testing.T) {
	ir, err := ParseOpenAI([]byte(`{"model":"test","parallel_tool_calls":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ir.ParallelToolCalls == nil || *ir.ParallelToolCalls {
		t.Fatal("parallel_tool_calls was not parsed")
	}
	body, err := SerializeOpenAI(ir)
	if err != nil || !containsJSON(body, `"parallel_tool_calls":false`) {
		t.Fatalf("parallel_tool_calls was not serialized: %s (%v)", body, err)
	}
}

func containsJSON(body []byte, fragment string) bool {
	return strings.Contains(string(body), fragment)
}
func TestImageDetail(t *testing.T) {
	ir, _ := ParseOpenAI([]byte(`{"model":"gpt-4-vision","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://a.com/i.jpg","detail":"high"}}]}]}`))
	if ir.Messages[0].Content[0].Image.Detail != "high" {
		t.Error("detail not parsed")
	}
	body, _ := SerializeOpenAI(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	content := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
	imageURL := content[0].(map[string]any)["image_url"].(map[string]any)
	if imageURL["detail"] != "high" {
		t.Error("detail not serialized")
	}
}
