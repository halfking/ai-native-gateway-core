package ir

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func historyCall(id string) ToolCall {
	c := ToolCall{ID: id, Type: "function"}
	c.Function.Name = "Read"
	c.Function.Arguments = "{}"
	return c
}
func TestValidateToolHistoryMissingAndInterleavedResults(t *testing.T) {
	req := &InternalRequest{Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{historyCall("a"), historyCall("b"), historyCall("c")}},
		{Role: "tool", ToolCallID: "c", Content: []ContentBlock{{Type: "text", Text: "real-c"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "resize notice"}}},
		{Role: "tool", ToolCallID: "a", Content: []ContentBlock{{Type: "text", Text: "real-a"}}},
	}}
	got := ValidateAndFixRequest(req, "")
	if len(got.Messages) != 5 {
		t.Fatalf("actual calls/results/text were not retained and repaired: %+v", got.Messages)
	}
	for i, id := range []string{"a", "b", "c"} {
		if got.Messages[i+1].ToolCallID != id {
			t.Fatalf("result %d=%s want %s", i, got.Messages[i+1].ToolCallID, id)
		}
	}
	if len(got.Messages[2].Content) != 1 || got.Messages[2].Content[0].ToolResult == nil || !got.Messages[2].Content[0].ToolResult.IsError {
		t.Fatalf("missing result is not an explicit error: %+v", got.Messages[2])
	}
	if got.Messages[4].Content[0].Text != "resize notice" {
		t.Fatal("notice lost")
	}
}
func TestAnthropicHistoryRoundTripPreservesActualCallsResultsAndError(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}]},{"role":"user","content":[{"type":"text","text":"notice"},{"type":"tool_result","tool_use_id":"b","content":"real-b"}]}]}`)
	req, err := ParseAnthropic(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = ValidateAndFixRequest(req, "")
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), `"type":"tool_use"`) != 2 || strings.Count(string(out), `"type":"tool_result"`) != 2 {
		t.Fatalf("calls or results lost/duplicated: %s", out)
	}
	if !strings.Contains(string(out), `"is_error":true`) || !strings.Contains(string(out), "real-b") || !strings.Contains(string(out), "notice") {
		t.Fatalf("result/error/text lost: %s", out)
	}
}

func TestValidateMissingAnthropicCallIDMatchesSynthetic(t *testing.T) {
	req, err := ParseAnthropic([]byte(`{"model":"m","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"","name":"Read","input":{}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req = ValidateAndFixRequest(req, "")
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), `"type":"tool_use"`) != 1 || strings.Contains(string(body), `"id":""`) {
		t.Fatalf("generated ID and mirrored actual call diverged: %s", body)
	}
	if !strings.Contains(string(body), `"tool_use_id":"call_generated_0_0"`) {
		t.Fatalf("synthetic answer did not use stable generated ID: %s", body)
	}
}

func TestSerializeAnthropicOnlyDeduplicatesProvenCallMirrors(t *testing.T) {
	for _, use := range []ToolUse{
		{ID: "same", Name: "Other", Input: []byte(`{}`)},
		{ID: "same", Name: "Read", Input: []byte(`{"n":2}`)},
		{ID: "same", Name: "Other", Input: []byte(`{"n":2}`)},
	} {
		call := historyCall("same")
		req := &InternalRequest{Model: "m", Messages: []Message{{Role: "assistant", ToolCalls: []ToolCall{call}, Content: []ContentBlock{{Type: "tool_use", ToolUse: &use}}}}}
		body, err := SerializeAnthropic(req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(body), `"type":"tool_use"`) != 2 {
			t.Fatalf("distinct call was silently treated as a mirror: %s", body)
		}
	}
}

func TestSerializeAnthropicMirrorSuppressesOnlyOneCanonicalOccurrence(t *testing.T) {
	call := historyCall("same")
	req := &InternalRequest{Model: "m", Messages: []Message{{Role: "assistant", ToolCalls: []ToolCall{call, call}, Content: []ContentBlock{{Type: "tool_use", ToolUse: &ToolUse{ID: "same", Name: "Read", Input: []byte(`{}`)}}}}}}
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), `"type":"tool_use"`) != 2 {
		t.Fatalf("duplicate actual canonical call disappeared behind one mirror: %s", body)
	}
}

func TestAnthropicCompleteHistoryPreservesMirroredCallMetadataAndPosition(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"text","text":"before-call"},{"type":"tool_use","id":"a","name":"Read","input":{"n":9007199254740993},"cache_control":{"type":"ephemeral"}},{"type":"text","text":"after-call"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"real-a"}]}]}`)
	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := SerializeAnthropic(ValidateAndFixRequest(req, ""))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(fixed, &root); err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]json.RawMessage
	if err = json.Unmarshal(root.Messages[1].Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || string(blocks[0]["text"]) != `"before-call"` || string(blocks[1]["type"]) != `"tool_use"` || string(blocks[2]["text"]) != `"after-call"` {
		t.Fatalf("actual content order changed: %s", fixed)
	}
	if string(blocks[1]["cache_control"]) != `{"type":"ephemeral"}` || !bytes.Contains(blocks[1]["input"], []byte("9007199254740993")) {
		t.Fatalf("actual call metadata/input lost: %s", fixed)
	}
	if strings.Count(string(fixed), `"type":"tool_use"`) != 1 || strings.Count(string(fixed), `"type":"tool_result"`) != 1 || strings.Contains(string(fixed), "interrupted") {
		t.Fatalf("complete exchange changed cardinality: %s", fixed)
	}
}
