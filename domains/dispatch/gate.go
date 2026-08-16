// Package dispatch implements the multi-tier request-queue scheduling pipeline
// (model queue → credential queue → forwarder) with per-credential concurrency
// modes (concurrency / rpm / tpm / disabled) and decoupled executors.
//
// See docs/会话优化v2/57-多层队列调度架构设计方案.md for the full design.
//
// This package MUST NOT import domains/streaming/executors (cycle); it obtains
// routing/forwarding/model-resolution capabilities via callbacks (RouteFunc,
// ForwardFunc, ModelResolveFunc) supplied by the executor wiring.
package dispatch

import (
	"os"
	"sync"
	"sync/atomic"
)

// DispatchGateKey is the settings_kv / Spec key backing the dispatch_v2
// feature-flag. Mirrors ratelimit.RateLimitGateKey.
const DispatchGateKey = "dispatch_v2.enabled"

// ModelChangeGateKey controls pre-first-byte model failover. It remains off by
// default and may be changed live through the platform settings endpoint.
const ModelChangeGateKey = "dispatch_v2.allow_model_change"

var (
	// dispatchEnabled is the hot-path atomic cache of the dispatch_v2 gate.
	// Read on every Submit so the request path never touches the DB. Boot
	// sync from settings happens in cmd/gateway/main_settings.go; admin PUT
	// flips it live in admin/settings.go.
	dispatchEnabled    atomic.Bool
	modelChangeEnabled atomic.Bool

	// transitionMu protects transitionHandlers (mirrors ratelimit gate).
	transitionMu       sync.RWMutex
	transitionHandlers []func(enabled bool)
)

func init() {
	// Default ON per design (ADR-Disp-005). The boot sync may flip it OFF
	// if settings_kv says so; KILL_DISPATCH_V2=1 forces OFF via the settings
	// kill-switch mechanism at startup.
	dispatchEnabled.Store(true)
	modelChangeEnabled.Store(false)
}

// IsDispatchEnabled reports whether the V2 multi-tier dispatch pipeline is
// active. When false, the executor falls back to the synchronous candidate
// loop (executor.Execute).
func IsDispatchEnabled() bool {
	return dispatchEnabled.Load()
}

// IsModelChangeEnabled reports whether dispatch may ask autoroute for another
// model after all credentials under the current model are exhausted.
func IsModelChangeEnabled() bool {
	return modelChangeEnabled.Load() || os.Getenv("AUTO_ROUTE_FALLBACK_ENABLED") == "true"
}

// SetModelChangeEnabled updates the hot-reloadable platform setting. The
// legacy AUTO_ROUTE_FALLBACK_ENABLED=true override remains force-on.
func SetModelChangeEnabled(v bool) {
	modelChangeEnabled.Store(v)
}

// SetDispatchEnabled updates the dispatch_v2 gate. Transition handlers run
// after the store (mirrors ratelimit.SetRateLimitEnabled).
func SetDispatchEnabled(v bool) {
	prev := dispatchEnabled.Swap(v)
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
// transition. Mirrors ratelimit.RegisterTransitionHandler.
func RegisterTransitionHandler(h func(enabled bool)) {
	transitionMu.Lock()
	transitionHandlers = append(transitionHandlers, h)
	transitionMu.Unlock()
}
