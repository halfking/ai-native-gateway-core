package sessions

import (
	"context"
	"log/slog"
	"net/http"
)

type contextKey string

const sessionContextKey contextKey = "session"

func SessionFromContext(ctx context.Context) *Session {
	if v := ctx.Value(sessionContextKey); v != nil {
		if s, ok := v.(*Session); ok {
			return s
		}
	}
	return nil
}

func SessionFromContextWith(ctx context.Context, s *Session) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionContextKey, s)
}

func WithSession(next http.Handler, manager *Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.Header.Get("X-Session-Id")
		if sessionID == "" {
			next.ServeHTTP(w, r)
			return
		}

		session, err := manager.Get(r.Context(), sessionID)
		if err == nil {
			ctx := context.WithValue(r.Context(), sessionContextKey, session)
			//nolint:errcheck // best-effort touch, non-critical
			go manager.Touch(context.Background(), sessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if err != ErrSessionNotFound {
			slog.Warn("legacy session lookup failed", "error", err, "session_id", sessionID)
			next.ServeHTTP(w, r)
			return
		}

		apiKeyID := GetAPIKeyIDFromContext(r.Context())
		tenantID := getTenantIDFromContext(r.Context())
		if apiKeyID == 0 {
			slog.Warn("legacy session fallback: no api key in context", "session_id", sessionID)
			next.ServeHTTP(w, r)
			return
		}

		deviceSeed := r.Header.Get("X-Device-Seed")
		if deviceSeed == "" {
			deviceSeed = r.Header.Get("X-Machine-Id")
		}
		if deviceSeed == "" {
			deviceSeed = "default"
		}

		newSession, createErr := manager.CreateV2(r.Context(), apiKeyID, tenantID, deviceSeed, "default")
		if createErr != nil {
			slog.Error("legacy session fallback create failed", "error", createErr, "session_id", sessionID)
			next.ServeHTTP(w, r)
			return
		}

		slog.Warn("legacy X-Session-Id used, fallback created; migrate to X-Gw-Session-Id",
			"original_session_id", sessionID,
			"new_session_id", newSession.SessionID,
		)
		w.Header().Set("X-Gw-Session-Id-Resume", newSession.SessionID)
		w.Header().Set("Deprecation", "true")

		ctx := context.WithValue(r.Context(), sessionContextKey, newSession)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func SetAPIKeyID(ctx context.Context, apiKeyID int) context.Context {
	return context.WithValue(ctx, apiKeyIDContextKey{}, apiKeyID)
}

func GetAPIKeyIDFromContext(ctx context.Context) int {
	if v := ctx.Value(apiKeyIDContextKey{}); v != nil {
		if id, ok := v.(int); ok {
			return id
		}
	}
	return 0
}

type apiKeyIDContextKey struct{}

func SetTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDContextKey{}, tenantID)
}

func getTenantIDFromContext(ctx context.Context) string {
	if v := ctx.Value(tenantIDContextKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "default"
}

// GetTenantIDFromContext is the exported version of
// getTenantIDFromContext. Added for Track C C4 (2026-06-18):
// the async retry goroutine in routing/executor.go needs to
// read the tenant id from the request context to populate
// the pending entry's TenantID field for multi-tenant
// isolation.
func GetTenantIDFromContext(ctx context.Context) string {
	return getTenantIDFromContext(ctx)
}

type tenantIDContextKey struct{}