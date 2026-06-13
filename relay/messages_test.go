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

// TestConvertChatResponseToAnthropic_ReasoningContent verifies that
// OpenAI-style reasoning_content emitted by thinking upstreams
// (minimax-M3, DeepSeek-R1, etc.) is surfaced as a standalone Anthropic
// `thinking` content block before the visible text — so SDK clients
// render the trace instead of silently truncating it.
func TestConvertChatResponseToAnthropic_ReasoningContent(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r1",
		"object":"chat.completion",
		"model":"minimax-m3",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"HELLO",
				"reasoning_content":"thinking step by step"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}
	}`)
	out := convertChatResponseToAnthropic(body, "minimax-m3", "req-test-r1")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v body=%s", err, out)
	}
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text); got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "thinking" {
		t.Errorf("content[0].type = %q, want \"thinking\" (reasoning must come first)", got.Content[0].Type)
	}
	if got.Content[0].Thinking != "thinking step by step" {
		t.Errorf("content[0].thinking = %q, want \"thinking step by step\"", got.Content[0].Thinking)
	}
	if got.Content[1].Type != "text" {
		t.Errorf("content[1].type = %q, want \"text\"", got.Content[1].Type)
	}
	if got.Content[1].Text != "HELLO" {
		t.Errorf("content[1].text = %q, want \"HELLO\"", got.Content[1].Text)
	}
}

// TestConvertChatResponseToAnthropic_ReasoningEmpty covers the case
// where reasoning_content is present-but-empty: must not emit an empty
// thinking block (would produce a visible-but-empty block in SDK UIs).
func TestConvertChatResponseToAnthropic_ReasoningEmpty(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r2",
		"choices":[{"index":0,"message":{"role":"assistant","content":"HI","reasoning_content":""},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-empty")
	var got struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	for _, b := range got.Content {
		if b["type"] == "thinking" {
			t.Errorf("empty reasoning_content must not produce a thinking block; got: %s", out)
		}
	}
	if len(got.Content) != 1 || got.Content[0]["type"] != "text" {
		t.Errorf("expected exactly one text block; got %s", out)
	}
}

// TestConvertChatResponseToAnthropic_ReasoningOnly covers a response
// where the upstream produced only reasoning and no visible text (e.g.
// reasoning model that hit max_tokens while still thinking). Without
// this fix the gateway would emit an empty text block; with the fix it
// emits just the thinking block.
func TestConvertChatResponseToAnthropic_ReasoningOnly(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r3",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"","reasoning_content":"truncated thought"},
			"finish_reason":"length"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":50,"total_tokens":51}
	}`)
	out := convertChatResponseToAnthropic(body, "minimax-m3", "req-ro")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 || got.Content[0].Type != "thinking" {
		t.Errorf("expected exactly one thinking block (no empty text); got %s", out)
	}
	if got.Content[0].Thinking != "truncated thought" {
		t.Errorf("thinking content lost: %s", out)
	}
	if got.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens (length maps to max_tokens)", got.StopReason)
	}
}

// TestConvertChatResponseToAnthropic_NoReasoningRegression covers the
// pre-Phase-2 baseline: a response without reasoning_content must still
// produce a single text block (no thinking block, no extra wrapping).
func TestConvertChatResponseToAnthropic_NoReasoningRegression(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-x",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-noreg")
	var got struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 content block (no reasoning); got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want \"text\"", got.Content[0].Type)
	}
}
