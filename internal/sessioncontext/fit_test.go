package sessioncontext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func wire(t *testing.T, messages []map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"model": "test", "messages": messages, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestFitPreservesTaskConstraintsAndRelevantOldFact(t *testing.T) {
	body := wire(t, []map[string]any{
		{"role": "system", "content": "Never change the approved port without consent."},
		{"role": "user", "content": "Maintain the billing service."},
		{"role": "assistant", "content": strings.Repeat("noise ", 8000) + "\nBilling database port is 6432; config path /srv/billing/db.yaml.\n" + strings.Repeat("noise ", 8000)},
		{"role": "user", "content": "What is the billing database port and config path?"},
	})
	original := bytes.Clone(body)
	got, err := Fit(body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 4096 || !json.Valid(got) {
		t.Fatalf("invalid/oversized: %d", len(got))
	}
	for _, want := range []string{"Never change the approved port without consent.", "Maintain the billing service.", "6432", "/srv/billing/db.yaml", "What is the billing database port and config path?"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("lost required fact: %s", want)
		}
	}
	if !bytes.Equal(body, original) {
		t.Fatal("mutated source used by other buckets")
	}
}
func TestFitPreservesActiveToolChainAndDropsOldPairsTogether(t *testing.T) {
	body := wire(t, []map[string]any{
		{"role": "user", "content": "Initial task"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "old", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}},
		{"role": "tool", "tool_call_id": "old", "content": strings.Repeat("log ", 10000)},
		{"role": "user", "content": "Current task"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "active", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{\"exact\":6432}"}}}},
		{"role": "tool", "tool_call_id": "active", "content": "exact result"},
	})
	got, err := Fit(body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatal(err)
	}
	calls, results := 0, 0
	for _, m := range envelope.Messages {
		if c, ok := m["tool_calls"].([]any); ok {
			calls += len(c)
		}
		if m["role"] == "tool" {
			results++
		}
	}
	if calls != 1 || results != 1 || !bytes.Contains(got, []byte("active")) {
		t.Fatalf("broken active chain: %s", got)
	}
}
func TestFitRejectsOversizeProtectedCurrentInput(t *testing.T) {
	body := wire(t, []map[string]any{{"role": "user", "content": strings.Repeat("当前任务", 4000)}})
	if _, err := Fit(body, 4096); err == nil {
		t.Fatal("silently truncated current task")
	}
}
func TestFitDoesNotRewriteSmallBody(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	got, err := Fit(body, 4096)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("small body changed: %s %v", got, err)
	}
}
func TestFitProtocolShapesAndMalformedInput(t *testing.T) {
	for _, key := range []string{"input", "contents"} {
		t.Run(key, func(t *testing.T) {
			old, current := map[string]any{"role": "assistant", "content": strings.Repeat("history ", 4000)}, map[string]any{"role": "user", "content": "latest"}
			if key == "contents" {
				old = map[string]any{"role": "model", "parts": []any{map[string]any{"text": strings.Repeat("history ", 4000)}}}
				current = map[string]any{"role": "user", "parts": []any{map[string]any{"text": "latest"}}}
			}
			b, _ := json.Marshal(map[string]any{key: []any{old, current}})
			got, err := Fit(b, 2048)
			if err != nil || len(got) > 2048 || !json.Valid(got) {
				t.Fatalf("%s: %s %v", key, got, err)
			}
		})
	}
	if _, err := Fit([]byte(`{"messages": [`), 100); err == nil {
		t.Fatal("accepted malformed JSON")
	}
}
