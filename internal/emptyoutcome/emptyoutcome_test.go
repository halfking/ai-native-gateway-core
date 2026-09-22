package emptyoutcome

import "testing"

// TestIsEmptyOutcome pins the single semantic decision point (Wave4-D2):
// no emitted output ⇒ empty outcome, unless a client disconnect is pending
// (completed-replay semantics win).
func TestIsEmptyOutcome(t *testing.T) {
	tests := []struct {
		name          string
		emittedOutput bool
		clientDisc    bool
		want          bool
	}{
		{"no output clean stream", false, false, true},
		{"content emitted", true, false, false},
		{"client disconnect suppresses empty classification", false, true, false},
		{"output plus disconnect", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEmptyOutcome(tt.emittedOutput, tt.clientDisc); got != tt.want {
				t.Fatalf("IsEmptyOutcome(%v,%v) = %v, want %v", tt.emittedOutput, tt.clientDisc, got, tt.want)
			}
		})
	}
}

// TestChatChunkHasOutput pins the per-frame rule set: usage-only, role-only
// and control frames carry no output; content / reasoning / tool calls /
// audio do.
func TestChatChunkHasOutput(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"empty payload", "", false},
		{"done sentinel", "[DONE]", false},
		{"usage-only chunk", `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}}`, false},
		{"role-only announcement", `{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, false},
		{"empty delta", `{"choices":[{"index":0,"delta":{}}]}`, false},
		{"text content", `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`, true},
		{"reasoning content", `{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`, true},
		{"tool call", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`, true},
		{"audio delta", `{"choices":[{"index":0,"delta":{"audio":{"data":"YWJj"}}}]}`, true},
		{"malformed JSON", `{not-json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChatChunkHasOutput(tt.payload); got != tt.want {
				t.Fatalf("ChatChunkHasOutput(%s) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

// TestChatSemanticDelta pins the gate's consecutive-empty counter input:
// only valid delta frames with ≥1 choice and no output count as semantic
// empty deltas; usage/control frames must reset or be ignored.
func TestChatSemanticDelta(t *testing.T) {
	semantic := `{"choices":[{"index":0,"delta":{}}]}`
	if !ChatSemanticDelta(semantic) {
		t.Fatalf("ChatSemanticDelta(empty delta) = false, want true")
	}
	for name, payload := range map[string]string{
		"usage frame":  `{"choices":[],"usage":{"prompt_tokens":1}}`,
		"done":         "[DONE]",
		"empty string": "",
		"content":      `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		"malformed":    `{oops`,
	} {
		if ChatSemanticDelta(payload) {
			t.Fatalf("ChatSemanticDelta(%s) = true, want false", name)
		}
	}
}

// TestClassifyBody re-pins the non-stream envelope routing (moved verbatim
// from streaming): key precedence choices→content→output, unknown shapes
// report not-empty.
func TestClassifyBody(t *testing.T) {
	if f, empty := ClassifyBody([]byte(`{"id":"unknown"}`)); f != FormatUnknown || empty {
		t.Fatalf("unknown JSON = (%v,%v), want (FormatUnknown,false)", f, empty)
	}
	if f, empty := ClassifyBody([]byte(`{"choices":[{"message":{"content":""}}]}`)); f != FormatChat || !empty {
		t.Fatalf("empty chat = (%v,%v), want (FormatChat,true)", f, empty)
	}
	if f, empty := ClassifyBody([]byte(`{"type":"message","content":[{"type":"text","text":"hello"}]}`)); f != FormatAnthropic || empty {
		t.Fatalf("anthropic text = (%v,%v), want (FormatAnthropic,false)", f, empty)
	}
	if f, empty := ClassifyBody([]byte(`{"object":"response","output":[]}`)); f != FormatResponses || !empty {
		t.Fatalf("empty responses = (%v,%v), want (FormatResponses,true)", f, empty)
	}
	if _, empty := ClassifyBody(nil); empty {
		t.Fatalf("nil body must be (FormatUnknown,false)")
	}
}
