package sessioncontext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFitAnthropicActiveToolPair(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "original task"},
		map[string]any{"role": "assistant", "content": strings.Repeat("old log ", 2000)},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "c1", "name": "read", "input": map[string]any{"path": "/x"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "ok"}}},
	}})
	out, err := Fit(b, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"type":"tool_use"`)) {
		t.Fatalf("active tool call was discarded while result survived: %s", out)
	}
}
func TestFitResponsesActiveToolPair(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"input": []any{
		map[string]any{"type": "message", "role": "assistant", "content": strings.Repeat("old log ", 2000)},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "read", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
	}})
	out, err := Fit(b, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"type":"function_call"`)) {
		t.Fatalf("active function call discarded: %s", out)
	}
}

func TestFitKeepsLatestHumanTaskBeforeToolResults(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "original task"},
		map[string]any{"role": "assistant", "content": strings.Repeat("old log ", 2000)},
		map[string]any{"role": "user", "content": "LATEST: change only billing port 6432, never delete db.yaml"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "c1", "name": "read", "input": map[string]any{"path": "/x"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "ok"}}},
	}})
	out, err := Fit(b, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string][]map[string]any
	_ = json.Unmarshal(out, &v)
	found := false
	for _, m := range v["messages"] {
		if m["content"] == "LATEST: change only billing port 6432, never delete db.yaml" {
			found = true
		}
	}
	if !found {
		t.Fatalf("latest task summarized: %s", out)
	}
}
func TestExcerptKeepsMatchingFactInLongLine(t *testing.T) {
	text := strings.Repeat("x", 3000) + " billing database port 6432 config /srv/billing/db.yaml " + strings.Repeat("x", 5000)
	got := sampleText(text, "billing database port config", 1024)
	if !strings.Contains(got, "6432") || !strings.Contains(got, "/srv/billing/db.yaml") {
		t.Fatalf("lost matched fact: %s", got)
	}
}
