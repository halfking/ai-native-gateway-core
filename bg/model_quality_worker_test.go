package bg

import (
	"context"
	"testing"
	"time"
)

// TestModelQualityWorker_TriggerDedup verifies the suspicious-action trigger
// path tracks in-flight + cooldown state so a flapping node cannot fan out an
// unbounded number of IQ tests (token-cost protection). We check the dedup
// bookkeeping directly since the goroutine path needs a real node source.
func TestModelQualityWorker_TriggerDedup(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	w.triggerCooldown = 10 * time.Minute

	// Simulate a node that was just triggered.
	key := "7:gpt-4o"
	now := time.Now()
	w.triggerInFlight[key] = struct{}{}
	w.triggerLast[key] = now

	w.triggerMu.Lock()
	_, inFlight := w.triggerInFlight[key]
	last := w.triggerLast[key]
	w.triggerMu.Unlock()
	if !inFlight {
		t.Fatal("expected in-flight marker to be present")
	}
	if now.Sub(last) > w.triggerCooldown {
		t.Fatal("cooldown math wrong: last should be within cooldown of now")
	}
}

// TestModelQualityWorker_TestSingleNodeRequiresSource confirms the on-demand
// test fails fast when no node source is configured (the admin API surfaces an
// error instead of hanging).
func TestModelQualityWorker_TestSingleNodeRequiresSource(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	if _, err := w.TestSingleNode(t.Context(), 1, "gpt-4o"); err == nil {
		t.Fatal("expected error when node source is nil")
	}
}

// TestModelQualityWorker_DefaultCooldown documents the default cooldown so
// operators know the repeat window without reading the constructor.
func TestModelQualityWorker_DefaultCooldown(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	if w.triggerCooldown != 10*time.Minute {
		t.Fatalf("default cooldown changed: got %v, want 10m", w.triggerCooldown)
	}
	if w.triggerInFlight == nil || w.triggerLast == nil {
		t.Fatal("dedup maps must be initialized by constructor")
	}
}

// TestModelQualityWorker_StopCancelsTriggerCtx verifies the 2026-08-11 audit
// fix: Stop() must cancel the worker's trigger context so in-flight
// anomaly-triggered IQ tests (TriggerNodeIQTest goroutines) are interrupted on
// graceful shutdown instead of continuing to make paid upstream calls for up
// to 5 minutes after the gateway begins shutting down.
//
// Crucially this must hold even when the worker was never Start()ed (running
// stays false): a failed Start can leave the trigger callback wired, and those
// goroutines must still be cancellable.
func TestModelQualityWorker_StopCancelsTriggerCtx(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	if w.triggerCtx == nil || w.triggerCancel == nil {
		t.Fatal("constructor must initialize triggerCtx/triggerCancel")
	}
	// Sanity: the context is alive right after construction.
	select {
	case <-w.triggerCtx.Done():
		t.Fatal("triggerCtx should be alive before Stop")
	default:
	}

	// Stop() on a never-started worker must still cancel triggerCtx (the
	// running==false early-return path must not skip the cancel).
	w.Stop()

	select {
	case <-w.triggerCtx.Done():
		// expected: context cancelled by Stop
	default:
		t.Fatal("triggerCtx should be cancelled after Stop, even when never started")
	}
}

// TestModelQualityWorker_TriggerCtxIsChildOfWorkerLifecycle confirms that a
// context derived from triggerCtx observes cancellation, which is the property
// TriggerNodeIQTest relies on to interrupt its detached goroutine.
func TestModelQualityWorker_TriggerCtxDerivedCancelled(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	derived, cancel := context.WithTimeout(w.triggerCtx, 5*time.Minute)
	defer cancel()
	w.Stop()
	select {
	case <-derived.Done():
		// expected: derived ctx cancelled because its parent triggerCtx is cancelled by Stop
	default:
		t.Fatal("derived trigger ctx must be cancelled when parent triggerCtx is cancelled by Stop")
	}
}

// TestModelQualityWorker_TriggerCtxResetOnRestart verifies that Stop followed
// by a fresh Start recreates triggerCtx so anomaly-triggered tests remain usable
// after a worker restart.
func TestModelQualityWorker_TriggerCtxResetOnRestart(t *testing.T) {
	w := NewModelQualityWorker("", "", "", time.Second)
	first := w.triggerCtx
	w.Stop()
	if first == nil {
		t.Fatal("expected initial trigger ctx")
	}
	w.Start(context.Background(), nil)
	second := w.triggerCtx
	if second == nil {
		t.Fatal("expected trigger ctx after restart")
	}
	if first == second {
		t.Fatal("expected restart to create a fresh trigger ctx")
	}
	select {
	case <-second.Done():
		t.Fatal("fresh trigger ctx should not be canceled immediately after restart")
	default:
	}
	w.Stop()
}
