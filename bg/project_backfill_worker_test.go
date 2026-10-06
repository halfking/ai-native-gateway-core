package bg

// project_backfill_worker_test.go — unit tests mirroring the lifecycle-test
// pattern of session_summaries_trimmer_test.go: defaults, nil-pool safety,
// idempotent Stop, context cancel. No real DB (see
// project_backfill_worker_realdb_test.go for the gated integration test).

import (
	"context"
	"os"
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

// backfillOnceBody extracts the BackfillOnce function source. Gates that reason
// about "where in the function" must read that body — a whole-file substring
// search happily matches a marker living in a different function, which is how
// a gate ends up passing while proving nothing.
func backfillOnceBody(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("project_backfill_worker.go")
	if err != nil {
		t.Fatalf("read worker source: %v", err)
	}
	s := string(src)
	start := strings.Index(s, "func (w *SessionProjectBackfillWorker) BackfillOnce(")
	if start < 0 {
		t.Fatal("BackfillOnce not found — the gate is silently inapplicable")
	}
	// The function ends at the first line that is exactly "}" at column 0.
	rest := s[start:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end+3]
	}
	t.Fatal("could not find the end of BackfillOnce")
	return ""
}

// TestBackfillBatchBudgetIsTransactionLocal pins the per-batch statement_timeout
// budget introduced 2026-10-06 (runbook §10.41).
//
// Why it must be checked as a *function body with order*, not as file-wide
// substrings:
//   - a marker found anywhere in the file says nothing about BackfillOnce;
//   - and the *order* is the whole point — set_config outside the transaction
//     leaks the raised budget onto pooled connections; inside but after the
//     batch query is too late to protect it.
func TestBackfillBatchBudgetIsTransactionLocal(t *testing.T) {
	body := backfillOnceBody(t)

	begin := strings.Index(body, "pool.Begin(")
	setCfg := strings.Index(body, "set_config('statement_timeout'")
	batch := strings.Index(body, "sessionProjectBackfillSQL")
	commit := strings.Index(body, "tx.Commit(")

	if begin < 0 {
		t.Fatal("BackfillOnce no longer opens a transaction — the SET LOCAL has nothing to be local to")
	}
	if setCfg < 0 {
		t.Fatal("BackfillOnce no longer sets a transaction-local statement_timeout; " +
			"the batch is back under the 30s role default and will be killed under contention")
	}
	if commit < 0 {
		t.Fatal("BackfillOnce never commits its transaction — every batch's work is silently discarded")
	}
	if !(begin < setCfg && setCfg < batch) {
		t.Errorf("ordering wrong: Begin@%d, set_config@%d, batch@%d — expected "+
			"Begin < set_config < batch", begin, setCfg, batch)
	}
	if !(batch < commit) {
		t.Errorf("ordering wrong: batch@%d must be committed@%d, not after it", batch, commit)
	}
	// The third argument is what makes it LOCAL. Dropping it leaks the budget
	// onto pooled connections, where it would outlive the batch.
	if !strings.Contains(body, "set_config('statement_timeout', $1, true)") {
		t.Error("set_config must be called with the literal third argument `true` " +
			"(= SET LOCAL); anything else leaks the raised timeout onto pooled connections")
	}
	// The budget must be derived from the named constant, so the startup log and
	// the enforced value cannot drift apart.
	if !strings.Contains(body, `fmt.Sprintf("%ds", batchTimeoutSec)`) {
		t.Errorf("the timeout value must come from fmt.Sprintf(%q, batchTimeoutSec) "+
			"so it matches what the worker logs at startup", "%ds")
	}
}

// TestBackfillBatchBudgetExceedsRoleDefault is a value-level gate, not a text
// one: the whole point is to lift the 30s role default measured on 252's
// llm_gateway role. A "budget" below it would be a no-op dressed as a fix.
func TestBackfillBatchBudgetExceedsRoleDefault(t *testing.T) {
	const measuredRoleDefaultSec = 30
	if batchTimeoutSec <= measuredRoleDefaultSec {
		t.Errorf("batchTimeoutSec = %d, must exceed the measured role default of %ds "+
			"or SET LOCAL changes nothing", batchTimeoutSec, measuredRoleDefaultSec)
	}
	if batchTimeoutSec > 600 {
		t.Errorf("batchTimeoutSec = %d is beyond any defensible lock-hold budget "+
			"for a 2000-row batch; keep it bounded", batchTimeoutSec)
	}
}
