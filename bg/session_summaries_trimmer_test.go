package bg

// session_summaries_trimmer_test.go — unit tests for the
// SessionSummariesTrimmer. Mirrors the lifecycle-test pattern of the
// opslog/audit trimmers: defaults, nil-pool safety, idempotent Stop,
// context cancel. No real DB.

import (
	"context"
	"testing"
	"time"
)

func TestNewSessionSummariesTrimmer_Defaults(t *testing.T) {
	tr := NewSessionSummariesTrimmer(nil)
	if tr == nil {
		t.Fatal("NewSessionSummariesTrimmer returned nil")
	}
	if tr.retention != 90*24*time.Hour {
		t.Errorf("default retention = %v, want 90d", tr.retention)
	}
	if tr.tick != 24*time.Hour {
		t.Errorf("default tick = %v, want 24h", tr.tick)
	}
}

func TestSessionSummariesTrimmer_TrimOnce_NilPool(t *testing.T) {
	tr := NewSessionSummariesTrimmer(nil)
	deleted, err := tr.TrimOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil, got %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deletes with nil pool, got %d", deleted)
	}
}

func TestSessionSummariesTrimmer_Stop_BeforeStart(t *testing.T) {
	tr := NewSessionSummariesTrimmer(nil)
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

func TestSessionSummariesTrimmer_Stop_Idempotent(t *testing.T) {
	tr := NewSessionSummariesTrimmer(nil)
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

func TestSessionSummariesTrimmer_RunContextCancel(t *testing.T) {
	tr := NewSessionSummariesTrimmer(nil)
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
