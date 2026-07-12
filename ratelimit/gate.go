package ratelimit

import "sync/atomic"

// RateLimitGateKey is the settings key for the gateway-wide rate-limit gate.
const RateLimitGateKey = "rate_limit.enabled"

var enabled atomic.Bool

func init() {
	enabled.Store(true)
}

// IsRateLimitEnabled reports whether gateway-side rate limiting is active.
func IsRateLimitEnabled() bool {
	return enabled.Load()
}

// SetRateLimitEnabled updates the gateway-wide rate-limit gate.
func SetRateLimitEnabled(v bool) {
	enabled.Store(v)
}

func DisableRateLimit() { SetRateLimitEnabled(false) }

func EnableRateLimit() { SetRateLimitEnabled(true) }
