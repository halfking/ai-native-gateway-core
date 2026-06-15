package memora

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRebuildBodyWithMemoraSnippets_OpenAI(t *testing.T) {
	orig := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"What is the weather in Tokyo?"},
		{"role":"assistant","content":"Sunny, 22C"},
		{"role":"user","content":"What about Paris?"}
	]}`)
	snippets := []Memory{
		{ID: "1", Text: "Tokyo in June averages 22C and is humid.", Score: 0.9},
		{ID: "2", Text: "Paris averages 18C with frequent rain.", Score: 0.85},
	}
	got, ok := RebuildBodyWithMemoraSnippets(orig, snippets, 2)
	if !ok {
		t.Fatal("expected ok=true")
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if len(parsed.Messages) != 5 {
		t.Fatalf("expected 5 messages (1 system + 1 dynamic_context + 2 tail + 1 last user), got %d", len(parsed.Messages))
	}
	// The dynamic_context message should be at index 1 (right after system).
	dyn := parsed.Messages[1]
	if dyn["role"] != "user" {
		t.Fatalf("dyn msg role should be user, got %v", dyn["role"])
	}
	content, _ := dyn["content"].(string)
	if !strings.Contains(content, "Tokyo") || !strings.Contains(content, "Paris") {
		t.Fatalf("dyn msg should contain both snippets, got: %s", content[:200])
	}
}

func TestRebuildBodyWithMemoraSnippets_PreservesToolUseId(t *testing.T) {
	// Critical: tool_use_id pairs must NOT be broken by rebuilder.
	orig := []byte(`{"model":"m","messages":[
		{"role":"user","content":"What is the weather?"},
		{"role":"assistant","tool_calls":[{"id":"call_X","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_X","content":"sunny"}
	]}`)
	snippets := []Memory{{ID: "1", Text: "Today's weather is sunny."}}
	got, ok := RebuildBodyWithMemoraSnippets(orig, snippets, 2)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(string(got), `"id":"call_X"`) {
		t.Fatal("rebuilder lost the tool_use id")
	}
	if !strings.Contains(string(got), `"tool_call_id":"call_X"`) {
		t.Fatal("rebuilder lost the tool result id")
	}
}

func TestRebuildBodyWithMemoraSnippets_EmptySnippets(t *testing.T) {
	orig := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	got, ok := RebuildBodyWithMemoraSnippets(orig, nil, 2)
	if ok || got != nil {
		t.Fatalf("expected (nil, false) for empty snippets, got (%v, %v)", got, ok)
	}
}

func TestRebuildBodyWithMemoraSnippets_PreservesOtherFields(t *testing.T) {
	orig := []byte(`{"model":"m","temperature":0.7,"stream":true,"tools":[{"type":"function","function":{"name":"x"}}],"messages":[{"role":"user","content":"hi"}]}`)
	snippets := []Memory{{Text: "fact"}}
	got, ok := RebuildBodyWithMemoraSnippets(orig, snippets, 1)
	if !ok {
		t.Fatal("expected ok=true")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["temperature"] != 0.7 {
		t.Fatalf("temperature lost: %v", parsed["temperature"])
	}
	if parsed["stream"] != true {
		t.Fatalf("stream lost")
	}
	if _, ok := parsed["tools"]; !ok {
		t.Fatal("tools lost")
	}
}

func TestSpliceMessagesRaw_RoundTrip(t *testing.T) {
	orig := []byte(`{"model":"m","temperature":0.5,"messages":[{"role":"user","content":"a"}]}`)
	newMsgs := []byte(`[{"role":"user","content":"b"}]`)
	got, ok := spliceMessagesRaw(orig, newMsgs)
	if !ok {
		t.Fatal("splice failed")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["temperature"] != 0.5 {
		t.Fatalf("temperature lost")
	}
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "b" {
		t.Fatalf("messages not replaced: %v", msgs)
	}
}
