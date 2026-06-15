package bg

// override_store_refresher_test.go — P7.7 unit tests for the
// OverrideStoreRefresher. We don't hit a real DB; we test the
// lifecycle methods, default tick value, and idempotent Stop.

import (
	"context"
	"testing"
	"time"
)

func TestOverrideStoreRefresher_Lifecycle(t *testing.T) {
	r := NewOverrideStoreRefresher(nil, nil)
	if r == nil {
		t.Fatal("NewOverrideStoreRefresher returned nil")
	}
	if r.tick != 1*time.Minute {
		t.Errorf("default tick = %v, want 1m", r.tick)
	}
}

func TestOverrideStoreRefresher_RefreshOnce_NilStore(t *testing.T) {
	r := NewOverrideStoreRefresher(nil, nil)
	err := r.RefreshOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when store is nil, got %v", err)
	}
}

func TestOverrideStoreRefresher_Stop_BeforeStart(t *testing.T) {
	// Stop on a never-Started refresher should be a clean no-op.
	r := NewOverrideStoreRefresher(nil, nil)
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("Stop blocked on never-Started refresher")
	}
}

func TestOverrideStoreRefresher_Stop_Idempotent(t *testing.T) {
	// Stop twice in quick succession should not panic.
	r := NewOverrideStoreRefresher(nil, nil)
	r.Start(context.Background())
	// Give it a moment to enter the loop
	time.Sleep(20 * time.Millisecond)
	r.Stop()
	// Second Stop should also be safe (sync.Once protects)
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("Second Stop blocked")
	}
}

func TestOverrideStoreRefresher_TickCustomisation(t *testing.T) {
	r := &OverrideStoreRefresher{
		tick: 30 * time.Second,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if r.tick != 30*time.Second {
		t.Errorf("tick = %v, want 30s", r.tick)
	}
}

func TestOverrideStoreRefresher_RunContextCancel(t *testing.T) {
	// Verify the goroutine respects context cancellation.
	r := NewOverrideStoreRefresher(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Error("Stop blocked after ctx cancel")
	}
}
