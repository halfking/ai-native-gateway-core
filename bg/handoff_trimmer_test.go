package bg

// handoff_trimmer_test.go — unit tests for the HandoffTrimmer.
//
// Mirrors bg/audit_trimmer_test.go: lifecycle, defaults, idempotent
// Stop, nil-pool safety, custom retention. No real DB.

import (
	"context"
	"testing"
	"time"
)

func TestNewHandoffTrimmer_Defaults(t *testing.T) {
	tr := NewHandoffTrimmer(nil)
	if tr == nil {
		t.Fatal("NewHandoffTrimmer returned nil")
	}
	if tr.retention != 14*24*time.Hour {
		t.Errorf("default retention = %v, want 14d", tr.retention)
	}
	if tr.tick != 24*time.Hour {
		t.Errorf("default tick = %v, want 24h", tr.tick)
	}
}

func TestHandoffTrimmer_TrimOnce_NilPool(t *testing.T) {
	tr := NewHandoffTrimmer(nil)
	deleted, err := tr.TrimOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil, got %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deletes with nil pool, got %d", deleted)
	}
}

func TestHandoffTrimmer_Stop_BeforeStart(t *testing.T) {
	tr := NewHandoffTrimmer(nil)
	done := make(chan struct{})
	go func() {
		tr.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("Stop blocked on never-Started trimmer")
	}
}

func TestHandoffTrimmer_Stop_Idempotent(t *testing.T) {
	tr := NewHandoffTrimmer(nil)
	tr.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	tr.Stop()
	done := make(chan struct{})
	go func() {
		tr.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("Second Stop blocked")
	}
}

func TestHandoffTrimmer_RunContextCancel(t *testing.T) {
	tr := NewHandoffTrimmer(nil)
	ctx, cancel := context.WithCancel(context.Background())
	tr.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		tr.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Error("Stop blocked after ctx cancel")
	}
}

func TestHandoffTrimmer_CustomRetention(t *testing.T) {
	tr := &HandoffTrimmer{
		retention: 7 * 24 * time.Hour,
		tick:      6 * time.Hour,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	if tr.retention != 7*24*time.Hour {
		t.Errorf("retention = %v, want 7d", tr.retention)
	}
	if tr.tick != 6*time.Hour {
		t.Errorf("tick = %v, want 6h", tr.tick)
	}
}
