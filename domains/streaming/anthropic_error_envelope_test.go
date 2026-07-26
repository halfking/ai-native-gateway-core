package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteErrorAnthropic_EnvelopeShape guards the 2026-07-27 fix (E-1):
// Anthropic-protocol clients must receive the Anthropic error envelope
// {"type":"error","error":{"type":...,"message":...}} rather than the
// OpenAI shape {"error":{message,type,code,...}}, which Anthropic SDKs
// cannot type-check.
func TestWriteErrorAnthropic_EnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorAnthropic(
		rec,
		http.StatusServiceUnavailable,
		"req-abc",
		"No available provider for model 'gpt-4o'. All 3 candidates failed.",
		"server_error",
		"model_not_found",
		map[string]any{"stage": "execution"},
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type=%q want application/json", ct)
	}
	if rid := rec.Header().Get("X-Request-Id"); rid != "req-abc" {
		t.Fatalf("X-Request-Id=%q want req-abc", rid)
	}

	// Body must be the Anthropic shape.
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not valid Anthropic envelope JSON: %v body=%s", err, rec.Body.String())
	}
	if env.Type != "error" {
		t.Errorf("outer type=%q want \"error\"", env.Type)
	}
	if env.Error.Type == "" {
		t.Errorf("error.type empty; Anthropic SDKs require it. body=%s", rec.Body.String())
	}
	if !strings.Contains(env.Error.Message, "gpt-4o") {
		t.Errorf("error.message lost content: %q", env.Error.Message)
	}
	// code=model_not_found maps to Anthropic's not_found_error (the model the
	// client named is unavailable). A generic server_error/provider_error
	// without a model_not_found code would map to overloaded_error instead.
	if env.Error.Type != "not_found_error" {
		t.Errorf("error.type=%q want not_found_error for model_not_found", env.Error.Type)
	}
}

// TestAnthropicErrorType_Mapping verifies the OpenAI→Anthropic error-type
// mapping covers the restricted set Anthropic SDKs recognize.
func TestAnthropicErrorType_Mapping(t *testing.T) {
	cases := []struct {
		errType string
		code    string
		want    string
	}{
		{"rate_limit_error", "rate_limit_exceeded", "rate_limit_error"},
		{"", "rate_limit_error", "rate_limit_error"},
		{"authentication_error", "authentication_error", "authentication_error"},
		{"", "authentication_error", "authentication_error"},
		{"invalid_request_error", "context_length_exceeded", "invalid_request_error"},
		{"invalid_request_error", "unsupported_feature", "invalid_request_error"},
		{"", "insufficient_quota", "invalid_request_error"},
		{"server_error", "provider_error", "overloaded_error"},
		{"server_error", "model_not_found", "not_found_error"},
		{"internal_error", "internal_error", "overloaded_error"}, // fallback
	}
	for _, c := range cases {
		got := anthropicErrorType(c.errType, c.code)
		if got != c.want {
			t.Errorf("anthropicErrorType(%q,%q)=%q want %q", c.errType, c.code, got, c.want)
		}
	}
}

// TestProtocolOfRequest verifies path-based protocol detection.
func TestProtocolOfRequest(t *testing.T) {
	cases := map[string]string{
		"/v1/messages":         "anthropic",
		"/v1/messages/abc":     "anthropic",
		"/v1/chat/completions": "openai",
		"/v1/completions":      "openai",
		"/v1/responses":        "openai",
		"":                     "openai", // nil-safe default
	}
	for path, want := range cases {
		var req *http.Request
		if path != "" {
			req = httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		}
		if got := protocolOfRequest(req); got != want {
			t.Errorf("protocolOfRequest(%q)=%q want %q", path, got, want)
		}
	}
}
