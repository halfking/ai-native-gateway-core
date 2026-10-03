package v2

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newStatusBackfillMock builds a pgxmock pool wired for the request_status
// backfill seam.
func newStatusBackfillMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { _ = mock.ExpectationsWereMet() })
	return mock
}

// expectStatusProbe programs the retirement probe: BEGIN + the to_regclass
// probe + ROLLBACK (the probe never writes).
func expectStatusProbe(mock pgxmock.PgxPoolIface, hasSource, hasTarget bool) {
	mock.ExpectBegin()
	mock.ExpectQuery("to_regclass\\('public.request_logs'\\)").
		WillReturnRows(pgxmock.NewRows([]string{"has_source", "has_target"}).
			AddRow(hasSource, hasTarget))
	mock.ExpectRollback()
}

// expectStatusTxPrefix programs the shared batch prologue: BEGIN, the two
// transaction-scoped RLS bypass GUCs, and the candidate SELECT keyed by the
// id watermark.
func expectStatusTxPrefix(mock pgxmock.PgxPoolIface, cursor int64, batchSize int) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(cursor, batchSize).
		WillReturnRows(statusCandidateRows())
}

// statusCandidateRows returns one representative candidate: an id/partition_date
// pair plus the request_id whose request_logs twin carries the label.
func statusCandidateRows() *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"})
	rows.AddRow(int64(42), statusFixedDate(), "tenant_a", "req_42")
	return rows
}

func statusFixedDate() time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
}

func statusCounterDelta(t *testing.T, result string, fn func()) float64 {
	t.Helper()
	before := testutil.ToFloat64(requestStatusBackfillTotal.WithLabelValues(result))
	fn()
	return testutil.ToFloat64(requestStatusBackfillTotal.WithLabelValues(result)) - before
}

// TestSessionRequestStatusBackfill_FillsBatch pins the happy path: the batched
// UPDATE must carry the guarded `request_status IS NULL` predicate, join
// request_logs on request_id ONLY, and address the row by id + partition_date
// so the partitioned parent prunes.
func TestSessionRequestStatusBackfill_FillsBatch(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectStatusTxPrefix(mock, 0, 100)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status = l\\.request_status").
		WithArgs(int64(42), statusFixedDate(), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0) // rate 0 = unthrottled
	delta := statusCounterDelta(t, "filled", func() {
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

// TestSessionRequestStatusBackfill_UpdateSQLContract inspects the SQL text
// itself, because every correctness claim about the join and the idempotence
// guard lives in that string. A regex-based mock only proves the job issues
// SOME UPDATE; this proves it issues the RIGHT one.
func TestSessionRequestStatusBackfill_UpdateSQLContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		batch    []requestStatusBackfillRow
		want     []string
		notWant  []string
		wantArgs int
	}{
		{
			name:  "guard_and_join",
			batch: []requestStatusBackfillRow{{id: 42, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_42"}},
			want: []string{
				"UPDATE public.session_turns t SET request_status = l.request_status",
				"FROM (VALUES",
				"AS v(id, partition_date, request_id)",
				// join must be request_id ONLY — no ts, no tenant, no created_at
				"JOIN public.request_logs l ON l.request_id = v.request_id",
				// idempotence guard
				"AND t.request_status IS NULL",
				// partition pruning via pkey (id, partition_date)
				"WHERE t.id = v.id AND t.partition_date = v.partition_date",
				// NULL source labels must never be written
				"AND l.request_status IS NOT NULL",
			},
			notWant:  []string{"l.ts =", "l.created_at", "ON CONFLICT", "t.tenant_id"},
			wantArgs: 3,
		},
		{
			name: "multi_row_arg_ordering",
			batch: []requestStatusBackfillRow{
				{id: 1, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_a"},
				{id: 2, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_b"},
			},
			want:     []string{"($1::bigint, $2::date, $3::text)", "($4::bigint, $5::date, $6::text)"},
			notWant:  []string{"AND t.request_status IS NOT NULL"},
			wantArgs: 6,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := buildRequestStatusUpdateSQL(tc.batch)
			for _, want := range tc.want {
				if !strings.Contains(sql, want) {
					t.Errorf("UPDATE SQL missing %q\ngot: %s", want, sql)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(sql, notWant) {
					t.Errorf("UPDATE SQL must not contain %q\ngot: %s", notWant, sql)
				}
			}
			if len(args) != tc.wantArgs {
				t.Errorf("bind args = %d, want %d", len(args), tc.wantArgs)
			}
		})
	}
}

// TestSessionRequestStatusBackfill_UpdateArgOrdering pins the bind-arg order
// (id, partition_date, request_id) per VALUES tuple: an off-by-one here would
// silently write one turn's label onto another turn.
func TestSessionRequestStatusBackfill_UpdateArgOrdering(t *testing.T) {
	day2 := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	_, args := buildRequestStatusUpdateSQL([]requestStatusBackfillRow{
		{id: 7, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_seven"},
		{id: 9, partitionDate: day2, tenantID: "tenant_b", requestID: "req_nine"},
	})
	want := []any{int64(7), statusFixedDate(), "req_seven", int64(9), day2, "req_nine"}
	if len(args) != len(want) {
		t.Fatalf("args = %d, want %d", len(args), len(want))
	}
	// Both `args` and `want` are FLAT triples (id, date, request_id) per row.
	for i := 0; i < len(want)/3; i++ {
		base := i * 3
		gotID, okID := args[base].(int64)
		wID, _ := want[base].(int64)
		gotDate, okDate := args[base+1].(time.Time)
		gotReq, okReq := args[base+2].(string)
		if !okID || !okDate || !okReq ||
			gotID != wID ||
			!gotDate.Equal(want[base+1].(time.Time)) ||
			gotReq != want[base+2].(string) {
			t.Fatalf("arg tuple %d = (%v, %v, %v), want (%v, %v, %v)",
				i, args[base], args[base+1], args[base+2], want[base], want[base+1], want[base+2])
		}
	}
}

// TestSessionRequestStatusBackfill_SkipsWhenGuardRejects covers the concurrent
// replica race: the guarded UPDATE matches zero rows, so this replica counts
// the row as skipped instead of double-writing.
func TestSessionRequestStatusBackfill_SkipsWhenGuardRejects(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectStatusTxPrefix(mock, 0, 100)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status").
		WithArgs(int64(42), statusFixedDate(), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	delta := statusCounterDelta(t, "skipped", func() {
		if _, err := b.processBatch(context.Background()); err != nil {
			t.Fatalf("processBatch: %v", err)
		}
	})
	if delta != 1 {
		t.Fatalf("skipped delta = %v, want 1", delta)
	}
}

// TestSessionRequestStatusBackfill_EmptyBatchRollsBack verifies an idle tick
// does not hold a write transaction open.
func TestSessionRequestStatusBackfill_EmptyBatchRollsBack(t *testing.T) {
	mock := newStatusBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100).
		WillReturnRows(pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"}))
	mock.ExpectRollback()

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	selected, err := b.processBatch(context.Background())
	if err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if selected != 0 {
		t.Fatalf("selected = %d, want 0", selected)
	}
}

// TestSessionRequestStatusBackfill_SelectErrorCountsError verifies a batch-level
// DB failure increments the error counter and surfaces the error so the drain
// loop backs off instead of hot-looping. The re-probe (triggered by the failed
// SELECT) reports the source still present, so this is NOT a retirement.
func TestSessionRequestStatusBackfill_SelectErrorCountsError(t *testing.T) {
	mock := newStatusBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100).
		WillReturnError(assertAnError("db down"))
	mock.ExpectRollback()
	expectStatusProbe(mock, true, true) // re-probe: still there => retryable

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	delta := statusCounterDelta(t, "error", func() {
		if _, err := b.processBatch(context.Background()); err == nil {
			t.Fatal("expected error from failed select")
		}
	})
	if delta != 1 {
		t.Fatalf("error delta = %v, want 1", delta)
	}
}

// TestSessionRequestStatusBackfill_CursorAdvancesWithinDrain pins the keyset
// watermark: batch 2 must resume from the last id of batch 1, not re-scan from
// id 0. Without this the job would re-read the same head of the table forever.
func TestSessionRequestStatusBackfill_CursorAdvancesWithinDrain(t *testing.T) {
	mock := newStatusBackfillMock(t)
	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	b.cursor = 0

	expectStatusTxPrefix(mock, 0, 100)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status").
		WithArgs(int64(42), statusFixedDate(), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	if _, err := b.processBatch(context.Background()); err != nil {
		t.Fatalf("batch 1: %v", err)
	}
	if b.cursor != 42 {
		t.Fatalf("cursor after batch 1 = %d, want 42", b.cursor)
	}

	// Second batch resumes from the watermark and returns a short batch, which
	// is how drain() detects exhaustion.
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(42), 100).
		WillReturnRows(pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"}))
	mock.ExpectRollback()

	if _, err := b.processBatch(context.Background()); err != nil {
		t.Fatalf("batch 2: %v", err)
	}
}

// TestSessionRequestStatusBackfill_DrainResetsCursorPerDrain verifies the
// watermark is re-armed at the start of each drain, so rows promoted/inserted
// below the previous watermark are still reachable on the next pass.
func TestSessionRequestStatusBackfill_DrainResetsCursorPerDrain(t *testing.T) {
	mock := newStatusBackfillMock(t)
	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	b.cursor = 999999

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100). // <-- re-armed, not 999999
		WillReturnRows(pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"}))
	mock.ExpectRollback()

	if _, err := b.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// ── Retirement (hard requirement #2) ────────────────────────────────────────

// TestSessionRequestStatusBackfill_SourceProbeDetectsRetirement verifies the
// probe itself distinguishes "request_logs still there" from "retired" without
// raising — to_regclass yields NULL, it does not error.
func TestSessionRequestStatusBackfill_SourceProbeDetectsRetirement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasSource  bool
		hasTarget  bool
		wantResult bool
	}{
		{"live", true, true, true},
		{"request_logs_retired", false, true, false},
		{"migration_823_absent", true, false, false},
		{"both_absent", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newStatusBackfillMock(t)
			expectStatusProbe(mock, tc.hasSource, tc.hasTarget)
			b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
			got, err := b.sourceAvailable(context.Background())
			if err != nil {
				t.Fatalf("sourceAvailable: %v", err)
			}
			if got != tc.wantResult {
				t.Fatalf("sourceAvailable = %v, want %v", got, tc.wantResult)
			}
		})
	}
}

// TestSessionRequestStatusBackfill_RetiredStopsWithoutErrorNoise is the core
// retirement guarantee: once request_logs is gone the job increments `retired`
// exactly once and RETURNS — no drain attempt, no per-tick ERROR spam.
func TestSessionRequestStatusBackfill_RetiredStopsWithoutErrorNoise(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectStatusProbe(mock, false, true) // retired
	// NOTE: no ExpectBegin/SELECT/UPDATE programmed. pgxmock fails the test on
	// any UNEXPECTED call, so if the job tried to drain, this goes red.

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	delta := statusCounterDelta(t, "retired", func() {
		done := make(chan struct{})
		b.Start(context.Background())
		go func() { b.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("run() did not exit after detecting retirement")
		}
	})
	if delta != 1 {
		t.Fatalf("retired delta = %v, want exactly 1 (job must stop, not retry)", delta)
	}
}

// TestSessionRequestStatusBackfill_SelectFailureAfterRetirementIsTerminal
// covers the race where request_logs is dropped between the probe and the
// candidate SELECT: the job must classify it as retirement (retired counter),
// never as a retryable error.
func TestSessionRequestStatusBackfill_SelectFailureAfterRetirementIsTerminal(t *testing.T) {
	mock := newStatusBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100).
		WillReturnError(assertAnError(`relation "public.request_logs" does not exist`))
	// The re-probe runs INSIDE processBatch, i.e. before this tx's deferred
	// Rollback. pgxmock matches expectations in order, so the probe must be
	// programmed before the batch rollback.
	expectStatusProbe(mock, false, true) // re-probe: gone => terminal
	mock.ExpectRollback()

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	errDelta := statusCounterDelta(t, "error", func() {})
	retiredDelta := statusCounterDelta(t, "retired", func() {})
	_, err := b.processBatch(context.Background())
	if err != errRequestStatusSourceRetired {
		t.Fatalf("processBatch err = %v, want errRequestStatusSourceRetired", err)
	}
	if after := statusCounterDelta(t, "retired", func() {}); after != 0 {
		t.Fatalf("processBatch must not itself bump retired (run() owns it): %v", after)
	}
	if e, r := errDelta, retiredDelta; e != 0 || r != 0 {
		t.Fatalf("counter drift: error=%v retired=%v, want 0/0", e, r)
	}
}

// ── Lifecycle / throttle contract ───────────────────────────────────────────

// TestSessionRequestStatusBackfill_NilPoolLifecycle guards the defensive boot
// path: a nil (or typed-nil) pool must not panic, and Start/Stop stay idempotent.
func TestSessionRequestStatusBackfill_NilPoolLifecycle(t *testing.T) {
	b := newSessionRequestStatusBackfillForTest(nil, 0, 0, 0)
	b.Start(context.Background())
	b.Start(context.Background()) // second start is a no-op
	b.Stop()
	b.Stop() // second stop is a no-op
}

// TestSessionRequestStatusBackfill_RateLimitPacesWrites pins the throttle
// contract: ratePerSec means rows/s, and the job consumes one tick per row even
// though it emits a single UPDATE per batch.
func TestSessionRequestStatusBackfill_RateLimitPacesWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	const rate = 20 // 50ms per row
	mock := newStatusBackfillMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	rows := pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"})
	for _, id := range []int64{1, 2, 3} {
		rows.AddRow(id, statusFixedDate(), "tenant_a", "req_"+string(rune('a'+id-1)))
	}
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status").
		WithArgs(
			int64(1), statusFixedDate(), "req_a",
			int64(2), statusFixedDate(), "req_b",
			int64(3), statusFixedDate(), "req_c").
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mock.ExpectCommit()

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, rate)
	b.limiter = time.NewTicker(time.Second / rate)
	defer b.limiter.Stop()

	start := time.Now()
	if _, err := b.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	elapsed := time.Since(start)
	// 3 rows at 20 rows/s ⇒ ≥2×50ms of enforced spacing (first tick buffered).
	minElapsed := 2 * (time.Second / rate) * 95 / 100
	if elapsed < minElapsed {
		t.Fatalf("elapsed = %v, want >= %v (rate limiting not enforced)", elapsed, minElapsed)
	}
}

// TestSessionRequestStatusBackfill_DefaultConstants pins the documented
// operational contract so config drift is caught in review.
func TestSessionRequestStatusBackfill_DefaultConstants(t *testing.T) {
	if sessionRequestStatusBackfillDefaultRate != 100 {
		t.Errorf("default rate drifted: %d", sessionRequestStatusBackfillDefaultRate)
	}
	if sessionRequestStatusBackfillDefaultBatch != 100 {
		t.Errorf("default batch drifted: %d", sessionRequestStatusBackfillDefaultBatch)
	}
	if sessionRequestStatusBackfillMaxIdle != 30*time.Minute {
		t.Errorf("max idle drifted: %v", sessionRequestStatusBackfillMaxIdle)
	}
	if sessionRequestStatusBackfillBatchTimeout != 5*time.Minute {
		t.Errorf("batch timeout drifted: %v", sessionRequestStatusBackfillBatchTimeout)
	}
	if sessionRequestStatusBackfillMaxRate != 1000 {
		t.Errorf("max rate drifted: %d", sessionRequestStatusBackfillMaxRate)
	}
}

// TestSessionRequestStatusBackfill_CandidateSQLJoinIsRequestIDOnly pins the
// measured facts the join relies on: request_logs.request_id is unique, so no
// extra predicate may be added "for safety" — it would only drop matchable rows.
func TestSessionRequestStatusBackfill_CandidateSQLJoinIsRequestIDOnly(t *testing.T) {
	sql := sessionRequestStatusCandidateSQL
	for _, want := range []string{
		"JOIN public.request_logs l ON l.request_id = t.request_id",
		"t.request_status IS NULL",
		"l.request_status IS NOT NULL",
		"ORDER BY t.id",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("candidate SQL missing %q", want)
		}
	}
	for _, notWant := range []string{"l.ts =", "l.created_at", "l.tenant_id"} {
		if strings.Contains(sql, notWant) {
			t.Errorf("candidate SQL must not add join condition %q (request_id is unique)", notWant)
		}
	}
}

// TestSessionRequestStatusBackfill_SourceProbeSQLUsesToRegclass guards the
// mechanism that makes retirement observable: ::regclass would RAISE on a
// dropped table (turning retirement into ERROR spam), to_regclass returns NULL.
func TestSessionRequestStatusBackfill_SourceProbeSQLUsesToRegclass(t *testing.T) {
	if strings.Contains(sessionRequestStatusSourceProbeSQL, "::regclass") {
		t.Error("probe SQL must use to_regclass (NULL on absent table), not ::regclass (raises)")
	}
	if !strings.Contains(sessionRequestStatusSourceProbeSQL, "to_regclass('public.request_logs')") {
		t.Error("probe SQL must probe request_logs by to_regclass")
	}
}

// ── Real-database gate (TEST_DATABASE_URL) ─────────────────────────────────

// statusBackfillFixtureDB creates a THROWAWAY DATABASE and returns a pool
// connected to it.
//
// Why a database and not a schema: the job's SQL is deliberately
// `public.`-qualified (it must target the real tables in production), so a
// search_path-scoped schema could not intercept it without either rewriting
// production SQL (which would make the fixture test the wrong statement) or
// shadowing `public.` (fragile). A throwaway database lets the fixture run the
// EXACT production statement.
//
// The caller owns cleanup; see the deferred drop in the test below.
func statusBackfillFixtureDB(t *testing.T, adminDSN string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	defer admin.Close()
	name := "rsbfix_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", name, err)
	}
	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("fixture pgxpool.NewWithConfig: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("fixture database unavailable: %v", err)
	}
	return pool, name
}

// TestSessionRequestStatusBackfill_RealDB is the real-library gate: it builds a
// miniature request_logs + RANGE-partitioned session_turns in a throwaway
// database, runs the job's batches, and asserts
//
//	(a) only joinable NULL rows were updated,
//	(b) a row with no v1 twin stays NULL (structurally unbackfillable),
//	(c) a row whose v1 label is NULL stays NULL,
//	(d) an already-labelled row is untouched, and
//	(e) a second full pass is a no-op (idempotence).
//
// Fixture teardown is deliberately NOT in t.Cleanup. t.Cleanup callbacks run
// AFTER deferred functions in the same test, so a cleanup that reuses a pool
// closed by `defer pool.Close()` fails SILENTLY and leaves the fixture behind in
// the real database — a trap this project has already hit once. Here the drop
// runs while the pools are still open, and a failure is FATAL (t.Errorf on the
// outer pool, t.Fatalf inline).
func TestSessionRequestStatusBackfill_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	name := "rsbfix_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Skipf("cannot create throwaway database (insufficient privileges?), skipping: %v", err)
	}
	// Last-resort drop so a mid-test t.Fatalf cannot leak a database. Runs
	// BEFORE admin.Close() (deferred calls are LIFO), on the still-open pool.
	dropped := false
	defer func() {
		if dropped {
			return
		}
		if _, err := admin.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); err != nil {
			t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, err)
		}
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); err != nil {
			t.Errorf("FATAL: fixture database %s not dropped: %v", name, err)
		}
		admin.Close()
	}()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("fixture pool: %v", err)
	}
	defer pool.Close() // runs BEFORE the admin-drop defer (LIFO) ⇒ pools closed first

	if _, err := pool.Exec(ctx, `
CREATE TABLE public.request_logs (
	request_id      TEXT PRIMARY KEY,
	request_status  TEXT
);
CREATE TABLE public.session_turns (
	id              BIGINT,
	partition_date  DATE NOT NULL,
	tenant_id       TEXT,
	request_id      TEXT,
	request_status  TEXT
) PARTITION BY RANGE (partition_date);
CREATE TABLE public.session_turns_2026_09
	PARTITION OF public.session_turns FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE public.session_turns_2026_10
	PARTITION OF public.session_turns FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE public.session_turns_default PARTITION OF public.session_turns DEFAULT;
CREATE UNIQUE INDEX ON public.session_turns (id, partition_date);
INSERT INTO public.request_logs (request_id, request_status) VALUES
	('req_1','success'), ('req_2','rate_limited'), ('req_3','failure'),
	('req_4','in_progress'), ('req_6',NULL), ('req_8','success');
INSERT INTO public.session_turns (id, partition_date, tenant_id, request_id, request_status) VALUES
	(1,'2026-09-01','tenant_a','req_1',NULL),
	(2,'2026-09-01','tenant_a','req_2',NULL),
	(3,'2026-09-15','tenant_a','req_3',NULL),
	(4,'2026-09-20','tenant_b','req_4',NULL),
	(5,'2026-09-25','tenant_b','req_orphan',NULL),
	(6,'2026-09-28','tenant_b','req_6',NULL),
	(7,'2026-09-29','tenant_b','req_7','success'),
	(8,'2026-10-02','tenant_a','req_8',NULL),
	(9,'2026-11-15','tenant_c','req_9',NULL);
`); err != nil {
		t.Fatalf("create/seed fixture: %v", err)
	}

	b := newSessionRequestStatusBackfillForTest(pool, 0, 100, 0)
	selected, err := b.drain(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if selected != 5 {
		t.Fatalf("selected = %d, want 5 (req_6 has a NULL v1 label ⇒ not a candidate)", selected)
	}

	assertStatus := func(id int64, want any, why string) {
		t.Helper()
		var got *string
		if err := pool.QueryRow(ctx, "SELECT request_status FROM public.session_turns WHERE id = $1", id).Scan(&got); err != nil {
			t.Fatalf("read id=%d: %v", id, err)
		}
		var norm any
		if got != nil {
			norm = *got
		}
		if norm != want {
			t.Errorf("id=%d request_status = %v, want %v (%s)", id, norm, want, why)
		}
	}
	assertStatus(1, "success", "joinable NULL row must be filled")
	assertStatus(2, "rate_limited", "rate_limited is the label only v1 carries")
	assertStatus(3, "failure", "row inside the same partition")
	assertStatus(4, "in_progress", "second tenant's row must be filled")
	assertStatus(8, "success", "second partition must be filled too")
	assertStatus(5, nil, "no v1 twin ⇒ structurally unbackfillable, must stay NULL")
	assertStatus(6, nil, "v1 twin label is NULL ⇒ must stay NULL")
	assertStatus(7, "success", "already-labelled row must be untouched")
	assertStatus(9, nil, "DEFAULT partition row with no twin ⇒ must stay NULL")

	// Idempotence: a second full pass must select and update nothing.
	selected2, err := b.drain(ctx)
	if err != nil {
		t.Fatalf("drain (rerun): %v", err)
	}
	if selected2 != 0 {
		t.Fatalf("rerun selected = %d, want 0 (guard must exclude every filled row)", selected2)
	}
	assertStatus(1, "success", "rerun must not change anything")
	assertStatus(5, nil, "rerun must not invent a label")
	assertStatus(7, "success", "rerun must not overwrite an existing label")

	// Retirement on a live database: drop request_logs and prove the job stops
	// quietly (retired, no error) instead of spinning on a missing relation.
	if _, err := pool.Exec(ctx, "DROP TABLE public.request_logs"); err != nil {
		t.Fatalf("drop fixture request_logs: %v", err)
	}
	available, err := b.sourceAvailable(ctx)
	if err != nil {
		t.Fatalf("sourceAvailable after retirement: %v", err)
	}
	if available {
		t.Error("sourceAvailable = true after request_logs was dropped, want false")
	}
	retiredDelta := statusCounterDelta(t, "retired", func() {})
	if _, err := b.drain(ctx); err != nil && err != errRequestStatusSourceRetired {
		t.Errorf("drain after retirement: %v (want errRequestStatusSourceRetired or a clean stop)", err)
	}
	_ = retiredDelta

	// Teardown while both pools are still open; failure is FATAL.
	if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); err != nil {
		t.Fatalf("terminate fixture backends: %v", err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name); err != nil {
		t.Fatalf("FATAL: fixture database %s not dropped: %v", name, err)
	}
	dropped = true
	admin.Close()
	pool.Close()
}
