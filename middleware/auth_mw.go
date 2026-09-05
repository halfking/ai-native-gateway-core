package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/i18n"
)

type AuthMiddleware struct {
	BaseMiddleware
	expectedKey string
}

func NewAuthMiddleware(apiKey string) *AuthMiddleware {
	return &AuthMiddleware{
		BaseMiddleware: BaseMiddleware{
			name: "auth",
			// Global API-key auth runs BEFORE mux routing, but admin handlers
			// registered under /api/* are independently wrapped with
			// admin.AdminMiddleware (Bearer JWT / cookie / API key) via
			// wrapAdmin in cmd/gateway/main.go. The /api/* prefix bypass
			// here lets cookie-authenticated browser sessions reach those
			// wrapped admin handlers without sending the global API key
			// (rule 20 §6.1 cookie compliance).
			//
			// /admin/* is the Vue SPA path prefix (e.g. /admin/turns). Nginx
			// often proxies location /admin to the Go process (for
			// /admin/config/reload), so SPA navigations would otherwise be
			// rejected as missing_key. /admin/config/reload still has its
			// own AdminTokenMiddleware after this bypass.
			//
			// SAFETY: every registered /api/* endpoint is wrapped by
			// wrapAdmin/superAdmin in cmd/gateway/main.go and
			// admin/handler.go. Verified 2026-06-30 via grep — see
			// docs/audit/2026-06-30-weekly-audit-report.md P0-3.
			bypass: BypassRule{
				// 2026-08-29：加 /readyz + /version。供 scripts/lifecycle/preflight.sh 三段检查使用。
				// /readyz 返回 DB+Redis 是否就绪（K8s readiness），/version 暴露 build metadata。
				// 两者均无敏感信息，必须 anon 可达。
				ExactPaths:   []string{"/healthz", "/healthz/full", "/readyz", "/version", "/metrics", "/"},
				PathPrefixes: []string{"/api/", "/admin/", "/assets/", "/maintain/", "/plugins/"},
			},
		},
		expectedKey: apiKey,
	}
}

func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	if m.expectedKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.ShouldBypass(r) {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if len(auth) < 7 || auth[:7] != "Bearer " {
			writeAuthUnauthorized(r.Context(), w, i18n.MsgMissingAuth, "missing_key")
			return
		}
		provided := auth[7:]

		// Exact-match on the deployed static key FIRST, even when it carries
		// an "sk-" prefix (deployments set LLM_GATEWAY_API_KEY=sk-gw* on
		// 154/245). Without this, the sk- branch below hands the static key
		// to the DB verifier, where it lands in tier "default" (12 RPM) —
		// every probe / self-check / ops caller then queues behind the
		// minute bucket, burning the caller's entire timeout budget
		// (observed 2026-08-26: kimi-k3 requests queued ~55-94s on 245 and
		// died with "context canceled" 502). Exact match cannot shadow
		// other users' keys — only the deployed key equal to expectedKey
		// ever takes this branch.
		if subtle.ConstantTimeCompare([]byte(m.expectedKey), []byte(provided)) == 1 {
			// Mark ctx with a sentinel that OriginMiddleware uses to decide
			// whether inbound X-LLM-Origin-Stage / X-LLM-Origin-Actor are
			// trustworthy. Also read by checkGatewayRateLimit to skip the
			// shared RPM bucket for the static data-plane key.
			ctx := RegisterAuthOwnerUser(r.Context(), "global-auth-passed")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Data-plane sk-* keys are NOT validated here. They are verified
		// per-request by domains/authentication.KeyVerifier against
		// api_keys (key_hash + enabled + status + expires_at). The static
		// gate must not shadow that check: with replicas holding different
		// LLM_GATEWAY_API_KEY values (each accidentally set to a user's
		// sk-key), the gate rejected every OTHER valid DB key with the
		// same "Invalid or expired API key" 401 as a real auth failure —
		// and nginx failover to the backup replica rejected the primary's
		// key during every deploy/restart window (2026-08-24 incident).
		// Rule 20 §2: /v1/* accepts sk-* via the DB verifier; the static
		// key only covers non-sk- internal callers (health probes,
		// self-check, deploy scripts) and the exact-match case above.
		if strings.HasPrefix(provided, "sk-") {
			next.ServeHTTP(w, r)
			return
		}

		// 2026-08-24: log a non-sensitive key prefix (same convention as
		// api_keys.key_prefix) so operators can tell WHICH misconfigured
		// internal caller is being rejected — the 2026-08-24 incident
		// needed cross-replica experiments to trace this gate.
		slog.Warn("auth: invalid API key",
			"remote", r.RemoteAddr,
			"path", r.URL.Path,
			"key_prefix", bearerKeyPrefixForLog(provided),
		)
		writeAuthUnauthorized(r.Context(), w, i18n.MsgInvalidKey, "invalid_key")
	})
}

// bearerKeyPrefixForLog returns a redacted prefix of a bearer token safe
// for structured logs: first 8 chars + "****" (mirrors api_keys.key_prefix).
// Never log the full token — invalid keys may still be near-valid secrets.
func bearerKeyPrefixForLog(key string) string {
	if len(key) <= 8 {
		return key + "****"
	}
	return key[:8] + "****"
}

// writeAuthUnauthorized emits the canonical 401 authentication-error envelope.
// messageKey is translated via i18n for the request's locale; code is the
// machine-readable token kept stable for SDKs.
func writeAuthUnauthorized(ctx context.Context, w http.ResponseWriter, messageKey, code string) {
	msg := i18n.T(ctx, messageKey)
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && strings.TrimSpace(requestID) != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "authentication_error",
			"code":    code,
		},
	})
}
