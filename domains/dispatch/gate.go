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
	"sync/atomic"
)

// AUDIT_24H B2b (2026-08-17): the dispatch_v2.enabled kill-switch was retired.
// The multi-tier pipeline is the only execute path — the legacy synchronous
// candidate loop in executor.Execute was deleted, so a gate that could "fall
// back" no longer had anything to fall back to. The dispatch_v2.enabled spec,
// boot sync, admin hot-reload branch and transition handlers went with it.
// Snapshot views keep a literal `Enabled: true` for admin UI compatibility.

// ModelChangeGateKey controls pre-first-byte model failover. It remains off by
// default and may be changed live through the platform settings endpoint.
const ModelChangeGateKey = "dispatch_v2.allow_model_change"

var (
	// modelChangeEnabled is the hot-path atomic cache of the allow_model_change
	// gate. Read on the dispatch failover path so requests never touch the DB.
	modelChangeEnabled atomic.Bool
)

func init() {
	modelChangeEnabled.Store(false)
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
