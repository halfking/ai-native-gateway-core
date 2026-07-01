package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"

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
			// SAFETY: every registered /api/* endpoint is wrapped by
			// wrapAdmin/superAdmin in cmd/gateway/main.go and
			// admin/handler.go. Verified 2026-06-30 via grep — see
			// docs/audit/2026-06-30-weekly-audit-report.md P0-3.
			bypass: BypassRule{
				ExactPaths:   []string{"/healthz", "/metrics", "/"},
				PathPrefixes: []string{"/api/"},
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

		if subtle.ConstantTimeCompare([]byte(m.expectedKey), []byte(provided)) != 1 {
			slog.Warn("auth: invalid API key",
				"remote", r.RemoteAddr,
				"path", r.URL.Path,
			)
			writeAuthUnauthorized(r.Context(), w, i18n.MsgInvalidKey, "invalid_key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeAuthUnauthorized emits the canonical 401 authentication-error envelope.
// messageKey is translated via i18n for the request's locale; code is the
// machine-readable token kept stable for SDKs.
func writeAuthUnauthorized(ctx context.Context, w http.ResponseWriter, messageKey, code string) {
	msg := i18n.T(ctx, messageKey)
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
