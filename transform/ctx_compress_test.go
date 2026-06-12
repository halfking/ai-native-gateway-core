package transform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompressMessagesIfNeeded_NoOpWhenFits(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"hi"}
	]}`)
	out := CompressMessagesIfNeeded(body, 100000)
	if string(out) != string(body) {
		t.Fatalf("expected no trim; got %s", out)
	}
}

func TestCompressMessagesIfNeeded_TrimsWhenOver(t *testing.T) {
	// Build a body whose messages add up to far more than 50 tokens.
	// With 50 token context window, the soft limit is ~42 tokens. The
	// body is ~ 1 + (1 + role+content overhead) * 11 ≈ 200+ chars;
	// 200/3.5 ≈ 57 tokens > 42 → must trim.
	long := strings.Repeat("a", 200)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"}
	]}`)
	out := CompressMessagesIfNeeded(body, 50)

	var before, after struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(out, &after)
	if len(after.Messages) >= len(before.Messages) {
		t.Fatalf("expected messages to shrink; before=%d after=%d", len(before.Messages), len(after.Messages))
	}
	// system message must be preserved
	if !isSystemMessage(after.Messages[0]) {
		t.Fatalf("expected first message to be system; got %s", after.Messages[0])
	}
}

func TestCompressMessagesIfNeeded_NoContextWindow(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 10000) + `"}]}`)
	out := CompressMessagesIfNeeded(body, 0)
	if string(out) != string(body) {
		t.Fatalf("expected no trim when context_window=0")
	}
}

func TestCompressMessagesIfNeeded_NotJSON(t *testing.T) {
	body := []byte(`not json at all`)
	out := CompressMessagesIfNeeded(body, 100000)
	if string(out) != string(body) {
		t.Fatalf("expected non-JSON body to be untouched")
	}
}

func TestCompressMessagesIfNeeded_PreservesOtherFields(t *testing.T) {
	long := strings.Repeat("a", 200)
	body := []byte(`{"model":"m","temperature":0.7,"stream":true,"tools":[{"type":"function","function":{"name":"x"}}],"messages":[
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"}
	]}`)
	out := CompressMessagesIfNeeded(body, 50)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if _, ok := got["temperature"]; !ok {
		t.Fatalf("temperature lost")
	}
	if _, ok := got["stream"]; !ok {
		t.Fatalf("stream lost")
	}
	if _, ok := got["tools"]; !ok {
		t.Fatalf("tools lost")
	}
}
