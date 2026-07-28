// Package bg/systemmonitor — health_check_test.go
//
// Unit tests for the audit follow-up #1 wiring
// (docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.1):
// healthCheckLoop → RecoveryGate.MarkClosedDebounced on persistent
// Redis health failures.
//
// The tests drive checkRedisHealthOnce directly (the per-tick hook is
// package-private and exposed for exactly this reason) instead of
// waiting on the production 15s ticker.
package systemmonitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeRecoveryGate implements RecoveryGate and records every call so
// tests can assert on (count, reasons, ttl) without spinning up Redis.
type fakeRecoveryGate struct {
	mu           sync.Mutex
	calls        []fakeGateCall
	restoreCalls []fakeRestoreCall
	failAll      bool  // when true, every MarkClosedDebounced returns an error
	restoreErr   error // optional override for RestoreIfClosed
	restoreCount int   // what RestoreIfClosed should report
}

type fakeGateCall struct {
	reason      string
	debounceTTL time.Duration
}

type fakeRestoreCall struct{}

func (g *fakeRecoveryGate) MarkClosedDebounced(_ context.Context, reason string, debounceTTL time.Duration) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failAll {
		return false, errors.New("simulated redis failure")
	}
	g.calls = append(g.calls, fakeGateCall{reason: reason, debounceTTL: debounceTTL})
	return true, nil
}

func (g *fakeRecoveryGate) RestoreIfClosed(_ context.Context) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.restoreCalls = append(g.restoreCalls, fakeRestoreCall{})
	if g.restoreErr != nil {
		return 0, g.restoreErr
	}
	return g.restoreCount, nil
}

func (g *fakeRecoveryGate) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// newHealthCheckSystemMonitor constructs a SystemMonitor suitable for
// driving checkRedisHealthOnce. The ping stub is injected via
// SystemMonitor.pingFn; the rest of the SystemMonitor is unused on the
// health-check code path.
func newHealthCheckSystemMonitor(gate RecoveryGate, threshold int, debounceTTL time.Duration, ping func(ctx context.Context) error) *SystemMonitor {
	sm := &SystemMonitor{
		recoveryGate:          gate,
		recoveryFailThreshold: threshold,
		recoveryDebounceTTL:   debounceTTL,
		pingFn:                ping,
	}
	if threshold <= 0 {
		sm.recoveryFailThreshold = 1
	}
	if debounceTTL <= 0 {
		sm.recoveryDebounceTTL = time.Minute
	}
	return sm
}

// TestCheckRedisHealthOnce_ThresholdNotReached verifies that single
// transient ping failures do NOT trigger the recovery gate close.
// Audit invariant: one flaky ping must never close the gate.
func TestCheckRedisHealthOnce_ThresholdNotReached(t *testing.T) {
	gate := &fakeRecoveryGate{}
	sm := newHealthCheckSystemMonitor(gate, 3, time.Minute, func(_ context.Context) error {
		return errors.New("transient redis error")
	})

	// Two consecutive failures — under threshold of 3.
	sm.checkRedisHealthOnce(context.Background())
	sm.checkRedisHealthOnce(context.Background())

	if got := gate.callCount(); got != 0 {
		t.Fatalf("gate must not be called below threshold; got %d calls", got)
	}
	if sm.consecutiveFailures != 2 {
		t.Fatalf("consecutiveFailures = %d, want 2", sm.consecutiveFailures)
	}
}

// TestCheckRedisHealthOnce_TriggersAtThreshold verifies the recovery
// gate close fires exactly on the 3rd consecutive failure (default
// threshold), and that the gate is called with the documented
// ("redis_unavailable", debounce_ttl) arguments.
func TestCheckRedisHealthOnce_TriggersAtThreshold(t *testing.T) {
	gate := &fakeRecoveryGate{}
	sm := newHealthCheckSystemMonitor(gate, 3, 5*time.Minute, func(_ context.Context) error {
		return errors.New("redis unreachable")
	})

	for i := 0; i < 3; i++ {
		sm.checkRedisHealthOnce(context.Background())
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.calls) != 1 {
		t.Fatalf("gate must be called exactly once at threshold; got %d calls", len(gate.calls))
	}
	if got := gate.calls[0].reason; got != "redis_unavailable" {
		t.Fatalf("gate.reason = %q, want redis_unavailable", got)
	}
	if got := gate.calls[0].debounceTTL; got != 5*time.Minute {
		t.Fatalf("gate.debounceTTL = %v, want 5m", got)
	}
}

// TestCheckRedisHealthOnce_ResetOnSuccess verifies that a successful
// ping resets the consecutive-failure counter, so the next failure
// event starts from 0 again.
func TestCheckRedisHealthOnce_ResetOnSuccess(t *testing.T) {
	gate := &fakeRecoveryGate{}
	// pingFn flips between success and failure so we can drive the
	// transition through checkRedisHealthOnce without exposing internals.
	var healthy bool
	sm := newHealthCheckSystemMonitor(gate, 3, time.Minute, func(_ context.Context) error {
		if healthy {
			return nil
		}
		return errors.New("redis error")
	})

	// 2 failures → counter = 2, no gate call.
	healthy = false
	sm.checkRedisHealthOnce(context.Background())
	sm.checkRedisHealthOnce(context.Background())
	if sm.consecutiveFailures != 2 {
		t.Fatalf("after 2 failures consecutiveFailures = %d, want 2", sm.consecutiveFailures)
	}

	// 1 success → counter resets to 0.
	healthy = true
	sm.checkRedisHealthOnce(context.Background())
	if sm.consecutiveFailures != 0 {
		t.Fatalf("after recovery consecutiveFailures = %d, want 0", sm.consecutiveFailures)
	}

	// 2 more failures (back under threshold because counter reset).
	healthy = false
	sm.checkRedisHealthOnce(context.Background())
	sm.checkRedisHealthOnce(context.Background())
	if got := gate.callCount(); got != 0 {
		t.Fatalf("gate must not be called when counter resets; got %d calls", got)
	}
}

// TestCheckRedisHealthOnce_NilGateIsNoOp verifies that when RecoveryGate
// is not wired (default in tests / disabled in production), the health
// check still records the failure and toggles fallback mode without
// panicking.
func TestCheckRedisHealthOnce_NilGateIsNoOp(t *testing.T) {
	sm := newHealthCheckSystemMonitor(nil, 3, time.Minute, func(_ context.Context) error {
		return errors.New("redis error")
	})

	for i := 0; i < 5; i++ {
		sm.checkRedisHealthOnce(context.Background())
	}

	if sm.consecutiveFailures != 5 {
		t.Fatalf("counter = %d, want 5", sm.consecutiveFailures)
	}
	if !sm.IsFallback() {
		t.Fatalf("fallback must be entered on persistent redis failure")
	}
}

// TestCheckRedisHealthOnce_GateErrorDoesNotPanic verifies that a
// failing RecoveryGate.MarkClosedDebounced is swallowed (best-effort)
// and does not crash the health-check loop.
func TestCheckRedisHealthOnce_GateErrorDoesNotPanic(t *testing.T) {
	gate := &fakeRecoveryGate{failAll: true}
	sm := newHealthCheckSystemMonitor(gate, 2, time.Minute, func(_ context.Context) error {
		return errors.New("redis error")
	})

	// Drive past threshold — gate returns error, monitor must not panic
	// and the loop must continue.
	for i := 0; i < 5; i++ {
		sm.checkRedisHealthOnce(context.Background())
	}

	if sm.consecutiveFailures != 5 {
		t.Fatalf("counter = %d, want 5", sm.consecutiveFailures)
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.calls) != 0 {
		// failAll returns (false, error) — the calls slice stays empty
		// because the fake records calls only on success.
		t.Fatalf("fake recorded %d calls under failAll=true", len(gate.calls))
	}
}

// TestCheckRedisHealthOnce_TriggersRestoreOnRecovery verifies the
// audit follow-up #6 wiring: on the fallback → healthy transition,
// checkRedisHealthOnce calls RecoveryGate.RestoreIfClosed. This
// complements TestCheckRedisHealthOnce_TriggersAtThreshold (the close
// side) — together they implement the full incident lifecycle.
func TestCheckRedisHealthOnce_TriggersRestoreOnRecovery(t *testing.T) {
	gate := &fakeRecoveryGate{restoreCount: 7}
	sm := newHealthCheckSystemMonitor(gate, 3, time.Minute, func(_ context.Context) error {
		return errors.New("redis error")
	})

	// Drive past the close threshold so we enter fallback + auto-close.
	for i := 0; i < 3; i++ {
		sm.checkRedisHealthOnce(context.Background())
	}
	if !sm.IsFallback() {
		t.Fatalf("setup: monitor must be in fallback after 3 failures")
	}
	gate.mu.Lock()
	initialCloseCalls := len(gate.calls)
	gate.mu.Unlock()
	if initialCloseCalls != 1 {
		t.Fatalf("setup: expected 1 close call, got %d", initialCloseCalls)
	}

	// Now Redis recovers — flip the ping stub to success and drive one
	// more tick. The monitor must clear fallback AND call RestoreIfClosed.
	sm.pingFn = func(_ context.Context) error { return nil }
	sm.checkRedisHealthOnce(context.Background())

	if sm.IsFallback() {
		t.Fatalf("monitor must exit fallback after recovery")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.restoreCalls) != 1 {
		t.Fatalf("RestoreIfClosed must be called exactly once on recovery; got %d",
			len(gate.restoreCalls))
	}
}

// TestCheckRedisHealthOnce_RestoreErrorDoesNotPanic verifies that a
// failing RecoveryGate.RestoreIfClosed is swallowed (best-effort) and
// does not crash the health-check loop on the recovery transition.
func TestCheckRedisHealthOnce_RestoreErrorDoesNotPanic(t *testing.T) {
	gate := &fakeRecoveryGate{restoreErr: errors.New("simulated restore failure")}
	sm := newHealthCheckSystemMonitor(gate, 2, time.Minute, func(_ context.Context) error {
		return errors.New("redis error")
	})

	// Drive into fallback (2 failures to hit threshold of 2).
	sm.checkRedisHealthOnce(context.Background())
	sm.checkRedisHealthOnce(context.Background())
	if !sm.IsFallback() {
		t.Fatalf("setup: must be in fallback")
	}

	// Recover.
	sm.pingFn = func(_ context.Context) error { return nil }
	// Should not panic even though RestoreIfClosed returns an error.
	sm.checkRedisHealthOnce(context.Background())

	if sm.IsFallback() {
		t.Fatalf("fallback must clear even when restore fails")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.restoreCalls) != 1 {
		t.Fatalf("restore must have been attempted once; got %d", len(gate.restoreCalls))
	}
}
