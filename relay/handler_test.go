package relay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestChatSelectUpstreamBodyBytes_AlwaysPassthrough verifies the
// fix: selectChatUpstreamBodyBytes no longer converts to Anthropic
// based on candidates[0].Protocol. It always returns the original
// body. Conversion now happens per-candidate inside executeAnthropic
// via the ChatToAnthropic callback, which avoids the body-format /
// routed-candidate mismatch bug that caused "invalid tool type" (2013)
// errors when candidates[0] was anthropic but the executor picked an
// openai-completions credential.
func TestChatSelectUpstreamBodyBytes_AlwaysPassthrough(t *testing.T) {
	originalBody := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":256,
        "messages":[
            {"role":"system","content":"you are a poet"},
            {"role":"user","content":"hi"}
        ]
    }`)

	protocols := []string{"anthropic-messages", "openai-completions", "", "openai-responses"}
	for _, proto := range protocols {
		cands := []provider.Candidate{{Protocol: proto}}
		out, err := selectChatUpstreamBodyBytes(cands, originalBody)
		if err != nil {
			t.Errorf("protocol=%q: unexpected error: %v", proto, err)
			continue
		}
		if string(out) != string(originalBody) {
			t.Errorf("protocol=%q: expected passthrough; got %s", proto, string(out))
		}
	}

	// Also with no candidates
	out, err := selectChatUpstreamBodyBytes(nil, originalBody)
	if err != nil {
		t.Fatalf("nil candidates: unexpected error: %v", err)
	}
	if string(out) != string(originalBody) {
		t.Errorf("nil candidates: expected passthrough; got %s", string(out))
	}
}

// TestChatHandler_OpenAIToAnthropic_ConvertsBodyBeforeUpstream is the
// integration-style assertion: a fake upstream records the body it
// received, and we verify the body was already in Anthropic shape
// (system at top-level, no chat-style "messages" with role:system).
func TestChatHandler_OpenAIToAnthropic_ConvertsBodyBeforeUpstream(t *testing.T) {
	upstreamBody := []byte(`{
        "id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M2.7",
        "content":[{"type":"text","text":"hello"}],
        "usage":{"input_tokens":2,"output_tokens":1},
        "stop_reason":"end_turn"
    }`)

	bodyBytes := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":256,
        "messages":[
            {"role":"system","content":"you are a poet"},
            {"role":"user","content":"hi"}
        ]
    }`)

	cands := []provider.Candidate{{Protocol: "anthropic-messages"}}
	converted, err := selectChatUpstreamBodyBytes(cands, bodyBytes)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatalf("converted body not valid JSON: %v", err)
	}
	if got["system"] != "you are a poet" {
		t.Errorf("system should be top-level in Anthropic-format body; got %v", got["system"])
	}
	msgs := got["messages"].([]any)
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] == "system" {
			t.Errorf("role:system must not appear in Anthropic messages[]; got %v", mm)
		}
	}
	_ = upstreamBody
}

// TestExtractBearerToken_Bearer covers the primary path: Anthropic-style
// clients sending Authorization: Bearer <key> (or lowercase variant).
func TestExtractBearerToken_Bearer(t *testing.T) {
	for _, prefix := range []string{"Bearer ", "bearer "} {
		req := httptest.NewRequest("POST", "/v1/messages", nil)
		req.Header.Set("Authorization", prefix+"sk-test-123")
		if got := extractBearerToken(req); got != "sk-test-123" {
			t.Errorf("Authorization %q: got %q, want sk-test-123", prefix+"sk-test-123", got)
		}
	}
}

// TestExtractBearerToken_XAPIKey covers the fallback added for clients
// that follow the Anthropic SDK default of sending x-api-key without an
// Authorization header (which previously caused 401 at this gateway).
func TestExtractBearerToken_XAPIKey(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-xapi-456")
	if got := extractBearerToken(req); got != "sk-xapi-456" {
		t.Errorf("x-api-key header: got %q, want sk-xapi-456", got)
	}
}

// TestExtractBearerToken_AuthorizationWins ensures that when both headers
// are present, Authorization: Bearer takes precedence (matches the
// documented ordering).
func TestExtractBearerToken_AuthorizationWins(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-bearer-789")
	req.Header.Set("x-api-key", "sk-xapi-789")
	if got := extractBearerToken(req); got != "sk-bearer-789" {
		t.Errorf("Authorization should take precedence over x-api-key; got %q", got)
	}
}

// TestExtractBearerToken_Empty ensures an empty x-api-key is ignored so
// that SDKs which send the header unconditionally with an empty value
// still fall through to 401 instead of returning a bogus token.
func TestExtractBearerToken_Empty(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-api-key", "")
	if got := extractBearerToken(req); got != "" {
		t.Errorf("empty x-api-key should not be accepted; got %q", got)
	}
}
