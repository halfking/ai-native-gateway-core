package compressor

import (
	"encoding/json"
	"testing"
)

func TestExtractOpenAI_SingleSystemSingleUser(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are a helpful agent. Always be polite."},
		{"role":"user","content":"Hello"}
	]}`)
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if !ret.IsPinnedSystem() {
		t.Error("expected A-track system message pinned")
	}
	if !ret.IsPinnedFirstUser() {
		t.Error("expected B-track first user pinned")
	}
	if ret.FirstUserIndex != 1 {
		t.Errorf("FirstUserIndex: want 1, got %d", ret.FirstUserIndex)
	}
	if got := len(ret.SystemMessages); got != 1 {
		t.Errorf("SystemMessages count: want 1, got %d", got)
	}
}

func TestExtractOpenAI_MultipleSystemsMultipleUsers(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"A"},
		{"role":"system","content":"B"},
		{"role":"user","content":"first user"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":"second user"}
	]}`)
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ret.SystemMessages); got != 2 {
		t.Errorf("SystemMessages count: want 2, got %d", got)
	}
	if !ret.IsPinnedFirstUser() {
		t.Fatal("first user must be pinned")
	}
	if !jsonContains(ret.FirstUser, "first user") {
		t.Error("FirstUser should contain 'first user', not 'second user'")
	}
	if jsonContains(ret.FirstUser, "second user") {
		t.Error("FirstUser must NOT be the second user message")
	}
	if ret.FirstUserIndex != 2 {
		t.Errorf("FirstUserIndex: want 2 (after 2 systems), got %d", ret.FirstUserIndex)
	}
}

func TestExtractOpenAI_NoSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"}
	]}`)
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if ret.IsPinnedSystem() {
		t.Error("no system in input; A-track must be empty")
	}
	if !ret.IsPinnedFirstUser() {
		t.Error("first user must still be pinned")
	}
}

func TestExtractOpenAI_NoMessages(t *testing.T) {
	body := []byte(`{"model":"m"}`)
	if _, err := extractOpenAI(body); err == nil {
		t.Error("empty messages array must error so caller skips compression")
	}
}

func TestExtractOpenAI_MalformedBody(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"`) // truncated
	if _, err := extractOpenAI(body); err == nil {
		t.Error("malformed JSON must error gracefully")
	}
}

func TestExtractAnthropic_TopLevelSystemString(t *testing.T) {
	body := []byte(`{"model":"m","system":"You are Claude.","messages":[
		{"role":"user","content":"Hello"}
	]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if !ret.IsPinnedSystem() {
		t.Error("Anthropic top-level system string must be pinned")
	}
	if got := len(ret.SystemMessages); got != 1 {
		t.Errorf("SystemMessages: want 1, got %d", got)
	}
	if !ret.IsPinnedFirstUser() {
		t.Error("first user must be pinned")
	}
}

func TestExtractAnthropic_TopLevelSystemBlocks(t *testing.T) {
	body := []byte(`{"model":"m","system":[
		{"type":"text","text":"You are Claude."},
		{"type":"text","text":"Be concise."}
	],"messages":[
		{"role":"user","content":"Hello"}
	]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if !ret.IsPinnedSystem() {
		t.Error("Anthropic top-level system blocks must be pinned")
	}
}

func TestExtractAnthropic_NoSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if ret.IsPinnedSystem() {
		t.Error("no system in input; A-track must be empty")
	}
	if !ret.IsPinnedFirstUser() {
		t.Error("first user must be pinned")
	}
}

func TestRetained_IsPinned_NilSafety(t *testing.T) {
	var r *Retained
	if r.IsPinnedSystem() {
		t.Error("nil IsPinnedSystem must be false")
	}
	if r.IsPinnedFirstUser() {
		t.Error("nil IsPinnedFirstUser must be false")
	}
}

func TestMessageRole(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"role":"user","content":"x"}`, "user"},
		{`{"role":"assistant","content":"x"}`, "assistant"},
		{`{"role":"system","content":"x"}`, "system"},
		{`{"role":"tool","content":"x"}`, "tool"},
		{`{"content":"x"}`, ""},
		{`not json`, ""},
	}
	for _, tc := range cases {
		got := messageRole(json.RawMessage(tc.raw))
		if got != tc.want {
			t.Errorf("messageRole(%s): want %q, got %q", tc.raw, tc.want, got)
		}
	}
}

// jsonContains is a small helper for tests - the retained raw JSON may
// have any whitespace / key order, so we check substring presence.
func jsonContains(raw *json.RawMessage, needle string) bool {
	if raw == nil {
		return false
	}
	for i := 0; i+len(needle) <= len(*raw); i++ {
		if string((*raw)[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
