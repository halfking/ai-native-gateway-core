package session

import (
	"context"
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
