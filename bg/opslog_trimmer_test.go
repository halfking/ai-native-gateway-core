package bg

// opslog_trimmer_test.go — unit tests for the OpslogTrimmer.
//
// Mirrors the lifecycle-test pattern of audit/handoff trimmers:
// defaults, nil-pool safety, idempotent Stop, context cancel,
// custom retention. No real DB.

import (
	"context"
	"testing"
	"time"
)

func TestNewOpslogTrimmer_Defaults(t *testing.T) {
	tr := NewOpslogTrimmer(nil)
	if tr == nil {
		t.Fatal("NewOpslogTrimmer returned nil")
	}
	if tr.cflRetention != 7*24*time.Hour {
		t.Errorf("default cfl retention = %v, want 7d", tr.cflRetention)
	}
	if tr.cpmRetention != 30*24*time.Hour {
		t.Errorf("default cpm retention = %v, want 30d", tr.cpmRetention)
	}
	if tr.tick != 24*time.Hour {
		t.Errorf("default tick = %v, want 24h", tr.tick)
	}
}

func TestOpslogTrimmer_TrimOnce_NilPool(t *testing.T) {
	tr := NewOpslogTrimmer(nil)
	cflDeleted, cpmDeleted, err := tr.TrimOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil, got %v", err)
	}
	if cflDeleted != 0 || cpmDeleted != 0 {
		t.Errorf("expected 0 deletes with nil pool, got %d/%d",
			cflDeleted, cpmDeleted)
	}
}

func TestOpslogTrimmer_Stop_BeforeStart(t *testing.T) {
	tr := NewOpslogTrimmer(nil)
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

func TestOpslogTrimmer_Stop_Idempotent(t *testing.T) {
	tr := NewOpslogTrimmer(nil)
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

func TestOpslogTrimmer_RunContextCancel(t *testing.T) {
	tr := NewOpslogTrimmer(nil)
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

func TestOpslogTrimmer_CustomRetention(t *testing.T) {
	tr := &OpslogTrimmer{
		cflRetention: 3 * 24 * time.Hour,
		cpmRetention: 14 * 24 * time.Hour,
		tick:         12 * time.Hour,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	if tr.cflRetention != 3*24*time.Hour {
		t.Errorf("cfl retention = %v, want 3d", tr.cflRetention)
	}
	if tr.cpmRetention != 14*24*time.Hour {
		t.Errorf("cpm retention = %v, want 14d", tr.cpmRetention)
	}
}
