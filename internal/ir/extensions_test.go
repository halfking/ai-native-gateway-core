package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseOpenAI_Extensions(t *testing.T) {
	// audit-provider-multimodal (2026-07-13): reasoning_effort was promoted from
	// Extensions to structured IR field. This test now verifies that an
	// unrelated unknown field still flows through Extensions.
	ir, _ := ParseOpenAI([]byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}],"custom_vendor_param":true}`))
	if len(ir.Extensions) == 0 {
		t.Error("Extensions empty for unknown fields")
	}
	if _, ok := ir.Extensions["custom_vendor_param"]; !ok {
		t.Error("Unknown field not captured in Extensions")
	}
}
func TestSerializeOpenAI_Extensions(t *testing.T) {
	// audit-provider-multimodal (2026-07-13): reasoning_effort now flows through
	// the structured ReasoningConfig field. This test verifies that an unrelated
	// Extensions entry is still serialized back to the body.
	ir := &InternalRequest{
		Model:      "test",
		Messages:   []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Reasoning:  &ReasoningConfig{Effort: "high"},
		Extensions: map[string]json.RawMessage{"custom_vendor_param": json.RawMessage(`true`)},
	}
	body, _ := SerializeOpenAI(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	if out["reasoning_effort"] != "high" {
		t.Error("Structured reasoning_effort not serialized")
	}
	if out["custom_vendor_param"] != true {
		t.Error("Extensions not restored for unknown fields")
	}
}

func TestAnthropicExtensionsRoundTrip(t *testing.T) {
	// audit-claude-4-5 (2026-07-13): context_management promoted from
	// Extensions to structured IR field. Use an unrelated unknown vendor
	// field to test the Extensions mechanism.
	ir, err := ParseAnthropic([]byte(`{"model":"claude","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"custom_vendor_param":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ir.Extensions["custom_vendor_param"]; !ok {
		t.Fatal("Unknown field not preserved in Extensions")
	}
	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["custom_vendor_param"] != true {
		t.Fatal("Extensions field not restored")
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
