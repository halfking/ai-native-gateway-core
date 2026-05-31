package sessions

import (
	"context"
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
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, "", "invalid session", "session_error", "SESSION_INVALID")
			return
		}

		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		go manager.Touch(context.Background(), sessionID)

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

type tenantIDContextKey struct{}