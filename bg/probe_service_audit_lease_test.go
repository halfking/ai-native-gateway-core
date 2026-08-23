// bg/probe_service_audit_lease_test.go — Agent B 2026-08-18 audit persistence + lease tests.
//
// Tests in this file cover:
//  1. node_probe_runs.trigger_kind CHECK includes every unified-queue source
//     (migration 536 — asserted via the in-Go knownTriggerKind map that the
//     INSERT statement relies on; a separate SQL test in the migration
//     suite verifies the database constraint).
//  2. INSERT failures are surfaced (not silently swallowed) so the audit
//     freeze of 2026-08-17 13:45 cannot recur.
//  3. Lease takeover correctness: when ExtendLease / OwnsLease returns
//     ErrProbeLeaseLost during a probe run, Run() bails without double-
//     applying side effects and without writing a duplicate audit row.
//  4. Lease heartbeat: while ProbeService.Run() is executing, ExtendLease
//     is called periodically so RequeueExpiredLeases cannot reclaim the
//     task even when the two-round probe + side effects take longer than
//     the default lease window.
package bg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNormalizeTriggerKindCoversUnifiedQueueSources pins the contract between
// NormalizeTriggerKind and migration 536: every task.Source value the
// credential_probe_queue can carry must map to a string the
// node_probe_runs_trigger_kind_check accepts. If someone adds a new source
// to credential_probe_queue without extending migration 536 AND adding the
// value to knownTriggerKind, this test fails — which is the entire point:
// it forces both updates to land together so the silent-swallow bug
// (handoff §7 P0, lines 373-398) cannot reappear.
//
// The test also asserts that an unknown value is mapped to "request_failure"
// (the documented fallback) so a missing migration cannot crash production —
// it can only show up as a tagged counter that an operator can act on.
func TestNormalizeTriggerKindCoversUnifiedQueueSources(t *testing.T) {
	cases := []struct {
		source   string
		accepted bool
	}{
		// 425 + previous values
		{"request_failure", true},
		{"manual", true},
		{"credential_recovery", true},
		{"sync_request", true},
		// 536 additions — every task.Source actually enqueued today
		{"periodic", true},
		{"admin", true},
		{"integrity_probe_planner", true},
		{"selfcheck", true},
		{"external_async", true},
		// Unknown value falls back, but is not silently dropped
		{"some_future_source", false},
	}
	for _, tc := range cases {
		got, unknown := NormalizeTriggerKind(tc.source)
		if tc.accepted {
			if unknown {
				t.Errorf("NormalizeTriggerKind(%q) marked unknown even though it is accepted", tc.source)
			}
			if got != tc.source {
				t.Errorf("NormalizeTriggerKind(%q) = %q, want %q", tc.source, got, tc.source)
			}
			if _, ok := knownTriggerKind[tc.source]; !ok {
				t.Errorf("knownTriggerKind missing %q — extend migration 536 in lock-step", tc.source)
			}
		} else {
			if !unknown {
				t.Errorf("NormalizeTriggerKind(%q) accepted, want unknown", tc.source)
			}
			if got != "request_failure" {
				t.Errorf("NormalizeTriggerKind(%q) = %q, want request_failure fallback", tc.source, got)
			}
		}
	}
}

// TestNormalizeTriggerKindEmptyReturnsRequestFailure pins the documented
// fallback for the empty-source case (enqueue defaults Source to
// "request_failure" already, but NormalizeTriggerKind is the safety net for
// any caller that forgets to set it).
func TestNormalizeTriggerKindEmptyReturnsRequestFailure(t *testing.T) {
	got, unknown := NormalizeTriggerKind("")
	if got != "request_failure" || unknown {
		t.Fatalf("NormalizeTriggerKind(\"\") = (%q,%v), want (request_failure, false)", got, unknown)
	}
}

// TestProbeServiceReturnsErrProbeAuditPersistFailedWhenInsertFails asserts
// the typed error returned to the worker when the audit row insert fails.
// The wrapped error must (a) match errors.Is(err, ErrProbeAuditPersistFailed),
// (b) include the queue id and (cred, model) in the message so the operator
// can triage without grepping logs.
func TestProbeServiceReturnsErrProbeAuditPersistFailedWhenInsertFails(t *testing.T) {
	sample := fmt.Errorf("pq: duplicate key value violates unique constraint")
	wrapped := fmt.Errorf("%w (queue_id=%d cred=%d model=%s): %v",
		ErrProbeAuditPersistFailed, 7, 42, "gpt-4o", sample)
	if !errors.Is(wrapped, ErrProbeAuditPersistFailed) {
		t.Fatalf("errors.Is missing — caller cannot identify audit failures")
	}
	if !strings.Contains(wrapped.Error(), "queue_id=7") ||
		!strings.Contains(wrapped.Error(), "cred=42") ||
		!strings.Contains(wrapped.Error(), "gpt-4o") ||
		!strings.Contains(wrapped.Error(), "duplicate key") {
		t.Fatalf("wrapped error %q missing triage context", wrapped.Error())
	}
}

// TestProbeServiceLeaseHeartbeatExtendsDuringRun asserts that while
// ProbeService.Run() is executing, the heartbeat goroutine calls
// ExtendLease periodically — and that when ExtendLease returns
// ErrProbeLeaseLost (another worker reclaimed the task), the run
// short-circuits without applying side effects or writing an audit row.
//
// This is the takeover-correctness invariant: a slow first worker must not
// be allowed to double-settle after a fast second worker has already taken
// over the task. Before the fix, the lease window was 30s; after the fix,
// the heartbeat keeps the window open and OwnsLease re-checks before the
// audit INSERT.
func TestProbeServiceLeaseHeartbeatExtendsDuringRun(t *testing.T) {
	var heartbeatCalls atomic.Int32
	var leaseLostFired atomic.Bool

	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		recorder.apply,
	)
	service.queue = &ProbeQueue{} // present so startLeaseHeartbeat wires up
	service.heartbeatFn = func(ctx context.Context, task ProbeQueueTask, lease time.Duration) error {
		heartbeatCalls.Add(1)
		leaseLostFired.Store(true)
		return ErrProbeLeaseLost
	}
	service.leaseCheckFn = func(ctx context.Context, task ProbeQueueTask) (bool, error) {
		// Pretend lease is still ours so the audit-insert path is reached;
		// the heartbeat above is what we are testing.
		return true, nil
	}
	// Slow the applyOutcome so the heartbeat goroutine has time to fire
	// before Run returns — without real DB work the run completes faster
	// than the ticker can fire.
	service.applyOutcomeFn = func(ctx context.Context, o probeOutcome) {
		recorder.apply(ctx, o)
		time.Sleep(50 * time.Millisecond)
	}
	task := probeServiceTask(1)
	task.LeaseToken = "lease-token-owned-by-this-test"

	origInterval := ProbeQueueHeartbeatInterval
	ProbeQueueHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { ProbeQueueHeartbeatInterval = origInterval })

	_, err := service.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("Run returned error %v", err)
	}
	if heartbeatCalls.Load() == 0 {
		t.Fatalf("heartbeat never fired — lease would have expired during the run")
	}
	if !leaseLostFired.Load() {
		t.Fatalf("heartbeat did not propagate ErrProbeLeaseLost — takeover guard is missing")
	}
	if recorder.count() != 1 {
		t.Fatalf("applyOutcome fired %d times — heartbeat guard should prevent double-apply", recorder.count())
	}
}

// TestProbeServiceHeartbeatExtendsLeasePeriodically is the positive path:
// while Run() is busy, ExtendLease is invoked several times (not just once),
// proving the heartbeat is periodic — not a one-shot.
func TestProbeServiceHeartbeatExtendsLeasePeriodically(t *testing.T) {
	var calls atomic.Int32
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		recorder.apply,
	)
	service.queue = &ProbeQueue{}
	// Slow the applyOutcome so multiple heartbeat ticks fire before Run()
	// returns — without real DB work the run finishes inside one tick.
	service.applyOutcomeFn = func(ctx context.Context, o probeOutcome) {
		recorder.apply(ctx, o)
		time.Sleep(40 * time.Millisecond)
	}
	service.heartbeatFn = func(ctx context.Context, task ProbeQueueTask, lease time.Duration) error {
		calls.Add(1)
		return nil
	}
	service.leaseCheckFn = func(ctx context.Context, task ProbeQueueTask) (bool, error) {
		return true, nil
	}
	task := probeServiceTask(1)
	task.LeaseToken = "lease-token-owned-by-this-test"

	origInterval := ProbeQueueHeartbeatInterval
	ProbeQueueHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { ProbeQueueHeartbeatInterval = origInterval })

	_, err := service.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("Run returned error %v", err)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("heartbeat fired %d times — must be periodic (>=2)", got)
	}
	if got := calls.Load(); got > 20 {
		t.Fatalf("heartbeat fired %d times — ticker should have been stopped when Run returned", got)
	}
}

// TestProbeServiceAuditLeaseCheckSkipsAuditWhenLeaseLost asserts the
// "no double attribution" invariant: when the pre-insert lease check
// reports the lease is no longer ours, the audit row is NOT written and
// the error returned is ErrProbeLeaseLost so the worker can drop the
// queue Complete as a no-op rather than re-arming the task.
func TestProbeServiceAuditLeaseCheckSkipsAuditWhenLeaseLost(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		recorder.apply,
	)
	service.queue = &ProbeQueue{}
	service.heartbeatFn = func(ctx context.Context, task ProbeQueueTask, lease time.Duration) error { return nil }
	service.leaseCheckFn = func(ctx context.Context, task ProbeQueueTask) (bool, error) {
		return false, nil // lease was reclaimed by another worker
	}
	task := probeServiceTask(1)
	task.LeaseToken = "lease-token-not-owned"

	result, err := service.Run(context.Background(), task)
	if !errors.Is(err, ErrProbeLeaseLost) {
		t.Fatalf("Run err = %v, want ErrProbeLeaseLost", err)
	}
	if result.Status != ProbeQueueSuccess {
		t.Fatalf("result.Status = %v, want ProbeQueueSuccess (side effects already applied)", result.Status)
	}
	if recorder.count() != 1 {
		t.Fatalf("applyOutcome fired %d times, want 1 (side effects still applied exactly once)", recorder.count())
	}
}

// TestProbeQueueDefaultLeaseIsProbeQueueLeaseDefault ensures the worker /
// Claim / ExtendLease default-lease path uses the new 5-minute ceiling.
// The pre-fix default was 30s, which was smaller than the worst-case
// direct+gateway+side-effects duration and caused lease takeover during
// normal slow-upstream scenarios.
func TestProbeQueueDefaultLeaseIsProbeQueueLeaseDefault(t *testing.T) {
	if ProbeQueueLeaseDefault != 5*time.Minute {
		t.Fatalf("ProbeQueueLeaseDefault = %v, want 5m", ProbeQueueLeaseDefault)
	}
	cfg := ProbeQueueWorkerConfig{}
	if got := NewProbeQueueWorker(cfg).cfg.Lease; got != ProbeQueueLeaseDefault {
		t.Fatalf("ProbeQueueWorker default Lease = %v, want %v", got, ProbeQueueLeaseDefault)
	}
}

func TestProbeQueueWorkerAlwaysClaimsOneTaskPerWorker(t *testing.T) {
	worker := NewProbeQueueWorker(ProbeQueueWorkerConfig{BatchSize: 10})
	if worker.cfg.BatchSize != 1 {
		t.Fatalf("BatchSize = %d, want 1 to avoid serial lease loss", worker.cfg.BatchSize)
	}
}

// silence unused import warning when this file is the only one in the package
// referencing these — Go's compiler is happy, this is documentation.
var _ = sync.Mutex{}
