package compressor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRebuildAnthropicAfterSummary_StringSystem(t *testing.T) {
	body := []byte(`{"model":"m","system":"You are Claude.","messages":[
		{"role":"user","content":"Hello"},
		{"role":"assistant","content":"Hi there"},
		{"role":"user","content":"How are you?"}
	]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	summary := "## User Intent\nUser greeted Claude.\n## Completed Work\nGreeting acknowledged."
	newBody, ok := RebuildAnthropicAfterSummary(body, summary, ret, 2)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	var got struct {
		System   string          `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(newBody, &got); err != nil {
		t.Fatal(err)
	}
	// C-track: original system + summary
	if !strings.Contains(got.System, "You are Claude.") {
		t.Error("original system must be preserved")
	}
	if !strings.Contains(got.System, "gateway injection") {
		t.Error("summary must be injected via gateway marker")
	}
	if !strings.Contains(got.System, "User Intent") {
		t.Error("summary content must be present in system field")
	}
	// B-track + tail: first user + tail up to 4 messages
	if len(got.Messages) < 1 {
		t.Fatal("messages must be non-empty after rebuild")
	}
	firstMsg := got.Messages[0]
	if role := messageRole(firstMsg); role != "user" {
		t.Errorf("first message must be B-track user, got role=%q", role)
	}
	if !strings.Contains(string(firstMsg), "Hello") {
		t.Error("B-track first user must be preserved verbatim")
	}
}

func TestRebuildAnthropicAfterSummary_BlocksSystem(t *testing.T) {
	body := []byte(`{"model":"m","system":[
		{"type":"text","text":"You are Claude."},
		{"type":"text","text":"Be concise."}
	],"messages":[{"role":"user","content":"hi"}]}`)
	ret, _ := extractAnthropic(body)
	summary := "summary content"
	newBody, ok := RebuildAnthropicAfterSummary(body, summary, ret, 2)
	if !ok {
		t.Fatal("rebuild must succeed for blocks-shaped system")
	}
	var got struct {
		System []json.RawMessage `json:"system"`
	}
	_ = json.Unmarshal(newBody, &got)
	// Original 2 blocks + 1 new gateway block = 3 blocks
	if len(got.System) != 3 {
		t.Errorf("expected 3 system blocks after append, got %d", len(got.System))
	}
	// Last block is the gateway injection
	last := got.System[len(got.System)-1]
	if !strings.Contains(string(last), "summary content") {
		t.Error("last block should contain the summary")
	}
	if !strings.Contains(string(last), "gateway injection") {
		t.Error("last block should contain the gateway marker")
	}
}

func TestRebuildAnthropicAfterSummary_NoOriginalSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	ret, _ := extractAnthropic(body)
	newBody, ok := RebuildAnthropicAfterSummary(body, "summary", ret, 2)
	if !ok {
		t.Fatal("rebuild must succeed with no original system")
	}
	var got struct {
		System []json.RawMessage `json:"system"`
	}
	_ = json.Unmarshal(newBody, &got)
	if len(got.System) != 1 {
		t.Errorf("expected 1 system block when no original system, got %d", len(got.System))
	}
	if !strings.Contains(string(got.System[0]), "summary") {
		t.Error("synthesized system block must contain the summary")
	}
}

func TestRebuildAnthropicAfterSummary_PreservesOtherTopLevelKeys(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":1024,"metadata":{"user_id":"u"},"system":"S","messages":[{"role":"user","content":"hi"}]}`)
	ret, _ := extractAnthropic(body)
	newBody, ok := RebuildAnthropicAfterSummary(body, "summary", ret, 2)
	if !ok {
		t.Fatal("rebuild failed")
	}
	var got map[string]any
	_ = json.Unmarshal(newBody, &got)
	for _, k := range []string{"model", "max_tokens", "metadata", "system", "messages"} {
		if _, ok := got[k]; !ok {
			t.Errorf("top-level key %q must survive rebuild", k)
		}
	}
	if got["max_tokens"].(float64) != 1024 {
		t.Errorf("max_tokens must be preserved, got %v", got["max_tokens"])
	}
}

func TestRebuildAnthropicAfterSummary_FailureModes(t *testing.T) {
	body := []byte(`{"model":"m","system":"S","messages":[{"role":"user","content":"hi"}]}`)
	ret, _ := extractAnthropic(body)
	// nil ret → fail
	if _, ok := RebuildAnthropicAfterSummary(body, "s", nil, 2); ok {
		t.Error("nil ret must fail")
	}
	// empty summary → fail
	if _, ok := RebuildAnthropicAfterSummary(body, "", ret, 2); ok {
		t.Error("empty summary must fail")
	}
	// malformed body → fail
	if _, ok := RebuildAnthropicAfterSummary([]byte(`{not json`), "s", ret, 2); ok {
		t.Error("malformed body must fail")
	}
}

func TestRebuildAnthropicSystemField_StringShape(t *testing.T) {
	orig := json.RawMessage(`"You are Claude."`)
	out, err := rebuildAnthropicSystemField(orig, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	_ = json.Unmarshal(out, &got)
	if !strings.Contains(got, "You are Claude.") {
		t.Error("original must be preserved")
	}
	if !strings.Contains(got, "summary") {
		t.Error("summary must be appended")
	}
	if !strings.Contains(got, "gateway injection") {
		t.Error("gateway marker must be present")
	}
	// Verify ordering: original < marker < summary
	if strings.Index(got, "You are Claude.") > strings.Index(got, "summary") {
		t.Error("original must come before summary")
	}
}

func TestRebuildAnthropicSystemField_EmptyOriginal(t *testing.T) {
	out, err := rebuildAnthropicSystemField(nil, "summary")
	if err != nil {
		t.Fatal(err)
	}
	// Should produce a single text block (no original to preserve).
	var blocks []json.RawMessage
	_ = json.Unmarshal(out, &blocks)
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
}

func TestTrimAnthropicTail_DropsToolResultOnly(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"hi"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"r"}]}`),
		json.RawMessage(`{"role":"assistant","content":"ok"}`),
		json.RawMessage(`{"role":"user","content":"plain text"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"r2"}]}`),
	}
	cleaned, dropped := TrimAnthropicTail(messages)
	if dropped != 2 {
		t.Errorf("dropped count: want 2, got %d", dropped)
	}
	if len(cleaned) != 3 {
		t.Errorf("cleaned length: want 3, got %d", len(cleaned))
	}
	// Verify tool_result-only are gone
	for _, m := range cleaned {
		if isToolResultOnly(m) {
			t.Error("cleaned list still contains a tool_result-only message")
		}
	}
}

func TestIsToolResultOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"string_content", `{"role":"user","content":"hi"}`, false},
		{"empty_content", `{"role":"user","content":""}`, false},
		{"single_tool_result", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"r"}]}`, true},
		{"mixed_blocks", `{"role":"user","content":[{"type":"text","text":"q"},{"type":"tool_result","tool_use_id":"a","content":"r"}]}`, false},
		{"assistant_role", `{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"a","content":"r"}]}`, false},
		{"null_content", `{"role":"user","content":null}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isToolResultOnly(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("isToolResultOnly(%s): want %v, got %v", tc.name, tc.want, got)
			}
		})
	}
}
