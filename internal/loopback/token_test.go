package loopback

import (
	"net/http/httptest"
	"regexp"
	"testing"
)

// TestTokenStableAndUnforgeable pins the per-boot token contract: generated
// once, stable for the process lifetime, 256-bit hex.
func TestTokenStableAndUnforgeable(t *testing.T) {
	first := Token()
	if first == "" {
		t.Fatal("Token() returned empty string")
	}
	if second := Token(); second != first {
		t.Fatalf("Token() changed across calls: %q vs %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("Token() = %q, want 64 hex chars (32 bytes)", first)
	}
}

func TestStripUntrustedCorrelationHeaders(t *testing.T) {
	// A request with forged headers and no token: everything stripped.
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Is-Auto", "true")
	req.Header.Set("X-Gw-Source-Actor", "goal-audit")
	req.Header.Set("X-Gw-Parent-Request-Id", "req-parent")
	req.Header.Set(TokenHeader, "forged-by-attacker")

	stripped := StripUntrustedCorrelationHeaders(req)
	if len(stripped) != 3 {
		t.Fatalf("stripped = %v, want all 3 correlation headers", stripped)
	}
	for _, h := range append(CorrelationHeaders, TokenHeader) {
		if got := req.Header.Get(h); got != "" {
			t.Errorf("header %s survived stripping: %q", h, got)
		}
	}

	// A loopback request carrying the real token: correlation preserved,
	// token itself still removed (never leaks upstream or into logs).
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req2.Header.Set("X-Gw-Is-Auto", "true")
	req2.Header.Set("X-Gw-Source-Actor", "auto-title-generator")
	req2.Header.Set(TokenHeader, Token())

	if got := StripUntrustedCorrelationHeaders(req2); got != nil {
		t.Fatalf("tokened loopback should keep correlation headers, stripped %v", got)
	}
	if req2.Header.Get("X-Gw-Is-Auto") != "true" || req2.Header.Get("X-Gw-Source-Actor") != "auto-title-generator" {
		t.Error("tokened loopback lost its correlation headers")
	}
	if req2.Header.Get(TokenHeader) != "" {
		t.Error("token header must be deleted even on valid loopback requests")
	}

	// A plain client request with no gateway headers at all: nothing to
	// report (callers skip logging).
	req3 := httptest.NewRequest("GET", "/v1/models", nil)
	if got := StripUntrustedCorrelationHeaders(req3); got != nil {
		t.Fatalf("plain request reported stripped headers: %v", got)
	}
}
