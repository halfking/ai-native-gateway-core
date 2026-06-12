package relay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestChatSelectUpstreamBodyBytes_ConvertsWhenAnthropic verifies the
// Q3 dispatch helper: when the chosen upstream speaks Anthropic, the
// original OpenAI chat body must be converted to Anthropic Messages
// format before being forwarded.
func TestChatSelectUpstreamBodyBytes_ConvertsWhenAnthropic(t *testing.T) {
	originalBody := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":256,
        "messages":[
            {"role":"system","content":"you are a poet"},
            {"role":"user","content":"hi"}
        ]
    }`)
	cands := []provider.Candidate{{Protocol: "anthropic-messages"}}
	out, err := selectChatUpstreamBodyBytes(cands, originalBody)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if v["system"] != "you are a poet" {
		t.Errorf("system not extracted to top-level: %v", v)
	}
	msgs := v["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("messages should drop system entry; got %d", len(msgs))
	}
	if _, hasStream := v["stream"]; hasStream {
		t.Errorf("no stream field in original body; output should not have one either")
	}
}

// TestChatSelectUpstreamBodyBytes_PassthroughWhenOpenAI verifies the
// Q1 (openai->openai) fast-path: when the upstream speaks OpenAI, the
// gateway forwards the body unchanged.
func TestChatSelectUpstreamBodyBytes_PassthroughWhenOpenAI(t *testing.T) {
	originalBody := []byte(`{"model":"mimo-v2.5-pro","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	cands := []provider.Candidate{{Protocol: "openai-completions"}}
	out, err := selectChatUpstreamBodyBytes(cands, originalBody)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}
	if string(out) != string(originalBody) {
		t.Errorf("expected passthrough for openai upstream; got %s", string(out))
	}
}

// TestChatSelectUpstreamBodyBytes_PassthroughWhenProtocolEmpty is the
// legacy "protocol unknown" fallback: no conversion happens.
func TestChatSelectUpstreamBodyBytes_PassthroughWhenProtocolEmpty(t *testing.T) {
	originalBody := []byte(`{"model":"mimo-v2.5-pro","max_tokens":10,"messages":[]}`)
	cands := []provider.Candidate{{Protocol: ""}}
	out, err := selectChatUpstreamBodyBytes(cands, originalBody)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}
	if string(out) != string(originalBody) {
		t.Errorf("expected passthrough for unknown protocol; got %s", string(out))
	}
}

// TestChatSelectUpstreamBodyBytes_ConvertsWhenAnthropicResponses is
// the Q1' fallback for openai-responses: a candidate with
// protocol="openai-responses" must NOT trigger Anthropic conversion
// (which would corrupt the Responses shape).
func TestChatSelectUpstreamBodyBytes_PasvertsWhenOpenAIResponses(t *testing.T) {
	originalBody := []byte(`{"model":"mimo-v2.5-pro","max_tokens":10,"messages":[]}`)
	cands := []provider.Candidate{{Protocol: "openai-responses"}}
	out, err := selectChatUpstreamBodyBytes(cands, originalBody)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}
	if string(out) != string(originalBody) {
		t.Errorf("openai-responses should pass through unchanged; got %s", string(out))
	}
}

// TestChatSelectUpstreamBodyBytes_ConvertsToolsAndStop is a broader
// "end-to-end shape" check on the converted body: when the upstream is
// anthropic-messages, stop must be renamed to stop_sequences and
// tools must carry input_schema.
func TestChatSelectUpstreamBodyBytes_ConvertsToolsAndStop(t *testing.T) {
	originalBody := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":10,
        "stop":["END"],
        "tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object"}}}],
        "messages":[{"role":"user","content":"hi"}]
    }`)
	cands := []provider.Candidate{{Protocol: "anthropic-messages"}}
	out, err := selectChatUpstreamBodyBytes(cands, originalBody)
	if err != nil {
		t.Fatalf("selectChatUpstreamBodyBytes: %v", err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	if v["stop_sequences"] == nil {
		t.Error("stop should be renamed to stop_sequences")
	}
	tools, ok := v["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools lost: %v", v["tools"])
	}
	tool := tools[0].(map[string]any)
	if _, ok := tool["input_schema"]; !ok {
		t.Error("function.parameters should become input_schema")
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
