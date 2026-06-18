package bg

import (
	"context"
	"testing"
	"time"
)

// TestPendingSweeper_DefaultConfig pins the constructor fallback
// values. The production defaults (10m stale, 60s interval) are
// documented in the design doc; if a future change alters them,
// this test fails and the doc must be updated.
func TestPendingSweeper_DefaultConfig(t *testing.T) {
	s := NewPendingSweeper(nil, 0, 0)
	if s.staleTimeout != 10*time.Minute {
		t.Errorf("staleTimeout: got %v, want 10m", s.staleTimeout)
	}
	if s.interval != 60*time.Second {
		t.Errorf("interval: got %v, want 60s", s.interval)
	}
}

func TestPendingSweeper_CustomConfig(t *testing.T) {
	s := NewPendingSweeper(nil, 5*time.Minute, 30*time.Second)
	if s.staleTimeout != 5*time.Minute {
		t.Errorf("staleTimeout: got %v", s.staleTimeout)
	}
	if s.interval != 30*time.Second {
		t.Errorf("interval: got %v", s.interval)
	}
}

// TestPendingSweeper_StartStop_NilStore is a smoke test for the
// lifecycle: even with a nil store (which is a no-op in sweep()),
// Start and Stop must not deadlock or panic.
func TestPendingSweeper_StartStop_NilStore(t *testing.T) {
	s := NewPendingSweeper(nil, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	// Give the goroutine a moment to start.
	time.Sleep(10 * time.Millisecond)
	// Stop must return within a reasonable time.
	stopDone := make(chan struct{})
	go func() {
		s.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s")
	}
}

// TestPendingSweeper_NilStoreSweepIsNoOp pins the graceful
// contract: when the store is nil, sweep() must return cleanly
// (used by the cron tick to avoid log spam when pending is
// intentionally disabled).
func TestPendingSweeper_NilStoreSweepIsNoOp(t *testing.T) {
	s := NewPendingSweeper(nil, 0, 0)
	// Should not panic, should not block.
	s.sweep(context.Background())
}
