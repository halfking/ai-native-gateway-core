package bg

// project_backfill_worker_test.go — unit tests mirroring the lifecycle-test
// pattern of session_summaries_trimmer_test.go: defaults, nil-pool safety,
// idempotent Stop, context cancel. No real DB (see
// project_backfill_worker_realdb_test.go for the gated integration test).

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewSessionProjectBackfillWorker_Defaults(t *testing.T) {
	w := NewSessionProjectBackfillWorker(nil)
	if w == nil {
		t.Fatal("NewSessionProjectBackfillWorker returned nil")
	}
	if w.tick != 10*time.Minute {
		t.Errorf("default tick = %v, want 10m", w.tick)
	}
}

func TestSessionProjectBackfillWorker_NilPool_NoOp(t *testing.T) {
	w := NewSessionProjectBackfillWorker(nil)
	n, err := w.BackfillOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 backfills with nil pool, got %d", n)
	}
}

func TestSessionProjectBackfillWorker_NilReceiver_Stop(t *testing.T) {
	var w *SessionProjectBackfillWorker
	w.Stop() // must not panic
}

func TestSessionProjectBackfillWorker_Stop_BeforeStart(t *testing.T) {
	w := NewSessionProjectBackfillWorker(nil)
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("Stop blocked on never-Started worker")
	}
}

func TestSessionProjectBackfillWorker_Stop_Idempotent(t *testing.T) {
	w := NewSessionProjectBackfillWorker(nil)
	w.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	w.Stop()
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Error("second Stop blocked (not idempotent)")
	}
}

func TestSessionProjectBackfillWorker_Start_ContextCancel(t *testing.T) {
	w := NewSessionProjectBackfillWorker(nil)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel()
	select {
	case <-w.done:
		// goroutine exited
	case <-time.After(2 * time.Second):
		t.Error("worker goroutine did not exit after context cancel")
	}
}

// TestSessionProjectBackfillSQLContract pins the batch SQL shape: it must go
// through sync_session_project_attr (single 口径 with the 762 trigger chain),
// restrict to gw_project_id IS NULL, and join session_dim on both key and
// tenant so a same-key cross-tenant session is never mis-attributed.
func TestSessionProjectBackfillSQLContract(t *testing.T) {
	for _, marker := range []string{
		"public.sync_session_project_attr(",
		"ss.gw_project_id IS NULL",
		"sd.gw_session_id = ss.session_key",
		"sd.tenant_id IS NOT DISTINCT FROM ss.tenant_id",
		"ORDER BY ss.session_key",
		"LIMIT $1",
	} {
		if !strings.Contains(sessionProjectBackfillSQL, marker) {
			t.Errorf("batch SQL missing contract marker %q", marker)
		}
	}
}
