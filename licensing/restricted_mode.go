package licensing

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// RestrictedModeMiddleware returns an Echo middleware that enforces restricted mode.
// In restricted mode, only health checks and license management endpoints are allowed.
// All other requests receive a 503 Service Unavailable response.
func RestrictedModeMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Request().URL.Path

			// Allow health check endpoint
			if path == "/api/healthz" || path == "/healthz" {
				return next(c)
			}

			// Allow license management endpoints
			if len(path) >= 19 && path[:19] == "/api/system/license" {
				return next(c)
			}

			// Block all other requests
			return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"error":   "license_required",
				"message": "Service is in restricted mode due to license verification failure. Please contact your administrator to resolve licensing issues.",
				"details": "Only /api/healthz and /api/system/license/* endpoints are available in restricted mode.",
			})
		}
	}
}
