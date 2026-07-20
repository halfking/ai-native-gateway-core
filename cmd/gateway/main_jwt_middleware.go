// JWT middleware helpers extracted from main() during the P0 main.go split.
//
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// These were originally inline closures inside main() that captured a local
// `jwtSecret` variable. They now take their dependencies as explicit
// parameters so they can live at package scope and be reused / tested.
package main

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// resolveJWTSecret returns the JWT secret to use for admin endpoints.
// Priority: envValue (if non-empty) → fallback. Centralised so the env
// precedence (LLM_GATEWAY_JWT_SECRET → cfg.SecretKey) is testable and not
// buried inside a closure in main().
func resolveJWTSecret(envValue, fallback string) string {
	if s := envValue; s != "" {
		return s
	}
	return fallback
}

// newJWTMiddleware returns an echo middleware that enforces a bearer-token
// JWT signed with secret. On success it injects user_id / tenant_id /
// username / role into the Echo context. On failure it short-circuits with
// 401 (or 503 if secret is unconfigured, signalling a misconfig to operators).
//
// Behaviour is identical to the inline closure that previously lived in main();
// only the dependency on a closure-captured `jwtSecret` was replaced by an
// explicit parameter so the function can be hoisted to package scope.
func newJWTMiddleware(secret string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if secret == "" {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "admin authentication is not configured"})
			}
			auth := c.Request().Header.Get("Authorization")
			if len(auth) < 7 || auth[:7] != "Bearer " {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			}
			tokenStr := auth[7:]
			claims, err := admin.VerifyToken(tokenStr, secret)
			if err != nil || claims.UserID <= 0 {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			}
			// 将用户信息注入 Echo context
			c.Set("user_id", claims.UserID)
			c.Set("tenant_id", claims.TenantID)
			c.Set("username", claims.Username)
			c.Set("role", claims.Role)
			return next(c)
		}
	}
}

// newRequireSuperAdminMiddleware returns an echo middleware that 403s when
// the role injected by newJWTMiddleware is anything other than "super_admin".
// Must be installed AFTER newJWTMiddleware in the chain.
func newRequireSuperAdminMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if role, _ := c.Get("role").(string); role != "super_admin" {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "super_admin required"})
			}
			return next(c)
		}
	}
}
