package ipblocklist

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// EchoMiddleware rejects blocked client IPs before handlers run.
func EchoMiddleware(svc *Service, scope string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if svc == nil || svc.Cache == nil {
				return next(c)
			}
			ip := NormalizeClientIP(c.Request().RemoteAddr, c.Request().Header.Get("X-Forwarded-For"))
			blocked, entry, err := svc.Cache.IsBlocked(c.Request().Context(), ip, scope)
			if err != nil {
				slog.Warn("ip blocklist check failed", "error", err)
				return next(c)
			}
			if blocked {
				if entry != nil && entry.ID > 0 {
					_ = svc.Store.IncrementHit(c.Request().Context(), entry.ID)
				}
				return echo.NewHTTPError(http.StatusForbidden, "ip blocked")
			}
			return next(c)
		}
	}
}

// HTTPMiddleware is the net/http variant for gateway admin routes.
func HTTPMiddleware(svc *Service, scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if svc == nil || svc.Cache == nil {
				next.ServeHTTP(w, r)
				return
			}
			ip := NormalizeClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
			blocked, entry, err := svc.Cache.IsBlocked(r.Context(), ip, scope)
			if err != nil {
				slog.Warn("ip blocklist check failed", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if blocked {
				if entry != nil && entry.ID > 0 {
					_ = svc.Store.IncrementHit(r.Context(), entry.ID)
				}
				http.Error(w, `{"error":"ip blocked"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RecordAuthFailure tracks abusive IPs after rejected registration/collect calls.
func RecordAuthFailure(svc *Service, r *http.Request, scope, reason string) {
	if svc == nil || svc.Tracker == nil || r == nil {
		return
	}
	ip := NormalizeClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	if ip == nil {
		return
	}
	if err := svc.Tracker.RecordFailure(r.Context(), ip.String(), scope, reason); err != nil {
		slog.Warn("attack tracker record failed", "error", err, "ip", ip.String())
	}
}

// RecordEchoAuthFailure is the echo.Context variant.
func RecordEchoAuthFailure(svc *Service, c echo.Context, scope, reason string) {
	if c == nil {
		return
	}
	RecordAuthFailure(svc, c.Request(), scope, reason)
}
