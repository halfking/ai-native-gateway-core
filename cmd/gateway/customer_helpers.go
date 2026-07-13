package main

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// noAuthCustomerMiddleware returns an Echo middleware that explicitly performs
// NO authentication. It exists to:
//
//  1. Document the intent (these endpoints are intentionally public).
//  2. Provide a single hook point for future per-request protections such as
//     local-only IP filtering (X-Forwarded-For trust check) or basic rate
//     limiting, without touching the CustomerAPI handler code.
//
// The endpoints are mounted under /api/system/license/* and /api/system/upgrade/*,
// which are reserved by licensing/restricted_mode.go — they remain reachable
// even when the service is in restricted mode.
func noAuthCustomerMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			if slog.Default() != nil {
				slog.Debug("customer api",
					"path", c.Request().URL.Path,
					"method", c.Request().Method,
					"status", c.Response().Status,
					"latency_ms", time.Since(start).Milliseconds(),
				)
			}
			return err
		}
	}
}

// currentGatewayVersionProvider returns a VersionProvider closure that
// captures the running gateway's Version + BuildNumber ldflag-injected values.
//
// In tests this can be replaced with a fixed closure to make upgrade checks
// deterministic.
func currentGatewayVersionProvider() autoupdate.VersionProvider {
	return func() (string, int) {
		v := Version
		if v == "" {
			v = "dev"
		}
		seq := 0
		if BuildNumber != "" && BuildNumber != "0" {
			if n, err := strconv.Atoi(BuildNumber); err == nil {
				seq = n
			}
		}
		return v, seq
	}
}
