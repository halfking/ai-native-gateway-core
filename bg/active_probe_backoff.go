// bg/active_probe_backoff.go — backoff schedule for error-triggered active probes
//
// DEPRECATED: all backoff definitions live in bg/probe_backoff.go.
// This file is kept as a thin compatibility shim.
//
// The active probe workflow follows a fixed backoff chain
// (5s → 30s → 2m → 5m → 15m) when retrying after a failed direct probe.
// attempt 1 → 5s delay before retry
// attempt 2 → 30s delay before retry
// attempt 3 → 2m  delay before retry
// attempt 4 → 5m  delay before retry
// attempt 5 → 15m delay before retry
// attempt 6+ → 15m (capped)
package bg

import "time"

// DefaultErrorProbeBackoffChain references the centralized definition.
var DefaultErrorProbeBackoffChain = ActiveProbeBackoffChain

// computeBackoff returns the delay after `failures` consecutive failures.
// Delegates to the unified ChainBackoffIndex in probe_backoff.go.
func computeBackoff(failures int) time.Duration {
	return ChainBackoffIndex(failures, ActiveProbeBackoffChain)
}

// computeBackoffForChain is the testable variant.
func computeBackoffForChain(failures int, chain []time.Duration) time.Duration {
	return ChainBackoffIndex(failures, chain)
}
