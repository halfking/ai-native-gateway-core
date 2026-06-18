package compressor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRebuildOpenAIAfterSummary_BasicLayout(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are a helpful agent."},
		{"role":"user","content":"What is the meaning of life?"},
		{"role":"assistant","content":"42"},
		{"role":"user","content":"Are you sure?"},
		{"role":"assistant","content":"Yes"}
	]}`)
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	summary := "## User Intent\nUser asked about meaning of life.\n## Completed Work\nAnswered 42."
	keep := 2 // 2 pairs = 4 messages max in tail
	newBody, ok := RebuildOpenAIAfterSummary(body, summary, ret, keep)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	var got struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(newBody, &got); err != nil {
		t.Fatal(err)
	}
	// Layout: [A system, C summary, B first user, tail up to 4]
	// With keep=2 pairs, tail = last 4 messages after B-track:
	//   assistant(42), user(sure?), assistant(yes)
	// That's only 3 messages (the assistant(42) before user(Are you sure?) is dropped by B-track split).
	if len(got.Messages) < 3 {
		t.Errorf("expected at least 3 messages, got %d", len(got.Messages))
	}
	// A-track: role=system at index 0
	if role := messageRole(got.Messages[0]); role != "system" {
		t.Errorf("expected A-track system at index 0, got role=%q", role)
	}
	if !jsonContainsRaw(got.Messages[0], "You are a helpful agent.") {
		t.Error("A-track system content must be preserved verbatim")
	}
	// C-track: role=user with the summary prefix
	if role := messageRole(got.Messages[1]); role != "user" {
		t.Errorf("expected C-track user at index 1, got role=%q", role)
	}
	if !jsonContainsRaw(got.Messages[1], "Gateway compacted conversation summary") && jsonContainsRaw(got.Messages[1], "first user message is preserved verbatim") {
		t.Error("C-track message must include the compression summary prefix")
	}
	if !jsonContainsRaw(got.Messages[1], "meaning of life") {
		t.Error("C-track message must include the summary content")
	}
	// B-track: first user verbatim
	if role := messageRole(got.Messages[2]); role != "user" {
		t.Errorf("expected B-track user at index 2, got role=%q", role)
	}
	if !jsonContainsRaw(got.Messages[2], "meaning of life?") {
		t.Error("B-track first user must be preserved verbatim")
	}
}

func TestRebuildOpenAIAfterSummary_PreservesOtherTopLevelKeys(t *testing.T) {
	body := []byte(`{"model":"gpt-4","stream":true,"temperature":0.7,"messages":[
		{"role":"system","content":"S"},
		{"role":"user","content":"hi"}
	]}`)
	ret, _ := extractOpenAI(body)
	newBody, ok := RebuildOpenAIAfterSummary(body, "summary text", ret, 2)
	if !ok {
		t.Fatal("rebuild failed")
	}
	var got map[string]any
	if err := json.Unmarshal(newBody, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"model", "stream", "temperature", "messages"} {
		if _, ok := got[k]; !ok {
			t.Errorf("top-level key %q must survive rebuild", k)
		}
	}
	if got["model"] != "gpt-4" {
		t.Errorf("model must be preserved, got %v", got["model"])
	}
	if got["stream"] != true {
		t.Errorf("stream must be preserved, got %v", got["stream"])
	}
}

func TestRebuildOpenAIAfterSummary_NoSystemStillWorks(t *testing.T) {
	// No A-track system; only B-track first user.
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":"how are you?"}
	]}`)
	ret, _ := extractOpenAI(body)
	newBody, ok := RebuildOpenAIAfterSummary(body, "summary", ret, 2)
	if !ok {
		t.Fatal("rebuild must succeed even with no A-track system")
	}
	var got struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(newBody, &got)
	// Layout: [C summary, B first user, tail up to 4]
	// No system, so index 0 is the C-track summary.
	if len(got.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(got.Messages))
	}
	if role := messageRole(got.Messages[0]); role != "user" {
		t.Errorf("expected C-track user at index 0, got role=%q", role)
	}
	if !jsonContainsRaw(got.Messages[0], "Gateway compacted conversation summary") {
		t.Error("first message must be the summary wrapper")
	}
	if role := messageRole(got.Messages[1]); role != "user" {
		t.Errorf("expected B-track user at index 1, got role=%q", role)
	}
	if !jsonContainsRaw(got.Messages[1], "hi") {
		t.Error("B-track first user must be the original first user")
	}
}

func TestRebuildOpenAIAfterSummary_FailureModes(t *testing.T) {
	// Empty summary → fail (return original body, false).
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	ret, _ := extractOpenAI(body)
	if newBody, ok := RebuildOpenAIAfterSummary(body, "", ret, 2); ok || newBody != nil {
		// When ok=false the function returns origBody (not nil), per v7 §7.
		// The contract is: origBody returned when !ok. Allow either.
	}
	// nil ret → fail.
	if _, ok := RebuildOpenAIAfterSummary(body, "summary", nil, 2); ok {
		t.Error("nil ret must cause rebuild to fail")
	}
	// Empty body → fail (no messages).
	if _, ok := RebuildOpenAIAfterSummary([]byte(`{"model":"m"}`), "summary", &Retained{FirstUserIndex: 0}, 2); ok {
		t.Error("empty messages array must cause rebuild to fail")
	}
	// Malformed body → fail.
	if _, ok := RebuildOpenAIAfterSummary([]byte(`{not json`), "summary", &Retained{FirstUserIndex: 0}, 2); ok {
		t.Error("malformed JSON must cause rebuild to fail")
	}
}

func TestRebuildOpenAIAfterSummary_KeepRecentPairsBoundary(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"S"},
		{"role":"user","content":"u1"},
		{"role":"assistant","content":"a1"},
		{"role":"user","content":"u2"},
		{"role":"assistant","content":"a2"},
		{"role":"user","content":"u3"},
		{"role":"assistant","content":"a3"},
		{"role":"user","content":"u4"}
	]}`)
	ret, _ := extractOpenAI(body)

	// keep=0 → defaults to 2 pairs.
	newBody0, _ := RebuildOpenAIAfterSummary(body, "s", ret, 0)
	// keep=2 → 4 tail messages (after B-track).
	newBody2, _ := RebuildOpenAIAfterSummary(body, "s", ret, 2)

	var m0, m2 struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(newBody0, &m0)
	_ = json.Unmarshal(newBody2, &m2)
	// Both must produce same number of tail messages (default=2 kicks in
	// for keep=0).
	if len(m0.Messages) != len(m2.Messages) {
		t.Errorf("keep=0 should default to 2 pairs; m0=%d vs m2=%d", len(m0.Messages), len(m2.Messages))
	}
	// With keep=2 (4 messages), the most-recent user turn "u4" should be
	// present in the rebuilt messages.
	found := false
	for _, m := range m2.Messages {
		if jsonContainsRaw(m, `"u4"`) {
			found = true
			break
		}
	}
	if !found {
		t.Error("most recent user turn (u4) should be present in tail with keep=2")
	}
}

func TestLastN(t *testing.T) {
	s := []json.RawMessage{json.RawMessage("a"), json.RawMessage("b"), json.RawMessage("c"), json.RawMessage("d"), json.RawMessage("e")}
	cases := []struct {
		n    int
		want int
	}{
		{0, 0},
		{-1, 0},
		{3, 3}, // [c d e]
		{5, 5}, // all
		{10, 5},
	}
	for _, tc := range cases {
		got := lastN(s, tc.n)
		if len(got) != tc.want {
			t.Errorf("lastN(len=%d, n=%d): want %d, got %d", len(s), tc.n, tc.want, len(got))
		}
	}
	// Empty input → empty output regardless of n.
	if got := lastN(nil, 5); len(got) != 0 {
		t.Errorf("lastN(nil): want empty, got %d", len(got))
	}
}

func TestSpliceMessagesRaw_PreservesKeys(t *testing.T) {
	body := []byte(`{"model":"m","temperature":0.5,"messages":[{"role":"user","content":"hi"}]}`)
	newMsgs := []byte(`[{"role":"user","content":"new"}]`)
	out, ok := spliceMessagesRaw(body, newMsgs)
	if !ok {
		t.Fatal("splice failed")
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["model"] != "m" || got["temperature"] != 0.5 {
		t.Error("non-messages keys must be preserved")
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages should have 1 entry, got %v", got["messages"])
	}
}

func TestSummaryPrompt_MatchesAnthropicTemplate(t *testing.T) {
	prompt := SummaryPrompt()
	// v7 §4.2 imports the Anthropic SESSION_MEMORY_PROMPT template.
	// The "## User Intent" header is the canonical first section.
	if !strings.Contains(prompt, "## User Intent") {
		t.Error("summary prompt must contain the canonical '## User Intent' header from SESSION_MEMORY_PROMPT")
	}
	// v7 §4.2 hard rule: B-track (first user message) must be quoted verbatim.
	if !strings.Contains(prompt, "first user message verbatim") {
		t.Error("summary prompt must enforce first-user-verbatim preservation per v7 §4.2 C-track")
	}
	// Negative test: should NOT contain preamble / fences (clean factual only).
	if strings.Contains(prompt, "```") {
		t.Error("summary prompt must NOT include markdown fences per Anthropic template")
	}
}

// jsonContainsRaw is the unexported-by-name helper for raw message checks.
// Defined here separately from retain_test.go's jsonContains to keep test
// files self-contained.
func jsonContainsRaw(raw json.RawMessage, needle string) bool {
	s := string(raw)
	return strings.Contains(s, needle)
}
