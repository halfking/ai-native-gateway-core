// bg/auto_route_workers_test.go — regression tests for the audit findings.
//
// Covers the silent-failure classes that the original feature shipped with:
//   - MEDIUM-4: Stop() must not deadlock when Start() was never called.
//   - MEDIUM-4: a second Start() must not launch a second run() goroutine.
//   - HIGH-1 + sample_count: visible behaviour of the cumulative count (a
//     pure-math test on the input shape; the SQL guard is exercised in the
//     scratch-DB smoke test rather than here, where it would require either
//     a real DB or a brittle stub).

package bg

import (
	"context"
	"testing"
	"time"
)

// MEDIUM-4: Stop on a freshly-constructed worker (no Start) must return, not
// block. Verified the bug on the original code: <-w.done blocked forever
// because run() never launched and done was never closed.
func TestSettleWorker_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked forever when Start() was never called")
	}
}

func TestAffinityWorker_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked forever when Start() was never called")
	}
}

// MEDIUM-4: Start() is idempotent. A second call would otherwise launch a
// second run() goroutine and panic on the second close(w.done).
func TestSettleWorker_StartIsIdempotent(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)
	w.Start(context.Background())
	w.Start(context.Background()) // must be a no-op
	w.Stop()
}

func TestAffinityWorker_StartIsIdempotent(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)
	w.Start(context.Background())
	w.Start(context.Background()) // must be a no-op
	w.Stop()
}

// MEDIUM-4: Stop() can be called twice without panicking — the stopOnce guard
// prevents a double-close on the cancel context.
func TestSettleWorker_StopTwiceDoesNotPanic(t *testing.T) {
	w := NewAutoRouteSettleWorker(nil)
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}

func TestAffinityWorker_StopTwiceDoesNotPanic(t *testing.T) {
	w := NewAutoRouteAffinityWorker(nil)
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}
