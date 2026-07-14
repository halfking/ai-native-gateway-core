package licensing

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// Restricted-mode middleware (v2 hardening).
//
// Why this rewrite
// ----------------
// The v1 implementation used `len(path) >= 19 && path[:19] == "/api/system/license"`,
// which is logically equivalent to strings.HasPrefix — and HasPrefix matches
// any path that *starts with* the prefix even when the next char is unrelated
// (e.g. "/api/system/licenseAdmin"). Restricted mode is the *only* mode
// where this kind of bypass is reachable, so it would have effectively
// leaked admin endpoints during a license outage.
//
// The new check enforces a boundary character after the prefix:
// either end-of-string or "/". This matches the obvious intent
// ("license path begins with /api/system/license/"). Anything that
// merely *contains* the prefix (e.g. "/api/system/license/foo") also
// works because HasPrefix is satisfied by the "/" boundary.
//
// Health and license allow-lists remain string-matched (the literal
// equality is what the contract calls for). All other requests get
// 503 with a structured body so dashboards can detect "we are in
// restricted mode" without parsing free-text strings.
func RestrictedModeMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Request().URL.Path

			// 1. Allow exact-match health endpoints.
			if path == "/api/healthz" || path == "/healthz" {
				return next(c)
			}

			// 2. Allow customer-facing license, upgrade, and distribution endpoints.
			if allowed := customerPublicPathAllowed(path); allowed {
				return next(c)
			}

			// 3. Block everything else.
			return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"error":   "license_required",
				"message": "Service is in restricted mode due to license verification failure. Please contact your administrator to resolve licensing issues.",
				"details": "Only /api/healthz, /api/system/license/*, /api/system/upgrade/*, /api/downloads/*, /api/donations/*, and /api/public/offline-activation/* are available in restricted mode.",
			})
		}
	}
}

// customerPublicPathAllowed reports paths reachable before admin login or during
// restricted mode: license/upgrade flows plus public download & donation portal.
func customerPublicPathAllowed(path string) bool {
	prefixes := []string{
		"/api/system/license",
		"/api/system/upgrade",
		"/api/downloads",
		"/api/donations",
		"/api/public/offline-activation",
	}
	for _, prefix := range prefixes {
		if pathAllowedWithBoundary(path, prefix) {
			return true
		}
	}
	return false
}

// licensePathAllowed reports whether the path is a license management endpoint.
func licensePathAllowed(path string) bool {
	return pathAllowedWithBoundary(path, "/api/system/license")
}

func pathAllowedWithBoundary(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}
