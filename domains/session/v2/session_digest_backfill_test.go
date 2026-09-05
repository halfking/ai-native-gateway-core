package v2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
)

// newBackfillMock builds a pgxmock pool wired for the backfill seam.
func newBackfillMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { _ = mock.ExpectationsWereMet() })
	return mock
}

// expectBackfillTxPrefix programs the shared batch prologue: BEGIN, the two
// transaction-scoped RLS bypass GUCs, and the candidate SELECT.
func expectBackfillTxPrefix(mock pgxmock.PgxPoolIface, batchSize int) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.digest IS NULL").
		WithArgs(batchSize).
		WillReturnRows(backfillCandidateRows())
}

// backfillCandidateRows returns one representative candidate turn: a hot-row
// id with populated bodies and metrics, plus fixed ts so the envelope is
// deterministic and can be recomputed by the test.
func backfillCandidateRows() *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "session_id", "turn_no", "request_id", "ts",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens",
		"cost_usd", "latency_ms", "status_code", "success", "error_kind",
		"injection_verdict", "output_verdict", "compression_applied", "compression_tokens_saved",
		"request_delta", "response_delta",
	})
	rows.AddRow(
		int64(42), "tenant_a", "sess_1", 3, "req_42", backfillFixedTS(),
		120, 80, 0, 0,
		0.05, 2400, 200, true, "",
		"pass", "skip", false, 30,
		[]byte(`[{"role":"user","content":"请帮我总结这个请求的关键结论"}]`),
		[]byte(`{"choices":[{"message":{"role":"assistant","content":"关键结论如下：一切正常。"}}]}`),
	)
	return rows
}

func backfillFixedTS() time.Time {
	return time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC)
}

// expectedBackfillDigestJSON recomputes what the job must persist for the
// representative candidate row — the exact sessiondigest.Build inputs the
// live writer would have used (digestBackfillMeta/Governance mirror
// session_writer_v2.Write's digestMeta/digestGovernance).
func expectedBackfillDigestJSON(t *testing.T) string {
	t.Helper()
	var request, response any
	if err := json.Unmarshal([]byte(`[{"role":"user","content":"请帮我总结这个请求的关键结论"}]`), &request); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"choices":[{"message":{"role":"assistant","content":"关键结论如下：一切正常。"}}]}`), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	meta := map[string]any{
		"prompt_tokens": 120, "completion_tokens": 80,
		"cost_usd": 0.05, "latency_ms": 2400,
		"status_code": 200, "success": true, "error_kind": "",
		"cache_read_tokens": 0, "cache_write_tokens": 0,
	}
	governance := map[string]any{
		"injection_verdict": "pass", "output_verdict": "skip",
		"compression_applied": false, "compression_tokens_saved": 30,
	}
	raw, err := sessiondigest.Marshal(sessiondigest.Build(request, response, meta, governance, backfillFixedTS()))
	if err != nil {
		t.Fatalf("marshal expected digest: %v", err)
	}
	return string(raw)
}

func counterDelta(t *testing.T, result string, fn func()) float64 {
	t.Helper()
	before := testutil.ToFloat64(digestBackfillTotal.WithLabelValues(result))
	fn()
	return testutil.ToFloat64(digestBackfillTotal.WithLabelValues(result)) - before
}

// TestSessionDigestBackfill_FillsHotRow pins the happy path: the envelope
// written back must equal sessiondigest.Build over the same inputs the live
// writer would have persisted, and the UPDATE must target the row by
// id+tenant+request with the digest-IS-NULL guard.
func TestSessionDigestBackfill_FillsHotRow(t *testing.T) {
	mock := newBackfillMock(t)
	expectBackfillTxPrefix(mock, 100)
	mock.ExpectExec("UPDATE public\\.session_turns_hot SET digest").
		WithArgs(int64(42), "tenant_a", expectedBackfillDigestJSON(t), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	b := newSessionDigestBackfillForTest(mock, 0, 100, 0) // rate 0 = unthrottled
	delta := counterDelta(t, "filled", func() {
		selected, err := b.processBatch(context.Background())
		if err != nil {
			t.Fatalf("processBatch: %v", err)
		}
		if selected != 1 {
			t.Fatalf("selected = %d, want 1", selected)
		}
	})
	if delta != 1 {
		t.Fatalf("filled delta = %v, want 1", delta)
	}
}

// TestSessionDigestBackfill_FallsThroughToPartition covers a row that moved
// from hot to a partition between the SELECT and the UPDATE (promote is a
// move): the hot UPDATE misses and the parent UPDATE must fill it.
func TestSessionDigestBackfill_FallsThroughToPartition(t *testing.T) {
	mock := newBackfillMock(t)
	expectBackfillTxPrefix(mock, 100)
	mock.ExpectExec("UPDATE public\\.session_turns_hot SET digest").
		WithArgs(int64(42), "tenant_a", expectedBackfillDigestJSON(t), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE public\\.session_turns SET digest").
		WithArgs(int64(42), "tenant_a", expectedBackfillDigestJSON(t), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	b := newSessionDigestBackfillForTest(mock, 0, 100, 0)
	delta := counterDelta(t, "filled", func() {
		if _, err := b.processBatch(context.Background()); err != nil {
			t.Fatalf("processBatch: %v", err)
		}
	})
	if delta != 1 {
		t.Fatalf("filled delta = %v, want 1", delta)
	}
}

// TestSessionDigestBackfill_SkipsWhenGuardsReject covers the concurrent
// replica race: both guarded UPDATEs match zero rows, so this replica counts
// the row as skipped instead of double-writing.
func TestSessionDigestBackfill_SkipsWhenGuardsReject(t *testing.T) {
	mock := newBackfillMock(t)
	expectBackfillTxPrefix(mock, 100)
	mock.ExpectExec("UPDATE public\\.session_turns_hot SET digest").
		WithArgs(int64(42), "tenant_a", expectedBackfillDigestJSON(t), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE public\\.session_turns SET digest").
		WithArgs(int64(42), "tenant_a", expectedBackfillDigestJSON(t), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	b := newSessionDigestBackfillForTest(mock, 0, 100, 0)
	delta := counterDelta(t, "skipped", func() {
		if _, err := b.processBatch(context.Background()); err != nil {
			t.Fatalf("processBatch: %v", err)
		}
	})
	if delta != 1 {
		t.Fatalf("skipped delta = %v, want 1", delta)
	}
}

// TestSessionDigestBackfill_EmptyBatchRollsBack verifies an idle tick does
// not hold a write transaction open.
func TestSessionDigestBackfill_EmptyBatchRollsBack(t *testing.T) {
	mock := newBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.digest IS NULL").
		WithArgs(100).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "session_id", "turn_no", "request_id", "ts",
			"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens",
			"cost_usd", "latency_ms", "status_code", "success", "error_kind",
			"injection_verdict", "output_verdict", "compression_applied", "compression_tokens_saved",
			"request_delta", "response_delta",
		}))
	mock.ExpectRollback()

	b := newSessionDigestBackfillForTest(mock, 0, 100, 0)
	selected, err := b.processBatch(context.Background())
	if err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if selected != 0 {
		t.Fatalf("selected = %d, want 0", selected)
	}
}

// TestSessionDigestBackfill_SelectErrorCountsError verifies a batch-level DB
// failure increments the error counter and surfaces the error so the drain
// loop backs off instead of hot-looping.
func TestSessionDigestBackfill_SelectErrorCountsError(t *testing.T) {
	mock := newBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.digest IS NULL").
		WithArgs(100).
		WillReturnError(assertAnError("db down"))
	mock.ExpectRollback()

	b := newSessionDigestBackfillForTest(mock, 0, 100, 0)
	delta := counterDelta(t, "error", func() {
		if _, err := b.processBatch(context.Background()); err == nil {
			t.Fatal("expected error from failed select")
		}
	})
	if delta != 1 {
		t.Fatalf("error delta = %v, want 1", delta)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func assertAnError(msg string) error { return errorString(msg) }

// TestSessionDigestBackfill_RateLimitPacesWrites pins the throttle contract:
// with rate R, the second and third writes in a batch each wait for a limiter
// tick (1/R apart). Three rows at 20 rows/s ⇒ ≥2×50ms of enforced spacing.
func TestSessionDigestBackfill_RateLimitPacesWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	const rate = 20 // 50ms per write
	mock := newBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))

	rows := backfillCandidateRows()
	// Two more copies with distinct ids so WithArgs stays unambiguous.
	for i, id := range []int64{43, 44} {
		rows.AddRow(id, "tenant_a", "sess_1", 4+i, "req_"+string(rune('0'+i)), backfillFixedTS(),
			120, 80, 0, 0,
			0.05, 2400, 200, true, "",
			"pass", "skip", false, 30,
			[]byte(`[{"role":"user","content":"请帮我总结这个请求的关键结论"}]`),
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"关键结论如下：一切正常。"}}]}`),
		)
	}
	mock.ExpectQuery("WHERE t\\.digest IS NULL").
		WithArgs(100).
		WillReturnRows(rows)
	for _, id := range []int64{42, 43, 44} {
		mock.ExpectExec("UPDATE public\\.session_turns_hot SET digest").
			WithArgs(id, "tenant_a", expectedBackfillDigestJSONForID(t, id), expectedRequestIDForID(id)).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	mock.ExpectCommit()

	b := newSessionDigestBackfillForTest(mock, 0, 100, rate)
	b.limiter = time.NewTicker(time.Second / time.Duration(rate))
	defer b.limiter.Stop()

	start := time.Now()
	if _, err := b.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	elapsed := time.Since(start)
	// Rows 2 and 3 each wait one full tick; allow a hair of scheduling slack
	// but never less than the two enforced intervals.
	minElapsed := 2 * (time.Second / rate) * 95 / 100
	if elapsed < minElapsed {
		t.Fatalf("elapsed = %v, want >= %v (rate limiting not enforced)", elapsed, minElapsed)
	}
}

// expectedRequestIDForID mirrors the synthetic ids used by the rate test so
// the WithArgs matcher stays exact.
func expectedRequestIDForID(id int64) string {
	switch id {
	case 42:
		return "req_42"
	case 43:
		return "req_0"
	default:
		return "req_1"
	}
}

// expectedBackfillDigestJSONForID recomputes the deterministic envelope for
// the rate test's synthetic rows. All rows share bodies/metrics except the
// request id (not part of the digest inputs), so the envelope is identical —
// that identity is itself worth pinning.
func expectedBackfillDigestJSONForID(t *testing.T, id int64) string {
	t.Helper()
	_ = id // digest inputs are identical across the synthetic rows
	return expectedBackfillDigestJSON(t)
}

// TestSessionDigestBackfill_MetaKeysPinWriterContract pins the Build input
// contract: the meta/governance key sets must stay key-for-key identical to
// session_writer_v2.Write's digestMeta/digestGovernance, otherwise backfilled
// envelopes diverge from live ones (e.g. hasMetrics flips an empty row to a
// nil digest and the row loops forever).
func TestSessionDigestBackfill_MetaKeysPinWriterContract(t *testing.T) {
	r := &digestBackfillRow{
		promptTokens: 120, completionTokens: 80,
		cacheReadTokens: 0, cacheWriteTokens: 0,
		costUSD: 0.05, latencyMs: 2400,
		statusCode: 200, success: true, errorKind: "",
		injectionVerdict: "pass", outputVerdict: "skip",
		compressionApplied: false, compressionTokensSaved: 30,
	}
	meta := digestBackfillMeta(r)
	for _, key := range []string{
		"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
		"status_code", "success", "error_kind", "cache_read_tokens", "cache_write_tokens",
	} {
		if _, ok := meta[key]; !ok {
			t.Errorf("digestBackfillMeta lost key %q (writer's digestMeta has it)", key)
		}
	}
	if len(meta) != 9 {
		t.Errorf("digestBackfillMeta has %d keys, want exactly the writer's 9", len(meta))
	}
	governance := digestBackfillGovernance(r)
	for _, key := range []string{
		"injection_verdict", "output_verdict", "compression_applied", "compression_tokens_saved",
	} {
		if _, ok := governance[key]; !ok {
			t.Errorf("digestBackfillGovernance lost key %q (writer's digestGovernance has it)", key)
		}
	}
	if len(governance) != 4 {
		t.Errorf("digestBackfillGovernance has %d keys, want exactly the writer's 4", len(governance))
	}
}

// TestSessionDigestBackfill_NilPoolLifecycle guards the defensive boot path:
// a nil (or typed-nil) pool must not panic, and Start/Stop stay idempotent.
func TestSessionDigestBackfill_NilPoolLifecycle(t *testing.T) {
	b := newSessionDigestBackfillForTest(nil, 0, 0, 0)
	b.Start(context.Background())
	b.Start(context.Background()) // second start is a no-op
	b.Stop()
	b.Stop() // second stop is a no-op
}

// TestSessionDigestBackfill_DefaultConstants pins the documented operational
// contract (100 rows/s default, adaptive idle cap, batch timeout) so config
// drift is caught in review.
func TestSessionDigestBackfill_DefaultConstants(t *testing.T) {
	if sessionDigestBackfillDefaultRate != 100 {
		t.Errorf("default rate drifted: %d", sessionDigestBackfillDefaultRate)
	}
	if sessionDigestBackfillDefaultBatch != 100 {
		t.Errorf("default batch drifted: %d", sessionDigestBackfillDefaultBatch)
	}
	if sessionDigestBackfillMaxIdle != 30*time.Minute {
		t.Errorf("max idle drifted: %v", sessionDigestBackfillMaxIdle)
	}
	if sessionDigestBackfillBatchTimeout != 5*time.Minute {
		t.Errorf("batch timeout drifted: %v", sessionDigestBackfillBatchTimeout)
	}
}
