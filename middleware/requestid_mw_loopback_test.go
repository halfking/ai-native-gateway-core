package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/loopback"
)

// TestRequestIDMiddleware_StripsUntrustedGatewayCorrelationHeaders pins the
// R35-R1 fix (R37): a client that forges X-Gw-Is-Auto / X-Gw-Source-Actor /
// X-Gw-Parent-Request-Id without this process's loopback token must reach
// the handler with those headers stripped — a forged X-Gw-Is-Auto otherwise
// removes the turn from the session_turns mirror and a forged
// X-Gw-Source-Actor:goal-% pollutes the goal shadow-round reconciliation.
func TestRequestIDMiddleware_StripsUntrustedGatewayCorrelationHeaders(t *testing.T) {
	var seen http.Header
	handler := NewRequestIDMiddleware().Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Is-Auto", "true")
	req.Header.Set("X-Gw-Source-Actor", "goal-audit")
	req.Header.Set("X-Gw-Parent-Request-Id", "forged-parent")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	for _, h := range loopback.CorrelationHeaders {
		if seen.Get(h) != "" {
			t.Errorf("forged header %s reached the handler: %q", h, seen.Get(h))
		}
	}
}

// TestRequestIDMiddleware_KeepsLoopbackTokenedCorrelationHeaders pins the
// other half: the internal auto-title/summary self-calls carry the per-boot
// token and must keep their correlation headers through the middleware.
func TestRequestIDMiddleware_KeepsLoopbackTokenedCorrelationHeaders(t *testing.T) {
	var seen http.Header
	handler := NewRequestIDMiddleware().Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Is-Auto", "true")
	req.Header.Set("X-Gw-Source-Actor", "auto-title-generator")
	req.Header.Set(loopback.TokenHeader, loopback.Token())
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.Get("X-Gw-Is-Auto") != "true" || seen.Get("X-Gw-Source-Actor") != "auto-title-generator" {
		t.Errorf("loopback correlation headers were stripped despite valid token: Is-Auto=%q Source-Actor=%q",
			seen.Get("X-Gw-Is-Auto"), seen.Get("X-Gw-Source-Actor"))
	}
	if seen.Get(loopback.TokenHeader) != "" {
		t.Error("loopback token must not reach handlers")
	}
}
