package ratelimit

import (
	"sync"
	"sync/atomic"
)

// RateLimitGateKey is the settings key for the gateway-wide rate-limit gate.
const RateLimitGateKey = "rate_limit.enabled"

var (
	enabled atomic.Bool

	// transitionMu protects transitionHandlers. Handlers run on every gate
	// change so callers can react (e.g. clear sticky caches).
	transitionMu       sync.RWMutex
	transitionHandlers []func(enabled bool)
)

func init() {
	enabled.Store(true)
}

// IsRateLimitEnabled reports whether gateway-side rate limiting is active.
func IsRateLimitEnabled() bool {
	return enabled.Load()
}

// SetRateLimitEnabled updates the gateway-wide rate-limit gate.
//
// On a true→false transition (gate disabled), transition handlers run
// after the atomic store so consumers can drop state that should not
// survive the gate-off interval (e.g. sticky session bindings).
func SetRateLimitEnabled(v bool) {
	prev := enabled.Swap(v)
	if prev == v {
		return
	}
	transitionMu.RLock()
	handlers := make([]func(bool), len(transitionHandlers))
	copy(handlers, transitionHandlers)
	transitionMu.RUnlock()
	for _, h := range handlers {
		h(v)
	}
}

// RegisterTransitionHandler registers a callback fired on every gate
// transition (true→false or false→true). The boolean argument is the
// new gate state. Handlers must not call RegisterTransitionHandler
// from within themselves.
func RegisterTransitionHandler(h func(enabled bool)) {
	transitionMu.Lock()
	transitionHandlers = append(transitionHandlers, h)
	transitionMu.Unlock()
}

func DisableRateLimit() { SetRateLimitEnabled(false) }

func EnableRateLimit() { SetRateLimitEnabled(true) }
