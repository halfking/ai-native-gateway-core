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

// 2026-09-25: DB system keys (sk-selfcheck-*, owner_user='self-check-worker')
// are verified INSIDE the handler — after OriginMiddleware ran — so the
// middleware always degraded them to the untrusted strip path. The claimed
// X-LLM-Origin-* pair must nevertheless survive on ctx so
// ResolveOriginForSystemKey can re-run the trust decision later.
func TestOriginMiddleware_CapturesClaimForUntrustedDBKeyCaller(t *testing.T) {
	mw := NewOriginMiddleware()
	var stage, actor, claimedStage, claimedActor string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage = ContextOriginStage(r.Context())
		actor = ContextOriginActor(r.Context())
		claimedStage, _ = r.Context().Value(originClaimedStageKey).(string)
		claimedActor, _ = r.Context().Value(originClaimedActorKey).(string)
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-LLM-Origin-Stage", "self_check")
	r.Header.Set("X-LLM-Origin-Actor", "credential-selfcheck-worker")
	// no auth.owner_user on ctx — the DB-key passthrough shape
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r)

	if stage != "business" {
		t.Fatalf("expected degraded stage=business at middleware time, got %q", stage)
	}
	if actor != "" {
		t.Fatalf("expected no actor at middleware time for a stripped claim, got %q", actor)
	}
	if r.Header.Get("X-LLM-Origin-Stage") != "" {
		t.Fatalf("expected inbound header stripped, got %q", r.Header.Get("X-LLM-Origin-Stage"))
	}
	if claimedStage != "self_check" || claimedActor != "credential-selfcheck-worker" {
		t.Fatalf("expected claim captured on ctx, got stage=%q actor=%q", claimedStage, claimedActor)
	}
}

func TestResolveOriginForSystemKey(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, originClaimedStageKey, "self_check")
	ctx = context.WithValue(ctx, originClaimedActorKey, "credential-selfcheck-worker")

	// trusted DB owner + valid claim → claim wins verbatim
	stage, actor, ok := ResolveOriginForSystemKey(ctx, "self-check-worker")
	if !ok || stage != "self_check" || actor != "credential-selfcheck-worker" {
		t.Fatalf("trusted owner + claim: got ok=%v stage=%q actor=%q", ok, stage, actor)
	}

	// trusted DB owner, no claim → canonical owner mapping (server-side,
	// cannot be forged by the client)
	stage, actor, ok = ResolveOriginForSystemKey(context.Background(), "credential-selfcheck-worker")
	if !ok || stage != "self_check" || actor != "credential-selfcheck-worker" {
		t.Fatalf("trusted owner fallback: got ok=%v stage=%q actor=%q", ok, stage, actor)
	}

	// trusted owner with a stage claim that fails the CHECK vocabulary →
	// falls back to the mapping rather than trusting the unknown value
	spoofed := context.WithValue(context.Background(), originClaimedStageKey, "manual-spoof")
	stage, _, ok = ResolveOriginForSystemKey(spoofed, "node-probe-worker")
	if !ok || stage != "node_probe" {
		t.Fatalf("spoofed claim: got ok=%v stage=%q, want node_probe fallback", ok, stage)
	}

	// untrusted business owner → never resolvable
	if _, _, ok = ResolveOriginForSystemKey(ctx, "some-saas-customer"); ok {
		t.Fatalf("business owner must not resolve")
	}

	// static-key sentinel is handled inside resolveOrigin, out of scope here
	if _, _, ok = ResolveOriginForSystemKey(ctx, "global-auth-passed"); ok {
		t.Fatalf("global-auth-passed sentinel must not resolve via the DB-key path")
	}

	// legacy-probe-worker: trusted but intentionally unmapped → only an
	// explicit valid claim marks it; no claim → ok=false
	if _, _, ok = ResolveOriginForSystemKey(context.Background(), "legacy-probe-worker"); ok {
		t.Fatalf("legacy-probe-worker without claim must not resolve a fallback stage")
	}
	stage, _, ok = ResolveOriginForSystemKey(ctx, "legacy-probe-worker")
	if !ok || stage != "self_check" {
		t.Fatalf("legacy-probe-worker with claim: got ok=%v stage=%q", ok, stage)
	}
}
