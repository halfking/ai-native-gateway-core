package main

import (
	"net/http"
	"sync"
	"time"

	"log/slog"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// customerRateLimiter is a token-bucket per remote IP for the
// unauthenticated customer surface (activation, status, heartbeat,
// offline request). It is intentionally coarse — its job is to blunt
// brute-force activation attempts and device-slot exhaustion rather
// than provide SLA-grade shaping. Production deployments that need
// stricter shaping should put a network-layer rate limit in front of
// the gateway.
type customerRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*customerBucket
	rate    int           // tokens added per window
	window  time.Duration // window length
	burst   int           // initial bucket capacity
	pruneAt time.Time
}

// customerBucket is one in-memory token bucket. refill tracks fractional
// tokens so we don't accumulate rounding drift on slow consumers.
type customerBucket struct {
	tokens float64
	last   time.Time
}

// newCustomerRateLimiter configures permissive but non-zero defaults
// so a misconfigured deployment cannot accidentally DOS its own
// activation flow. Defaults: 10 requests / second / IP, burst 20.
func newCustomerRateLimiter() *customerRateLimiter {
	return &customerRateLimiter{
		buckets: map[string]*customerBucket{},
		rate:    10,
		window:  time.Second,
		burst:   20,
		pruneAt: time.Now().Add(5 * time.Minute),
	}
}

// allow returns true if the IP may issue another request right now
// and decrements a token. Stale buckets are pruned periodically to
// bound memory.
func (l *customerRateLimiter) allow(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &customerBucket{tokens: float64(l.burst), last: now}
		l.buckets[ip] = b
	} else {
		elapsed := now.Sub(b.last)
		if elapsed > 0 {
			refill := float64(l.rate) * (float64(elapsed) / float64(l.window))
			b.tokens += refill
			if b.tokens > float64(l.burst) {
				b.tokens = float64(l.burst)
			}
			b.last = now
		}
	}
	// Periodic prune to keep the map bounded across long uptimes.
	if now.After(l.pruneAt) {
		for k, v := range l.buckets {
			if now.Sub(v.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.pruneAt = now.Add(5 * time.Minute)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens -= 1
	return true
}

var sharedCustomerLimiter = newCustomerRateLimiter()

// clientIPFromEchoRequest extracts the originating client IP, falling back
// to RemoteAddr. The trusted-proxy hop count is intentionally small: a
// correct network deployment terminates X-Forwarded-For at the gateway
// edge.
func clientIPFromEchoRequest(c echo.Context) string {
	xff := c.Request().Header.Get("X-Forwarded-For")
	if xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	return c.RealIP()
}

// noAuthCustomerMiddleware returns an Echo middleware that explicitly performs
// NO authentication, but applies a per-IP rate limit and structured request
// logging. It exists to:
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
			ip := clientIPFromEchoRequest(c)
			if !sharedCustomerLimiter.allow(ip) {
				c.Response().Header().Set("Retry-After", "1")
				return c.JSON(http.StatusTooManyRequests, map[string]string{
					"error": "too many requests, slow down",
				})
			}
			err := next(c)
			if slog.Default() != nil {
				slog.Debug("customer api",
					"path", c.Request().URL.Path,
					"method", c.Request().Method,
					"status", c.Response().Status,
					"client_ip", ip,
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
