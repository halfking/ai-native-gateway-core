// Admin middleware wrappers extracted from main() during the P0 main.go split.
//
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// These were originally inline closures inside main() that captured local
// variables (pool / cfg.SecretKey). They now take their dependencies as
// explicit parameters so they can live at package scope.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// newWrapAdmin returns a closure that wraps an http.HandlerFunc with the
// admin JWT/API-key middleware. Used by every /api/admin/* and meta-tools
// route registered below. Behaviour is identical to the inline closure
// that previously lived in main().
func newWrapAdmin(pool *pgxpool.Pool, secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(fn http.HandlerFunc) http.HandlerFunc {
		return admin.AdminMiddleware(fn, pool, secret)
	}
}

// newWrapSessionAnalytics returns a closure that wraps a session-analytics
// handler with either admin middleware or session-service JWT middleware,
// depending on whether LLM_GATEWAY_SESSION_SERVICE_JWT_SECRET is set AND
// the session_service_auth.enabled platform setting is true.
//
// Priority:
//  1. If service JWT enabled + secret set → admin.SessionAnalyticsMiddleware
//  2. Otherwise → admin.AdminMiddleware (fallback)
//
// Behaviour is identical to the inline closure that previously lived in main().
func newWrapSessionAnalytics(pool *pgxpool.Pool, secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			serviceJWTEnabled := settings.GetPlatformBool("session_service_auth.enabled", false)
			serviceJWTSecret := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_SECRET"))
			if !serviceJWTEnabled || serviceJWTSecret == "" {
				if serviceJWTEnabled && serviceJWTSecret == "" {
					slog.Warn("session service JWT enabled but secret is missing; using admin middleware")
				}
				admin.AdminMiddleware(fn, pool, secret)(w, r)
				return
			}
			serviceJWTIssuer := os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_ISSUER")
			if serviceJWTIssuer == "" {
				serviceJWTIssuer = "ai-session-manager"
			}
			serviceJWTAudience := os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_AUDIENCE")
			if serviceJWTAudience == "" {
				serviceJWTAudience = "llm-gateway-session-analytics"
			}
			admin.SessionAnalyticsMiddleware(fn, pool, secret, serviceJWTSecret, serviceJWTIssuer, serviceJWTAudience)(w, r)
		}
	}
}

// newAdminMiddleware returns a closure that wraps a handler with admin
// JWT/API-key authentication. Thin wrapper used for the self-check API.
// Behaviour is identical to the inline closure that previously lived in main().
func newAdminMiddleware(pool *pgxpool.Pool, secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(fn http.HandlerFunc) http.HandlerFunc {
		return admin.AdminMiddleware(fn, pool, secret)
	}
}

// newSuperAdminMiddleware returns a closure that wraps a handler with
// admin super-admin-gated authentication. Thin wrapper used for the
// self-check API. Behaviour is identical to the inline closure that
// previously lived in main().
func newSuperAdminMiddleware(pool *pgxpool.Pool, secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(fn http.HandlerFunc) http.HandlerFunc {
		return admin.SuperAdminMiddleware(fn, pool, secret)
	}
}
