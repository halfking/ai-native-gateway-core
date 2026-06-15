package bg

// audit_trimmer_test.go — P8.8 unit tests for the AuditTrimmer.
// We don't hit a real DB; we test the lifecycle methods, default
// retention value, and idempotent Stop.

import (
	"context"
	"testing"
	"time"
)

func TestNewAuditTrimmer_Defaults(t *testing.T) {
	tr := NewAuditTrimmer(nil)
	if tr == nil {
		t.Fatal("NewAuditTrimmer returned nil")
	}
	if tr.retention != 90*24*time.Hour {
		t.Errorf("default retention = %v, want 90d", tr.retention)
	}
	if tr.tick != 24*time.Hour {
		t.Errorf("default tick = %v, want 24h", tr.tick)
	}
}

func TestAuditTrimmer_TrimOnce_NilPool(t *testing.T) {
	tr := NewAuditTrimmer(nil)
	overridesDeleted, auditDeleted, err := tr.TrimOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil, got %v", err)
	}
	if overridesDeleted != 0 || auditDeleted != 0 {
		t.Errorf("expected 0 deletes with nil pool, got %d/%d",
			overridesDeleted, auditDeleted)
	}
}

func TestAuditTrimmer_Stop_BeforeStart(t *testing.T) {
	tr := NewAuditTrimmer(nil)
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

func TestAuditTrimmer_Stop_Idempotent(t *testing.T) {
	tr := NewAuditTrimmer(nil)
	tr.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	tr.Stop()
	// Second Stop should also be safe
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

func TestAuditTrimmer_RunContextCancel(t *testing.T) {
	tr := NewAuditTrimmer(nil)
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

func TestAuditTrimmer_CustomRetention(t *testing.T) {
	tr := &AuditTrimmer{
		retention: 30 * 24 * time.Hour,
		tick:      6 * time.Hour,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	if tr.retention != 30*24*time.Hour {
		t.Errorf("retention = %v, want 30d", tr.retention)
	}
	if tr.tick != 6*time.Hour {
		t.Errorf("tick = %v, want 6h", tr.tick)
	}
}
