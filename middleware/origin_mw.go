// Package middleware — origin_mw.go
//
// OriginMiddleware populates request-context with the caller's origin
// metadata that the realtime stream / request_logs should display:
//
//   - origin_stage        : self_check | node_probe | system_health | business
//     (plus legacy probe_* values for backward
//     compatibility with rows written before
//     migration 341 by credential_probe_v2 /
//     model_probe / active_probe / passive_probe).
//   - origin_actor        : worker / actor name (see commit 4/5/6).
//   - client_ip           : real client IP (X-Real-IP > X-Forwarded-For[0]
//     > RemoteAddr, mirroring
//     telemetry/request_metadata.go:ExtractClientIP).
//   - client_forwarded_for: full X-Forwarded-For header chain (capped at
//     1024B to keep the request_logs row narrow).
//
// Header trust model
// ──────────────────
// The middleware ONLY honours X-LLM-Origin-Stage / X-LLM-Origin-Actor
// from requests that the auth middleware identified as system keys
// (is_system=TRUE && owner_user IN ('credential-selfcheck-worker',
// 'node-probe-worker', 'system-health-worker', 'legacy-probe-worker',
// 'model-quality-worker')).
// For every other request the inbound headers are stripped — otherwise
// a public client could send X-LLM-Origin-Stage=manual and bypass the
// daily self-check rate limit.  Auth runs in the same chain BEFORE this
// middleware (see cmd/gateway/main.go); if a future route skips auth it
// MUST not skip this middleware either.
//
// Wiring
// ──────
//
//	handler := middleware.NewBuilder().
//	    Add(middleware.NewRecoveryMiddleware()).
//	    Add(middleware.NewRequestIDMiddleware()).
//	    Add(middleware.NewLocaleMiddleware(...)).   // before auth
//	    Add(middleware.NewCORSMiddleware(...)).
//	    Add(middleware.NewPrometheusMiddleware()).
//	    Add(middleware.NewAuthMiddleware(...)).    // attaches OwnerUser
//	    Add(middleware.NewOriginMiddleware()).     // ← new (commit 3)
//	    Add(middleware.NewLoggingMiddleware()).
//	    Add(middleware.NewSecurityHeadersMiddleware()).
//	    Build().Then(mux)
//
// Readers
// ───────
// `telemetry.RequestLogEntry.ApplyOriginFromContext(ctx)` (commit 2)
// pulls the four values from ctx and writes them onto the entry, so
// the existing INSERT/UPDATE paths in telemetry/client.go carry the
// origin without needing to know about the middleware package.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// -------------------------------------------------------------------
// context keys
// -------------------------------------------------------------------
//
// Cross-package keys use string-typed context keys (Go stdlib
// convention) so the telemetry package can read origin values
// without importing this one (avoids an import cycle).
//
//	"origin.stage"          string — origin_stage value
//	"origin.actor"          string — origin_actor value
//	"origin.client_ip"      string — resolved client IP
//	"origin.xff"            string — full X-Forwarded-For chain
//	"auth.owner_user"       string — set by AuthMiddleware after key
//	                                verification; consumed here to
//	                                decide whether inbound X-LLM-Origin-*
//	                                headers are trustworthy.

const (
	originStageKey      = "origin.stage"
	originActorKey      = "origin.actor"
	originClientIPKey   = "origin.client_ip"
	originClientXFFKey  = "origin.xff"
	authOwnerUserCtxKey = "auth.owner_user"
)

// system-owner-user list. Auth middleware stores the resolved owner
// under "auth.owner_user" on ctx — when that value matches one of the
// names below, we trust inbound X-LLM-Origin-* headers; otherwise we
// strip them.
//
// "global-auth-passed" is the sentinel AuthMiddleware sets when the
// request carried the static global API key.  Auth_mw cannot read the
// DB to look up the real owner_user, so it cannot distinguish a
// genuine business user with that key from a system worker.  Instead
// we trust the X-LLM-Origin-Actor header when it names a known
// worker; an unknown / empty actor falls back to "business".
var trustedOriginOwners = map[string]struct{}{
	"global-auth-passed":          {}, // sentinel from AuthMiddleware
	"credential-selfcheck-worker": {},
	"node-probe-worker":           {},
	"system-health-worker":        {},
	"legacy-probe-worker":         {}, // tenant=system 5min cadence in 252
	"model-quality-worker":        {}, // 2026-08-10: MMLU 智商测试经网关请求
	"self-check-worker":           {}, // 2026-08-10: 修复潜伏的归属错误——legacy SelfCheckWorker 与 model-quality-worker 都复用这个系统 key
}

// -------------------------------------------------------------------
// egress IP injection
// -------------------------------------------------------------------
//
// Workers that POST to the local gateway should ship their own source
// IP in X-Real-IP / X-Forwarded-For so the receiving middleware can
// populate client_ip / client_forwarded_for correctly.  Two env vars
// (set on the worker process) configure this:
//
//	LLM_GATEWAY_EGRESS_IP                — single source IP for X-Real-IP
//	LLM_GATEWAY_EGRESS_FORWARDED_FOR     — comma-separated chain to append
//	                                        to X-Forwarded-For (excluding
//	                                        the egress IP itself, which
//	                                        is prepended automatically)
//
// On the server side we only set the headers if they're not already
// present — that way callers can override (e.g. e2e tests).
var (
	egressEnvOnce      sync.Once
	egressIP           string
	egressForwardedFor string
)

func loadEgressEnv() {
	egressEnvOnce.Do(func() {
		egressIP = strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP"))
		egressForwardedFor = strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR"))
	})
}

// -------------------------------------------------------------------
// middleware
// -------------------------------------------------------------------

// OriginMiddleware reads inbound X-LLM-Origin-* headers + client IP
// chain, applies the trust policy above, and stuffs the result on the
// request context for downstream consumers (telemetry, loggers, etc).
type OriginMiddleware struct {
	BaseMiddleware
	// trustedProxies are the CIDR-allowed immediate peers from which
	// X-Forwarded-For / X-Real-IP are honoured. Requests from peers
	// outside this list fall back to RemoteAddr (see resolveClientIP).
	// nil means "trust nothing" — the safest baseline; deployers behind
	// a load balancer MUST extend it via Security.TrustedProxyCIDRs.
	trustedProxies []*net.IPNet
}

// NewOriginMiddleware constructs an OriginMiddleware with no trusted-proxy
// allowlist: X-Forwarded-For / X-Real-IP are ignored and the resolved client
// IP falls back to the immediate peer (RemoteAddr). Production wiring MUST
// use NewOriginMiddlewareWithTrustedProxies with the trusted CIDR list so
// that X-Forwarded-For / X-Real-IP from public clients cannot impersonate
// other tenants.
func NewOriginMiddleware() *OriginMiddleware {
	loadEgressEnv()
	return &OriginMiddleware{
		BaseMiddleware: BaseMiddleware{
			name: "origin",
			bypass: BypassRule{
				// Skip infra endpoints that don't produce request_logs rows.
				ExactPaths: []string{"/healthz", "/metrics", "/"},
			},
		},
	}
}

// NewOriginMiddlewareWithTrustedProxies builds the middleware with a
// pre-parsed allowlist. Callers SHOULD pre-validate the CIDRs (use
// ParseTrustedProxyCIDRs to convert from the YAML/env string slice).
func NewOriginMiddlewareWithTrustedProxies(cidrs []*net.IPNet) *OriginMiddleware {
	mw := NewOriginMiddleware()
	mw.trustedProxies = cidrs
	return mw
}

// ParseTrustedProxyCIDRs parses the raw config strings into *net.IPNet
// values. Invalid CIDRs are dropped with a slog.Warn so a single bad
// entry cannot poison the entire allowlist — but the rest still take
// effect. Returning a non-nil empty slice means "trust nothing"; nil
// means "trust nothing" as well (legacy constructor behaviour).
func ParseTrustedProxyCIDRs(raw []string) []*net.IPNet {
	if len(raw) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			slog.Warn("trusted proxy CIDR parse failed; entry ignored",
				"cidr", c, "err", err)
			continue
		}
		out = append(out, n)
	}
	return out
}

func (m *OriginMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.ShouldBypass(r) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		stage, actor, strip := m.resolveOrigin(r)
		clientIP, clientChain := m.resolveClientIP(r)

		// Inbound header sanitisation: never let a non-system caller
		// smuggle X-LLM-Origin-Stage through to the realtime stream.
		// X-LLM-Pin-Credential (2026-08-13) forces the router to a specific
		// credential; it MUST be honored only for trusted internal callers
		// (system self-check / node-probe), so strip it for everyone else to
		// prevent a public client from pinning routing to an arbitrary cred.
		if strip {
			r.Header.Del("X-LLM-Origin-Stage")
			r.Header.Del("X-LLM-Origin-Actor")
			r.Header.Del("X-LLM-Pin-Credential")
		}

		// 1024B cap on the chain so a malicious header cannot bloat
		// request_logs rows.  The IP column is a single INET and
		// stays short naturally.
		if len(clientChain) > 1024 {
			clientChain = clientChain[:1024]
		}

		ctx = context.WithValue(ctx, originStageKey, stage)
		ctx = context.WithValue(ctx, originActorKey, actor)
		ctx = context.WithValue(ctx, originClientIPKey, clientIP)
		ctx = context.WithValue(ctx, originClientXFFKey, clientChain)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveOrigin decides what origin_stage / origin_actor to attach.
// Returns the values + a bool indicating whether the inbound X-LLM-Origin-*
// headers must be stripped (true for any non-system caller).
func (m *OriginMiddleware) resolveOrigin(r *http.Request) (stage, actor string, strip bool) {
	// 1. Default: business request.
	strip = true
	stage = "business"
	actor = ""

	// 2. Trust check: only system-key callers may set their own stage.
	owner := authOwnerUser(r.Context())
	if _, ok := trustedOriginOwners[owner]; !ok {
		return stage, actor, strip
	}
	strip = false

	// 3. Honour inbound header if non-empty and well-formed.
	if v := strings.TrimSpace(r.Header.Get("X-LLM-Origin-Stage")); v != "" {
		if isValidOriginStage(v) {
			stage = v
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-LLM-Origin-Actor")); v != "" {
		// Actor is free-form (worker / manual:<id>); cap at 64B to
		// match the DB column width.
		if len(v) > 64 {
			v = v[:64]
		}
		actor = v
	}
	return stage, actor, strip
}

// isValidOriginStage matches the additive CHECK on request_logs.origin_stage
// (see migration 341). Unknown values fall back to "business" so the
// row still passes the CHECK and an operator can spot the spoof.
func isValidOriginStage(s string) bool {
	switch s {
	case "self_check", "node_probe", "system_health", "business",
		// legacy values kept valid by the additive CHECK.
		"probe_direct", "probe_v2", "model_probe", "passive_probe", "manual":
		return true
	}
	return false
}

// resolveClientIP extracts the real client IP and the full XFF chain.
//
// Priority (matches telemetry/request_metadata.go:ExtractClientIP):
//
//	X-Real-IP  >  X-Forwarded-For[0]  >  RemoteAddr
//
// Trust model (2026-08-29, HIGH security): X-Real-IP / X-Forwarded-For
// are ONLY honoured when the immediate TCP peer (r.RemoteAddr host)
// matches one of the CIDRs in m.trustedProxies. When no allowlist is
// configured (nil/empty), the headers are ignored and the caller falls
// back to RemoteAddr — preventing a public client from spoofing an IP
// to impersonate another tenant or bypass IP-based rate limits /
// audit trails. For the chain we keep the *original* X-Forwarded-For
// header value (after trimming whitespace) so operators can audit
// every proxy hop when the peer is trusted.
func (m *OriginMiddleware) resolveClientIP(r *http.Request) (single, chain string) {
	remoteHost, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)

	trusted := m.trustedProxies != nil && remoteIP != nil
	if trusted {
		for _, cidr := range m.trustedProxies {
			if cidr.Contains(remoteIP) {
				if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
					single = v
				}
				if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
					chain = v
					// If no X-Real-IP, fall back to the first hop.
					if single == "" {
						if i := strings.IndexByte(v, ','); i >= 0 {
							single = strings.TrimSpace(v[:i])
						} else {
							single = strings.TrimSpace(v)
						}
					}
				}
				break
			}
		}
	}
	if single == "" {
		single = remoteHost
	}
	// If a single value came from X-Real-IP but no XFF chain is
	// available, persist the single value as the chain too so the
	// (single, chain) tuple is never (a, "").
	if chain == "" && single != "" {
		chain = single
	}
	return single, chain
}

// -------------------------------------------------------------------
// context accessors
// -------------------------------------------------------------------

// ContextOriginStage returns the origin_stage value stored on ctx by
// OriginMiddleware, or "business" if absent.
func ContextOriginStage(ctx context.Context) string {
	if v, ok := ctx.Value(originStageKey).(string); ok && v != "" {
		return v
	}
	return "business"
}

// ContextOriginActor returns the origin_actor value stored on ctx, or
// empty string.
func ContextOriginActor(ctx context.Context) string {
	if v, ok := ctx.Value(originActorKey).(string); ok {
		return v
	}
	return ""
}

// ContextClientIP returns the resolved client IP, or empty.
func ContextClientIP(ctx context.Context) string {
	if v, ok := ctx.Value(originClientIPKey).(string); ok {
		return v
	}
	return ""
}

// ContextClientForwardedFor returns the full XFF chain, or empty.
func ContextClientForwardedFor(ctx context.Context) string {
	if v, ok := ctx.Value(originClientXFFKey).(string); ok {
		return v
	}
	return ""
}

// authOwnerUser returns the OwnerUser that AuthMiddleware stored on
// ctx.  Auth_mw.go (commit 3) sets ctx with key "auth.owner_user" via
// RegisterAuthOwnerUser — we read the same string key.
func authOwnerUser(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(authOwnerUserCtxKey).(string); ok {
		return v
	}
	return ""
}

// RegisterAuthOwnerUser lets the auth middleware install the owner
// user value on ctx in a way OriginMiddleware can read.  Calling
// pattern (in auth_mw.go, after key verification):
//
//	ctx = middleware.RegisterAuthOwnerUser(ctx, ownerUser)
//
// The returned ctx is the one that should be threaded forward via
// r.WithContext.
func RegisterAuthOwnerUser(ctx context.Context, ownerUser string) context.Context {
	if ownerUser == "" {
		return ctx
	}
	return context.WithValue(ctx, authOwnerUserCtxKey, ownerUser)
}

// IsGlobalAuthPassed reports whether AuthMiddleware authenticated the request
// with the deployed static data-plane key. The sentinel is intentionally
// private to middleware so downstream packages can only observe the decision,
// not forge the context value.
func IsGlobalAuthPassed(ctx context.Context) bool {
	return authOwnerUser(ctx) == "global-auth-passed"
}
