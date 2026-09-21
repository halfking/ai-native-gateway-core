package middleware

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOriginMiddleware_StripsHeadersForBusiness verifies that a
// non-system caller (no auth.owner_user sentinel) cannot smuggle
// X-LLM-Origin-Stage through to downstream consumers; the middleware
// must overwrite inbound headers with the safe "business" default.
//
// 2026-08-29: also exercises the XFF-trust path — set RemoteAddr to
// loopback so the test continues to validate X-Forwarded-For/X-Real-IP
// passthrough behaviour from a trusted peer, in parallel with the stage
// spoof protection.
func TestOriginMiddleware_StripsHeadersForBusiness(t *testing.T) {
	mw := NewOriginMiddlewareWithTrustedProxies(
		ParseTrustedProxyCIDRs([]string{"127.0.0.1/32", "::1/128"}))
	var seen map[string]string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = map[string]string{
			"stage": ContextOriginStage(r.Context()),
			"ip":    ContextClientIP(r.Context()),
			"xff":   ContextClientForwardedFor(r.Context()),
		}
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	r.Header.Set("X-Real-IP", "198.51.100.7")
	r.Header.Set("X-LLM-Origin-Stage", "manual")
	r.Header.Set("X-LLM-Origin-Actor", "attacker")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if seen["stage"] != "business" {
		t.Fatalf("expected business override, got %q", seen["stage"])
	}
	if seen["ip"] != "198.51.100.7" {
		t.Fatalf("expected client ip from X-Real-IP, got %q", seen["ip"])
	}
	if seen["xff"] != "198.51.100.7, 10.0.0.1" {
		t.Fatalf("expected XFF chain, got %q", seen["xff"])
	}
	if r.Header.Get("X-LLM-Origin-Stage") != "" {
		t.Fatalf("expected inbound header to be stripped, got %q", r.Header.Get("X-LLM-Origin-Stage"))
	}
}

// TestOriginMiddleware_TrustsSystemKey verifies that when the static
// global key authenticated the request (sentinel "global-auth-passed"
// in ctx), the middleware honours inbound X-LLM-Origin-* headers.
func TestOriginMiddleware_TrustsSystemKey(t *testing.T) {
	mw := NewOriginMiddleware()
	var stage, actor string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage = ContextOriginStage(r.Context())
		actor = ContextOriginActor(r.Context())
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-LLM-Origin-Stage", "node_probe")
	r.Header.Set("X-LLM-Origin-Actor", "node-probe-worker")
	// simulate AuthMiddleware wiring the sentinel onto ctx
	ctx := RegisterAuthOwnerUser(r.Context(), "global-auth-passed")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))

	if stage != "node_probe" {
		t.Fatalf("expected stage=node_probe, got %q", stage)
	}
	if actor != "node-probe-worker" {
		t.Fatalf("expected actor=node-probe-worker, got %q", actor)
	}
}

// TestOriginMiddleware_RejectsUnknownStage ensures an attacker cannot
// set a custom origin_stage value that would slip past the DB CHECK.
func TestOriginMiddleware_RejectsUnknownStage(t *testing.T) {
	mw := NewOriginMiddleware()
	var stage string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage = ContextOriginStage(r.Context())
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-LLM-Origin-Stage", "totally-fake-stage")
	ctx := RegisterAuthOwnerUser(r.Context(), "global-auth-passed")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))

	if stage != "business" {
		t.Fatalf("expected fallback to business on unknown stage, got %q", stage)
	}
}

// TestOriginMiddleware_TruncatesLongXFF ensures the chain column never
// grows past 1024 bytes regardless of what the client sends.
func TestOriginMiddleware_TruncatesLongXFF(t *testing.T) {
	mw := NewOriginMiddleware()
	var chain string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain = ContextClientForwardedFor(r.Context())
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	long := make([]byte, 2048)
	for i := range long {
		long[i] = 'a'
	}
	r.Header.Set("X-Forwarded-For", string(long))
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if len(chain) > 1024 {
		t.Fatalf("expected XFF <= 1024B, got %d", len(chain))
	}
}

// TestRegisterAuthOwnerUser ensures the round-trip via ctx survives a
// subsequent context.WithValue chain.
func TestRegisterAuthOwnerUser(t *testing.T) {
	ctx := context.Background()
	ctx = RegisterAuthOwnerUser(ctx, "global-auth-passed")
	ctx = context.WithValue(ctx, "unrelated", "v")
	if authOwnerUser(ctx) != "global-auth-passed" {
		t.Fatalf("expected global-auth-passed, got %q", authOwnerUser(ctx))
	}
}

// TestOriginMiddleware_IgnoresXFFFromUntrustedPeer (HIGH security, 2026-08-29):
// when the immediate TCP peer is NOT on the trusted-proxy allowlist,
// X-Forwarded-For / X-Real-IP MUST be ignored and the middleware MUST
// fall back to RemoteAddr. Otherwise any public client can spoof an
// arbitrary source IP to impersonate another tenant or bypass
// IP-based rate limits / audit trails.
func TestOriginMiddleware_IgnoresXFFFromUntrustedPeer(t *testing.T) {
	cidrs := ParseTrustedProxyCIDRs([]string{"127.0.0.1/32", "::1/128"})
	mw := NewOriginMiddlewareWithTrustedProxies(cidrs)
	var seen map[string]string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = map[string]string{
			"ip":  ContextClientIP(r.Context()),
			"xff": ContextClientForwardedFor(r.Context()),
		}
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "203.0.113.10:54321" // public IP, NOT on the allowlist
	r.Header.Set("X-Forwarded-For", "8.8.8.8, 10.0.0.1")
	r.Header.Set("X-Real-IP", "8.8.8.8")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if seen["ip"] != "203.0.113.10" {
		t.Fatalf("expected client_ip from RemoteAddr (203.0.113.10), got %q", seen["ip"])
	}
	if seen["xff"] == "8.8.8.8, 10.0.0.1" {
		t.Fatalf("XFF chain from untrusted peer must NOT be honoured, got %q", seen["xff"])
	}
}

// TestOriginMiddleware_HonoursXFFFromTrustedPeer is the positive control:
// when the immediate peer IS on the allowlist, X-Forwarded-For / X-Real-IP
// MUST be honoured just like before the trust change.
func TestOriginMiddleware_HonoursXFFFromTrustedPeer(t *testing.T) {
	cidrs := ParseTrustedProxyCIDRs([]string{"127.0.0.1/32", "::1/128"})
	mw := NewOriginMiddlewareWithTrustedProxies(cidrs)
	var seen map[string]string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = map[string]string{
			"ip":  ContextClientIP(r.Context()),
			"xff": ContextClientForwardedFor(r.Context()),
		}
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:1234" // loopback, on the allowlist
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	r.Header.Set("X-Real-IP", "198.51.100.7")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if seen["ip"] != "198.51.100.7" {
		t.Fatalf("expected client_ip from X-Real-IP (198.51.100.7), got %q", seen["ip"])
	}
	if seen["xff"] != "198.51.100.7, 10.0.0.1" {
		t.Fatalf("expected XFF chain from trusted peer, got %q", seen["xff"])
	}
}

// TestOriginMiddleware_NilAllowlistIgnoresAllHeaders covers the default
// "trust nothing" behaviour — when the constructor is invoked without
// an allowlist (legacy NewOriginMiddleware path or a config that was
// misparsed into an empty slice), the middleware MUST still refuse to
// honour spoofable headers.
func TestOriginMiddleware_NilAllowlistIgnoresAllHeaders(t *testing.T) {
	mw := NewOriginMiddleware() // legacy: no allowlist
	var ip string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip = ContextClientIP(r.Context())
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Real-IP", "1.2.3.4")
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if ip != "10.0.0.1" {
		t.Fatalf("expected client_ip from RemoteAddr (10.0.0.1), got %q", ip)
	}
}

// TestParseTrustedProxyCIDRs covers CIDR parsing including invalid
// entries being dropped (rather than the whole allowlist being poisoned).
func TestParseTrustedProxyCIDRs(t *testing.T) {
	got := ParseTrustedProxyCIDRs([]string{
		"127.0.0.1/32",
		" 10.0.0.0/8 ",
		"",
		"not-a-cidr",
		"::1/128",
	})
	if len(got) != 3 {
		t.Fatalf("expected 3 valid CIDRs (invalid entries dropped), got %d", len(got))
	}
	loopback := net.ParseIP("127.0.0.1")
	if !got[0].Contains(loopback) {
		t.Fatalf("expected first CIDR to contain 127.0.0.1")
	}
}
