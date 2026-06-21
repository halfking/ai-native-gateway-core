package relay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestChatHandler_OpenAIToAnthropic_ConvertsBodyBeforeUpstream verifies
// that the per-candidate conversion (via ConvertChatRequestToAnthropic)
// produces a valid Anthropic-format body when the client sends OpenAI
// shape. This tests the conversion function directly — the handler now
// passes raw bytes and the executor calls the callback per-candidate.
func TestChatHandler_OpenAIToAnthropic_ConvertsBodyBeforeUpstream(t *testing.T) {
	bodyBytes := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":256,
        "messages":[
            {"role":"system","content":"you are a poet"},
            {"role":"user","content":"hi"}
        ]
    }`)

	converted, err := ConvertChatRequestToAnthropic(bodyBytes)
	if err != nil {
		t.Fatalf("ConvertChatRequestToAnthropic: %v", err)
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

func TestClassifyStreamInterruption_BenignEOF(t *testing.T) {
	m := map[string]any{
		"stream_interrupted":    true,
		"failure_detail_code":   "eof_without_done",
		"stream_chunk_count":    7,
		"stream_done_received":  false,
	}
	isErr, detail := classifyStreamInterruption(m)
	if isErr {
		t.Error("benign EOF (eof_without_done + chunks>0) should NOT be classified as error")
	}
	if detail != "eof_without_done" {
		t.Errorf("expected detail 'eof_without_done', got %q", detail)
	}
}

func TestClassifyStreamInterruption_BenignEOF_ZeroChunks(t *testing.T) {
	m := map[string]any{
		"stream_interrupted":  true,
		"failure_detail_code": "eof_without_done",
		"stream_chunk_count":  0,
	}
	isErr, _ := classifyStreamInterruption(m)
	if !isErr {
		t.Error("eof_without_done with 0 chunks IS a real error (no content delivered)")
	}
}

func TestClassifyStreamInterruption_ClientCancel(t *testing.T) {
	for _, reason := range []string{"client_cancel", "client_disconnected"} {
		m := map[string]any{
			"stream_interrupted":  true,
			"failure_detail_code": reason,
			"stream_chunk_count":  5,
		}
		isErr, detail := classifyStreamInterruption(m)
		if isErr {
			t.Errorf("reason=%q should NOT be classified as error", reason)
		}
		if detail != reason {
			t.Errorf("expected detail %q, got %q", reason, detail)
		}
	}
}

func TestClassifyStreamInterruption_RealErrors(t *testing.T) {
	for _, reason := range []string{"stream_timeout", "stream_error", "read_error"} {
		m := map[string]any{
			"stream_interrupted":  true,
			"failure_detail_code": reason,
			"stream_chunk_count":  10,
		}
		isErr, detail := classifyStreamInterruption(m)
		if !isErr {
			t.Errorf("reason=%q with chunks>0 SHOULD be classified as error", reason)
		}
		if detail != reason {
			t.Errorf("expected detail %q, got %q", reason, detail)
		}
	}
}

func TestClassifyStreamInterruption_UnknownReason(t *testing.T) {
	m := map[string]any{
		"stream_interrupted":  true,
		"failure_detail_code": "",
		"stream_chunk_count":  3,
	}
	isErr, _ := classifyStreamInterruption(m)
	if !isErr {
		t.Error("unknown/empty reason with chunks>0 should default to error")
	}
}

func TestSanitizeGwSessionHeader(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "spaces", in: "   ", want: ""},
		{name: "valid gw session", in: "gw_123", want: "gw_123"},
		{name: "trim valid gw session", in: "  gw_abc  ", want: "gw_abc"},
		{name: "plain uuid rejected", in: "44007f85-5199-4d61-a7ef-73f5419dcdff", want: ""},
		{name: "legacy random rejected", in: "sess-001", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeGwSessionHeader(tc.in); got != tc.want {
				t.Fatalf("sanitizeGwSessionHeader(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
