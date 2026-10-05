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
//
// 2026-10-06: added the resolvability marker. The "joinable" filter alone is
// not enough — rows whose project_id AND application_code are both NULL enter
// the window, sync returns 0, they stay NULL, and ORDER BY session_key picks
// them again next batch, so the window saturates and the worker stalls at
// backfilled=0 forever. Measured on production: the first 5,745 rows of the
// ordered set were all unresolvable while LIMIT is 2,000. See §10.38.
func TestSessionProjectBackfillSQLContract(t *testing.T) {
	for _, marker := range []string{
		"public.sync_session_project_attr(",
		"ss.gw_project_id IS NULL",
		"sd.gw_session_id = ss.session_key",
		"sd.tenant_id IS NOT DISTINCT FROM ss.tenant_id",
		"public.gw_resolve_project_ref(sd.project_id, sd.application_code) IS NOT NULL",
		"ORDER BY ss.session_key",
		"LIMIT $1",
	} {
		if !strings.Contains(sessionProjectBackfillSQL, marker) {
			t.Errorf("batch SQL missing contract marker %q", marker)
		}
	}
}

// TestSessionProjectBackfillSQLWindowExcludesUnresolvable is a structural
// companion to the contract test: it asserts the resolvability guard sits
// INSIDE the targets CTE (before LIMIT), not somewhere that cannot narrow the
// window. A guard placed after the LIMIT would leave the SQL textually
// compliant while the stall came straight back.
func TestSessionProjectBackfillSQLWindowExcludesUnresolvable(t *testing.T) {
	lower := strings.ToLower(sessionProjectBackfillSQL)
	guard := strings.Index(lower, "gw_resolve_project_ref")
	limit := strings.Index(lower, "limit $1")
	if guard < 0 {
		t.Fatal("batch SQL has no resolvability guard at all")
	}
	if limit < 0 {
		t.Fatal("batch SQL has no LIMIT")
	}
	if guard > limit {
		t.Errorf("resolvability guard at offset %d lands after LIMIT at %d; "+
			"it must narrow the window, not post-filter the batch",
			guard, limit)
	}
}
