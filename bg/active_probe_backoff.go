// bg/active_probe_backoff.go — backoff schedule for error-triggered active probes
//
// The active probe workflow follows a fixed backoff chain
// (5s → 30s → 2m → 5m → 15m) when retrying after a failed direct probe.
// attempt 1 → 5s delay before retry
// attempt 2 → 30s delay before retry
// attempt 3 → 2m  delay before retry
// attempt 4 → 5m  delay before retry
// attempt 5 → 15m delay before retry
// attempt 6+ → 15m (capped)
//
// The chain is intentionally hard-coded (not exponential) so the dashboard
// can predict exactly when the next probe will fire and operators know the
// worst-case window before the workflow hands control back to the
// passive_probe_listener / credential_recovery goroutines.
package bg

import "time"

// DefaultErrorProbeBackoffChain is the default backoff chain (5s / 30s /
// 2m / 5m / 15m) for the active probe workflow.
//
// attempt index is 1-based; computeBackoff(1) returns the FIRST retry delay
// after the initial probe fails.
var DefaultErrorProbeBackoffChain = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// computeBackoff returns the delay that should pass BEFORE running the
// nextAttempt-th probe. nextAttempt is 1-based.
//
//	computeBackoff(1) → 5s   (first retry, almost immediate)
//	computeBackoff(2) → 30s
//	computeBackoff(3) → 2m
//	computeBackoff(4) → 5m
//	computeBackoff(5) → 15m
//	computeBackoff(6) → 15m  (capped, do not run further)
//
// Pure function: no side effects, easy to unit-test.
func computeBackoff(nextAttempt int) time.Duration {
	if nextAttempt < 1 {
		return DefaultErrorProbeBackoffChain[0]
	}
	if nextAttempt > len(DefaultErrorProbeBackoffChain) {
		return DefaultErrorProbeBackoffChain[len(DefaultErrorProbeBackoffChain)-1]
	}
	return DefaultErrorProbeBackoffChain[nextAttempt-1]
}

// computeBackoffForChain is the testable variant of computeBackoff that
// accepts an explicit chain. Production code calls computeBackoff (which
// uses DefaultErrorProbeBackoffChain); tests call this directly.
func computeBackoffForChain(nextAttempt int, chain []time.Duration) time.Duration {
	if len(chain) == 0 {
		return 0
	}
	if nextAttempt < 1 {
		return chain[0]
	}
	if nextAttempt > len(chain) {
		return chain[len(chain)-1]
	}
	return chain[nextAttempt-1]
}
