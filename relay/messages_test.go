package relay

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestSelectUpstreamBodyBytes_PassthroughWhenUpstreamAnthropic verifies
// the Q4 fast-path: when the first candidate's protocol is
// "anthropic-messages", the original Anthropic body bytes are
// forwarded unchanged. The pre-converted OpenAI body is ignored
// entirely.
func TestSelectUpstreamBodyBytes_PassthroughWhenUpstreamAnthropic(t *testing.T) {
	originalBody := []byte(`{"model":"minimax-m2.7","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":false}`)
	convertedBody := []byte(`{"model":"minimax-m2.7","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":false}`)

	candidates := []provider.Candidate{
		{ProviderID: 14, CredentialID: 6, Protocol: "anthropic-messages"},
	}

	got := selectUpstreamBodyBytes(candidates, originalBody, convertedBody)

	if string(got) != string(originalBody) {
		t.Errorf("body bytes must be forwarded unchanged for Q4 passthrough\n got: %s\nwant: %s", string(got), string(originalBody))
	}
}

// TestSelectUpstreamBodyBytes_ConvertsWhenUpstreamOpenAI verifies the
// Q2 path: when the first candidate's protocol is openai-completions,
// the pre-converted OpenAI body is returned.
func TestSelectUpstreamBodyBytes_ConvertsWhenUpstreamOpenAI(t *testing.T) {
	originalBody := []byte(`{"model":"minimax-m2.7","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)

	chatBody := map[string]any{
		"model":      "minimax-m2.7",
		"max_tokens": 256,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":     false,
	}
	convertedBody, err := json.Marshal(chatBody)
	if err != nil {
		t.Fatalf("test setup: marshal: %v", err)
	}

	candidates := []provider.Candidate{
		{ProviderID: 1, CredentialID: 1, Protocol: "openai-completions"},
	}

	got := selectUpstreamBodyBytes(candidates, originalBody, convertedBody)

	if string(got) != string(convertedBody) {
		t.Errorf("body bytes must be the converted OpenAI body for Q2 path\n got: %s\nwant: %s", string(got), string(convertedBody))
	}
	// Output must NOT be the raw Anthropic body verbatim.
	if string(got) == string(originalBody) {
		t.Error("body was not converted; got raw Anthropic body for Q2 path")
	}
}

// TestSelectUpstreamBodyBytes_ConvertsWhenProtocolEmpty verifies that
// an empty Protocol field (older provider rows) falls through to
// conversion — preserves Q1/Q2 behavior.
func TestSelectUpstreamBodyBytes_ConvertsWhenProtocolEmpty(t *testing.T) {
	originalBody := []byte(`{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	convertedBody := []byte(`{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)

	candidates := []provider.Candidate{{ProviderID: 1, CredentialID: 1, Protocol: ""}}

	got := selectUpstreamBodyBytes(candidates, originalBody, convertedBody)

	if string(got) != string(convertedBody) {
		t.Errorf("empty Protocol must default to conversion (Q1/Q2 behavior preservation)\n got: %s\nwant: %s", string(got), string(convertedBody))
	}
}

// TestSelectUpstreamBodyBytes_ConvertsWhenProtocolOpenAIResponses
// verifies the openai-responses protocol also takes the Q2 path
// (the existing /v1/chat completions executor handles both).
func TestSelectUpstreamBodyBytes_ConvertsWhenProtocolOpenAIResponses(t *testing.T) {
	originalBody := []byte(`{"model":"x","max_tokens":10}`)
	convertedBody := []byte(`{"model":"x","max_tokens":10}`)

	candidates := []provider.Candidate{{ProviderID: 1, CredentialID: 1, Protocol: "openai-responses"}}

	got := selectUpstreamBodyBytes(candidates, originalBody, convertedBody)

	if string(got) != string(convertedBody) {
		t.Errorf("openai-responses must take the Q2 path\n got: %s\nwant: %s", string(got), string(convertedBody))
	}
}

// TestSelectUpstreamBodyBytes_NoCandidatesFallsBackToConvert verifies
// the safety net: an empty candidates slice still produces the
// converted body (this should not happen in practice — GetCandidates
// returns 503 upstream — but it guards against a future caller
// forgetting to check len(candidates) > 0).
func TestSelectUpstreamBodyBytes_NoCandidatesFallsBackToConvert(t *testing.T) {
	originalBody := []byte(`{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	convertedBody := []byte(`{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)

	got := selectUpstreamBodyBytes(nil, originalBody, convertedBody)

	if string(got) != string(convertedBody) {
		t.Errorf("no candidates must fall back to conversion (defensive)\n got: %s\nwant: %s", string(got), string(convertedBody))
	}
	if len(got) == 0 {
		t.Error("expected non-empty body")
	}
}
