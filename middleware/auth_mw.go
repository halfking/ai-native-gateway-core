package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
)

type AuthMiddleware struct {
	BaseMiddleware
	expectedKey string
}

func NewAuthMiddleware(apiKey string) *AuthMiddleware {
	return &AuthMiddleware{
		BaseMiddleware: BaseMiddleware{
			name: "auth",
			bypass: BypassRule{
				ExactPaths: []string{"/healthz", "/metrics", "/"},
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
			writeAuthUnauthorized(w, "Missing or malformed Authorization header")
			return
		}
		provided := auth[7:]

		if subtle.ConstantTimeCompare([]byte(m.expectedKey), []byte(provided)) != 1 {
			slog.Warn("auth: invalid API key",
				"remote", r.RemoteAddr,
				"path", r.URL.Path,
			)
			writeAuthUnauthorized(w, "Invalid API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeAuthUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "authentication_error",
			"code":    "invalid_api_key",
		},
	})
}
