package v2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// statusRemainingCountTag marks a pgxmock expectation as "the remaining-rows
// count query", so the matcher below recognises it by SHAPE instead of by
// literal text.
const statusRemainingCountTag = "<<remaining-count>>"

// statusRemainingCountRe recognises a single-column bigint count query.
//
// Shape, not text — see statusBackfillQueryMatcher for why.
var statusRemainingCountRe = regexp.MustCompile(`(?is)\bselect\s+(?:0|count\s*\([^()]*\))\s*::\s*bigint\s*$`)

// statusRemainingConstantZeroRe recognises the structural zero that the
// remaining count has become since 890 (the source family is dropped, so
// there is nothing left that could be copied).
var statusRemainingConstantZeroRe = regexp.MustCompile(`(?is)\bselect\s+0\s*::\s*bigint\s*$`)

// statusBackfillQueryMatcher is the pool-level pgxmock matcher for this file.
//
// pgxmock v4 supports only a POOL-level matcher (ExpectedQuery carries no
// QueryMatcher of its own), so a per-expectation rule has to be dispatched
// from the expected string: expectations carrying the marker prefix are matched
// by shape, everything else keeps the default regexp behaviour.
//
// ★★ 2026-10-21 (R65 round, measured): this exists because of a failure
// mode that is much worse than a red assertion.
//
// A regexp expectation pins the SQL TEXT into the fixture, and pgxmock's
// response to a text mismatch is NOT "this assertion turns red":
//
//	count query text changed  ⇒  the ExpectedQuery never matches
//	⇒  it is NOT consumed  ⇒  it stays at the HEAD of the expectation queue
//	⇒  every later call fails with "was not expected"
//	⇒  the product's "probe failed ⇒ back off and retry" path turns that into
//	    a loop that NEVER returns
//	⇒  the only exit left is go test's default 600s timeout.
//
// Measured on 2026-10-21: the whole domains/session/v2 package took
// 606.375s and the one test that never finished was
// TestSessionRequestStatusBackfill_GaugeSampledBeforeFirstDrain. The fixture
// had gone stale because migration 890 rewrote
// sessionRequestStatusRemainingSQL from "SELECT count(*) … JOIN
// public._request_logs" to "SELECT 0::bigint", while the fixture still
// expected the old JOIN text.
//
// ⇒ LESSON: a fixture must pin BEHAVIOUR, never TEXT — and any test that can
// loop forever must carry its own deadline (bounded ctx, see below).
func statusBackfillQueryMatcher(expectedSQL, actualSQL string) error {
	if strings.HasPrefix(expectedSQL, statusRemainingCountTag) {
		return matchStatusRemainingCount(actualSQL)
	}
	return pgxmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
}

// matchStatusRemainingCount accepts any single-column bigint count query.
//
// The value the query returns is the fixture's business, not the query's
// literal text: the mock may report any count, and the assertions check that
// the number reaches the gauge verbatim.
func matchStatusRemainingCount(actualSQL string) error {
	if !statusRemainingCountRe.MatchString(actualSQL) {
		return fmt.Errorf("not a single-column bigint count query: %s", actualSQL)
	}
	return nil
}

// stripSQLComments removes SQL comments (-- line comments and /* */ blocks,
// including nested blocks) from a statement and returns the executable text.
//
// ★★ 2026-10-21 (R65 round): every SQL-text assertion in this package must go
// through it, and the reason is a measured false green.
//
// The constants carry long explanatory comments, and a substring assertion
// over the RAW text can be satisfied by a word that appears only in a
// COMMENT. Measured: the candidate query's contract test asserted
// "AND FALSE" over the raw text; DELETING the clause left the test GREEN,
// because the clause is also NAMED in the comment right above it.
//
// ⇒ A text assertion that does not strip comments is not a gate, it is a
// coincidence detector. The mutation that proved it: M6 in the R65 mutation
// round (removing AND FALSE) reported GREEN, and the fix (strip first) turned
// it RED.
func stripSQLComments(q string) string {
	var out strings.Builder
	out.Grow(len(q))
	for i := 0; i < len(q); {
		switch {
		case q[i] == '\'':
			// Copy a string literal verbatim, honouring the '' escape.
			out.WriteByte(q[i])
			i++
			for i < len(q) {
				out.WriteByte(q[i])
				if q[i] != '\'' {
					i++
					continue
				}
				i++
				if i < len(q) && q[i] == '\'' {
					out.WriteByte(q[i])
					i++
					continue
				}
				break
			}
		case strings.HasPrefix(q[i:], "--"):
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case strings.HasPrefix(q[i:], "/*"):
			depth := 1
			i += 2
			for i < len(q) && depth > 0 {
				switch {
				case strings.HasPrefix(q[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(q[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
		default:
			out.WriteByte(q[i])
			i++
		}
	}
	return out.String()
}

// executableSQL is the stripper plus a trim — what a text assertion should
// look at.
func executableSQL(q string) string {
	return strings.TrimSpace(stripSQLComments(q))
}

// newStatusBackfillMock builds a pgxmock pool wired for the request_status
// backfill seam.
func newStatusBackfillMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherFunc(statusBackfillQueryMatcher)))
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
	mock.ExpectQuery("to_regclass\\('public._request_logs'\\)").
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
// pair plus the request_id whose _request_logs twin carries the label.
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
// UPDATE must carry the guarded `request_status IS NULL` predicate and address
// the row by id + partition_date so the partitioned parent prunes.
//
// ★ 2026-10-21 (R65 round): the "join _request_logs on request_id ONLY" half
// of this contract died with the v1 family. The mock's UPDATE expectation is
// also deliberately the PREFIX "SET request_status" and not the full
// "SET request_status = l.request_status" it used to be: pinning the whole
// text is what turned an implementation change into a 606-second hang (see
// statusBackfillQueryMatcher).
func TestSessionRequestStatusBackfill_FillsBatch(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectStatusTxPrefix(mock, 0, 100)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status").
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
			// ★ 2026-10-21 (R65 round): migration 890 dropped the v1 family, so
			// the JOIN is gone and the statement degenerates to an identity
			// assignment behind AND FALSE. The previous form kept
			// `SET request_status = l.request_status` while removing the JOIN
			// that defined the alias `l` ⇒ the statement could not even be
			// parsed (42P01, measured on a real PG).
			name:  "guard_and_no_v1_source",
			batch: []requestStatusBackfillRow{{id: 42, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_42"}},
			want: []string{
				// identity assignment: shape-compatible and always legal
				"UPDATE public.session_turns t SET request_status = t.request_status",
				"FROM (VALUES",
				"AS v(id, partition_date, request_id)",
				// idempotence guard
				"AND t.request_status IS NULL",
				// partition pruning via pkey (id, partition_date)
				"WHERE t.id = v.id AND t.partition_date = v.partition_date",
				// no-op gate: the candidate set is empty since 890
				"AND FALSE",
			},
			notWant: []string{
				// ★ the v1 family is DROPPED: any reference is a 42P01 at runtime
				"public._request_logs",
				// ★ alias l vanished with the JOIN; naming it is a 42P01 at
				//   PARSE time, independent of AND FALSE
				"l.request_status",
				"ON CONFLICT", "t.tenant_id",
			},
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
			sql = executableSQL(sql)
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
// probe itself distinguishes "_request_logs still there" from "retired" without
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
// retirement guarantee: once _request_logs is gone the job increments `retired`
// exactly once and RETURNS — no drain attempt, no per-tick ERROR spam.
//
// ★ 2026-10-21 (R65 round): the startup count no longer FAILS in the retired
// world — since 890 the remaining count is a constant 0 (the source is gone,
// so there is nothing left that could be copied), so the gauge legitimately
// reads 0. The "a failed count must read -1, never 0" contract is still
// covered, but it belongs to the count-FAILURE path and lives in
// TestSessionRequestStatusBackfill_RemainingGaugeFailureIsNotZero.
func TestSessionRequestStatusBackfill_RetiredStopsWithoutErrorNoise(t *testing.T) {
	mock := newStatusBackfillMock(t)
	// The startup gauge sample fires BEFORE the first probe, so it sees the
	// retired world too: the count answers 0 (nothing left to copy).
	expectStatusRemaining(mock, 0)
	expectStatusProbe(mock, false, true) // retired
	// NOTE: no batch ExpectBegin/SELECT/UPDATE programmed. pgxmock fails the
	// test on any UNEXPECTED call, so if the job tried to drain, this goes red.

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	delta := statusCounterDelta(t, "retired", func() {
		done := make(chan struct{})
		// Bounded ctx: if the fixture ever goes stale again, the job's retry
		// loop must die on a deadline, not on go test's 600s default.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b.Start(ctx)
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
// covers the race where _request_logs is dropped between the probe and the
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
		WillReturnError(assertAnError(`relation "public._request_logs" does not exist`))
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b.Start(ctx)
	b.Start(ctx) // second start is a no-op
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

// TestSessionRequestStatusBackfill_CandidateSQLIsEmptyAfterV1Retirement pins
// the candidate query's post-890 shape.
//
// ★ 2026-10-21 (R65 round): the previous contract here ("the join must be on
// request_id ONLY") died with the v1 family. What replaces it is a stronger
// property: the candidate set is EMPTY (AND FALSE) and the query must not
// name the dropped family at all — naming it is a runtime 42P01, not a subtle
// row-set change. The keyset shape is kept so the drain machinery stays intact.
func TestSessionRequestStatusBackfill_CandidateSQLIsEmptyAfterV1Retirement(t *testing.T) {
	sql := executableSQL(sessionRequestStatusCandidateSQL)
	for _, want := range []string{
		// keyset shape (the machinery's contract)
		"WHERE t.id > $1",
		"ORDER BY t.id",
		"LIMIT $2",
		// the NULL-label predicate is still the only thing that could
		// qualify a row
		"t.request_status IS NULL",
		// ★ the explicit "no source" gate
		"AND FALSE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("candidate SQL missing %q", want)
		}
	}
	for _, notWant := range []string{
		// ★ the v1 family is DROPPED (migration 890): naming it is a 42P01
		"public._request_logs",
	} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(notWant)) {
			t.Errorf("candidate SQL must not contain %q — the label source is retired, "+
				"so the candidate set is empty and the query must not touch the dropped family", notWant)
		}
	}
	// The projection must stay aligned with the table's real columns: a
	// projected column that does not exist is a PARSE-time 42P01, exactly like
	// the alias l that 890 left behind. The real-database gate in
	// session_request_status_backfill_v1_retirement_test.go PREPAREs this
	// statement, which is the authoritative check.
	for _, col := range []string{"t.id", "t.partition_date", "t.tenant_id", "t.request_id"} {
		if !strings.Contains(sql, col) {
			t.Errorf("candidate SQL does not project %q", col)
		}
	}
}

// TestSessionRequestStatusBackfill_SourceProbeSQLUsesToRegclass guards the
// mechanism that makes retirement observable: ::regclass would RAISE on a
// dropped table (turning retirement into ERROR spam), to_regclass returns NULL.
func TestSessionRequestStatusBackfill_SourceProbeSQLUsesToRegclass(t *testing.T) {
	if strings.Contains(executableSQL(sessionRequestStatusSourceProbeSQL), "::regclass") {
		t.Error("probe SQL must use to_regclass (NULL on absent table), not ::regclass (raises)")
	}
	if !strings.Contains(executableSQL(sessionRequestStatusSourceProbeSQL), "to_regclass('public._request_logs')") {
		t.Error("probe SQL must probe _request_logs by to_regclass")
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

// TestSessionRequestStatusBackfill_RealDB is the real-library gate.
//
// ★ 2026-10-21 (R65 round): the gate in this slot used to build a miniature
// _request_logs + session_turns, run the job's batches, and assert that
// joinable NULL rows got their labels copied from their v1 twin. That premise
// died with migration 890 (the v1 family is DROPPED), so the gate now pins
// the behaviour that actually ships:
//
//	(a) every SQL the job issues must PARSE on a real PostgreSQL — this is
//	    where the 42P01 class of defect dies. A statement naming a relation or
//	    an alias that does not exist is rejected at BIND time, even when it
//	    can never match a row: 890 left `SET request_status = l.request_status`
//	    behind after removing the JOIN that defined the alias `l`;
//	(b) the job retires immediately and quietly when the source is gone
//	    (no drain, no error spam, exactly one `retired`);
//	(c) the remaining gauge reads 0 — the structural zero of a retired source;
//	(d) not a single row is written.
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

	// The fixture mirrors production AFTER 890: the session family exists, the
	// v1 label source does NOT.
	if _, err := pool.Exec(ctx, `
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
INSERT INTO public.session_turns (id, partition_date, tenant_id, request_id, request_status) VALUES
	(1,'2026-09-01','tenant_a','req_1',NULL),
	(2,'2026-10-02','tenant_b','req_2',NULL),
	(3,'2026-10-03','tenant_c','req_3','success');
`); err != nil {
		t.Fatalf("create/seed fixture: %v", err)
	}

	// (a) Every statement must PARSE. PREPARE binds without executing, so this
	// is a pure parse/bind check — it writes nothing and touches no row.
	batchSQL, _ := buildRequestStatusUpdateSQL([]requestStatusBackfillRow{
		{id: 42, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_42"},
	})
	for _, stmt := range []struct {
		name string
		sql  string
	}{
		{"rsb_candidate", sessionRequestStatusCandidateSQL},
		{"rsb_remaining", sessionRequestStatusRemainingSQL},
		{"rsb_probe", sessionRequestStatusSourceProbeSQL},
		{"rsb_update", batchSQL},
	} {
		if _, err := pool.Exec(ctx, "PREPARE "+stmt.name+" AS "+stmt.sql); err != nil {
			t.Errorf("PREPARE %s failed: %v\n--- statement ---\n%s", stmt.name, err, stmt.sql)
			continue
		}
		if _, err := pool.Exec(ctx, "DEALLOCATE "+stmt.name); err != nil {
			t.Fatalf("DEALLOCATE %s: %v", stmt.name, err)
		}
	}

	// (b) The job must retire immediately and quietly: the source is gone, so
	// the probe says so and the job returns without draining.
	b := newSessionRequestStatusBackfillForTest(pool, 0, 100, 0)
	runCtx, cancelRun := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRun()
	retired := statusCounterDelta(t, "retired", func() { b.run(runCtx) })
	if retired != 1 {
		t.Errorf("retired delta = %v, want 1 — with the v1 family dropped the job must "+
			"retire exactly once instead of retrying", retired)
	}

	// (c) The gauge is the structural zero of a retired source.
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != 0 {
		t.Errorf("remaining gauge = %v, want 0 (the source is gone ⇒ nothing left to copy)", got)
	}

	// (d) Nothing may be written. A drain on a source-less world selects no
	// candidates, and the NULL labels must survive untouched.
	selected, err := b.drain(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if selected != 0 {
		t.Errorf("selected = %d, want 0 — with no label source there are no candidates", selected)
	}
	var stillNull, labelled int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FILTER (WHERE request_status IS NULL), "+
			"count(*) FILTER (WHERE request_status IS NOT NULL) "+
			"FROM public.session_turns").Scan(&stillNull, &labelled); err != nil {
		t.Fatalf("read labels: %v", err)
	}
	if stillNull != 2 || labelled != 1 {
		t.Errorf("labels after the job = %d NULL / %d non-NULL, want 2 NULL / 1 non-NULL "+
			"(the job must not invent or clear labels)", stillNull, labelled)
	}

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

// ── D9 clause 4: the remaining-rows gauge ────────────────────────────────────
//
// The release precondition for retiring _request_logs is "the backfill is
// done" (decision sheet D9 clause 4, audit §9.156/§9.158.9). A counter of how
// many rows THIS process filled cannot answer that — restarts and multiple
// replicas break the arithmetic, and nothing distinguishes "0 filled because
// nothing is left" from "0 filled because nothing ran". The gauge exists so the
// precondition is a number an operator can read.

// expectIdleBatch programs one drain iteration that finds no candidates: a
// short batch is what marks the candidate set exhausted, and that is the only
// moment the remaining count is worth paying for.
func expectIdleBatch(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(int64(0), 100).
		WillReturnRows(pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"}))
	mock.ExpectRollback()
}

// expectIdleBatchAt is expectIdleBatch for an explicit (cursor, batchSize).
// The cursor must be the previous batch's last id — the drain advances the
// keyset watermark, so a hardcoded 0 only works for the first batch.
func expectIdleBatchAt(mock pgxmock.PgxPoolIface, cursor int64, batchSize int) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WHERE t\\.id > \\$1").
		WithArgs(cursor, batchSize).
		WillReturnRows(pgxmock.NewRows([]string{"id", "partition_date", "tenant_id", "request_id"}))
	mock.ExpectRollback()
}

// remainingTimeoutArg is the value sampleRemaining passes to
// set_config('statement_timeout', $1, true), derived from the same constant
// the code uses — the expectation and the enforced budget cannot drift apart.
func remainingTimeoutArg() string {
	return fmt.Sprintf("%ds", int(sessionRequestStatusRemainingTimeout.Seconds()))
}

// expectStatusRemaining programs the tx-scoped remaining-count sample: BEGIN,
// the two RLS bypass GUCs plus the statement_timeout lift (the SAME prologue
// family as the batch path — the count walks RLS-protected tables and the
// role-default 30s would kill it before its own budget; R31 2026-10-10), the
// count itself, ROLLBACK. Programming the Begin is itself the pin: a
// regression to a bare pool.QueryRow would consume no Begin and fail these
// expectations.
//
// n is the count the MOCK reports — a simulation, not the literal the real
// query produces. It must be allowed to be non-zero: the real remaining count
// has been 0 since 890, and 0 is also the Prometheus zero value, so a gauge
// assertion on 0 cannot tell "sampled" from "never sampled" (a恒绿 assertion).
// A non-zero sentinel is what gives the gauge assertions teeth.
func expectStatusRemaining(mock pgxmock.PgxPoolIface, n int64) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('statement_timeout', \\$1, true\\)").
		WithArgs(remainingTimeoutArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// Matched by SHAPE, not by text — see statusBackfillQueryMatcher.
	mock.ExpectQuery(statusRemainingCountTag).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(n))
	mock.ExpectRollback()
}

// expectStatusRemainingErr is expectStatusRemaining with a failing count.
//
// The "a count failure must read as -1, never 0" contract is independent of
// the query's shape: even though the real query has been a constant 0 since
// 890, a failure still has to be reported honestly.
func expectStatusRemainingErr(mock pgxmock.PgxPoolIface, err error) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('statement_timeout', \\$1, true\\)").
		WithArgs(remainingTimeoutArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(statusRemainingCountTag).
		WillReturnError(err)
	mock.ExpectRollback()
}

// TestSessionRequestStatusBackfill_GaugeSampledBeforeFirstDrain pins the
// startup sample: without it the gauge holds its Prometheus zero for the whole
// active backfill (≈4.2h at defaults), and zero is the release-gate PASS value
// — an operator querying mid-backfill would read "safe to retire" while
// 1.5M rows are still draining. Here the probe then reports the source
// retired, so run() returns without ever draining; the gauge must still carry
// the startup number, proving the sample fired before the first drain.
//
// ★ 2026-10-21 (R65 round): two things changed, and both were load-bearing.
//
//  1. The count is reported by the mock as a NON-ZERO SENTINEL. The real
//     remaining count has been a constant 0 since 890, and 0 is also the
//     Prometheus zero value — a gauge assertion on 0 cannot tell "sampled"
//     from "never sampled", so it would be a恒绿 assertion. The sentinel
//     restores the teeth: the gauge must show the number the count reported.
//
//  2. The ctx is BOUNDED. This is the test that hung for 606s: the stale
//     fixture made the probe fail, the job's "probe failed ⇒ back off and
//     retry" path looped forever, and the only exit was go test's default
//     600s timeout. A one-second fixture regression must not cost the CI
//     ten minutes; with a deadline it fails in seconds and says why.
func TestSessionRequestStatusBackfill_GaugeSampledBeforeFirstDrain(t *testing.T) {
	mock := newStatusBackfillMock(t)
	const startupRemaining int64 = 424242 // sentinel: ≠ 0 and ≠ -1
	// Declare the baseline: the gauge is PACKAGE-LEVEL state, so "the gauge
	// reads 0" only means something if this test put it there. Without the
	// reset, a value left behind by an earlier test would satisfy the
	// assertion below even if the startup sample never fired — a false green
	// that depends on test order.
	requestStatusBackfillRemaining.Set(0)
	expectStatusRemaining(mock, startupRemaining)
	expectStatusProbe(mock, false, false)

	b := newSessionRequestStatusBackfillForTest(mock, time.Millisecond, 100, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retired := statusCounterDelta(t, "retired", func() { b.run(ctx) })
	if retired != 1 {
		t.Fatalf("retired delta = %v, want 1 — the probe reports the source gone, "+
			"so run() must retire exactly once instead of retrying", retired)
	}
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != float64(startupRemaining) {
		t.Errorf("remaining gauge = %v after startup, want %d — if this reads 0 the "+
			"pre-drain sample was dropped (or carried 0, which is indistinguishable "+
			"from never sampled) and the D9 release gate can false-green", got, startupRemaining)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestSessionRequestStatusBackfill_RemainingGaugeReportsIdle(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectIdleBatch(mock)
	expectStatusRemaining(mock, 0)

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	if _, err := b.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != 0 {
		t.Errorf("remaining gauge = %v, want 0 when the count reports none left", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A non-zero count must reach the gauge verbatim — this is the number the
// release check reads, so a scaled or clamped value would silently pass a
// half-finished backfill.
//
// ★ 2026-10-21 (R65 round): the value is now a SENTINEL rather than the old
// business number 1520530. The real remaining count has been 0 since 890, and
// 0 is also the Prometheus zero value — an assertion on 0 cannot tell
// "the count reported 0" from "the count never ran". A sentinel keeps the
// property under test (the count's value reaches the gauge UNSCALED) and
// keeps the assertion meaningful.
func TestSessionRequestStatusBackfill_RemainingGaugeIsVerbatim(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectIdleBatch(mock)
	const want int64 = 314159 // sentinel: ≠ 0 and ≠ -1
	expectStatusRemaining(mock, want)

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	if _, err := b.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != float64(want) {
		t.Errorf("remaining gauge = %v, want %d verbatim", got, want)
	}
}

// The single most important property: an unmeasurable count must NOT read as
// zero. Reporting 0 tells the release gate "backfill complete" on a value
// nobody verified — the exact failure mode this gauge was added to prevent.
func TestSessionRequestStatusBackfill_RemainingGaugeFailureIsNotZero(t *testing.T) {
	mock := newStatusBackfillMock(t)
	expectIdleBatch(mock)
	expectStatusRemainingErr(mock, errors.New("connection reset by peer"))

	b := newSessionRequestStatusBackfillForTest(mock, 0, 100, 0)
	// Telemetry must never block the job.
	if _, err := b.drain(context.Background()); err != nil {
		t.Fatalf("drain returned an error because the gauge count failed: %v", err)
	}
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != -1 {
		t.Errorf("remaining gauge = %v, want -1 on a failed count — 0 would tell "+
			"the D9 release gate to proceed on an unverified number", got)
	}
}

// The remaining count must be paid for EXACTLY ONCE per drain, at the end —
// not per batch. It scans all 1.69M rows, so a per-batch sample would turn a
// rate-limited backfill into a full-table scan loop.
//
// batchSize is 1 here so that a single returned row is a FULL batch (the loop
// only continues when selected == batchSize). With batchSize 100 one row would
// be a short batch and the drain would end immediately.
//
// The assertion is indirect on purpose: exactly one remaining-count expectation
// is programmed. A second (unprogrammed) call would fail and write the -1
// sentinel, so "gauge == 0" proves the count was asked exactly once.
func TestSessionRequestStatusBackfill_RemainingSampledOncePerDrain(t *testing.T) {
	mock := newStatusBackfillMock(t)
	// Batch 1: FULL (1 == batchSize) -> loop continues.
	expectStatusTxPrefix(mock, 0, 1)
	mock.ExpectExec("UPDATE public\\.session_turns t SET request_status").
		WithArgs(int64(42), statusFixedDate(), "req_42").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	// Batch 2: short (0 rows) -> drain ends.
	expectIdleBatchAt(mock, 42, 1)
	// Exactly ONE count query for the whole drain.
	expectStatusRemaining(mock, 0)

	b := newSessionRequestStatusBackfillForTest(mock, 0, 1, 0)
	if _, err := b.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := testutil.ToFloat64(requestStatusBackfillRemaining); got != 0 {
		t.Errorf("remaining gauge = %v, want 0 — a second, unprogrammed sample "+
			"would have failed and written the -1 sentinel, so != 0 means the "+
			"1.69M-row count was paid for more than once per drain", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestSessionRequestStatusBackfill_RemainingSQLContract pins the remaining
// count query's post-890 shape.
//
// ★ 2026-10-21 (R65 round): the previous contract here ("the v1-side IS NOT
// NULL filter must survive") died with the v1 family — the source is DROPPED,
// so the number of rows that could still be copied is structurally 0.
//
// ⚠⚠ The consequence must not be silently inherited: **a gauge that is
// structurally 0 is a PERMANENTLY GREEN release signal.** The D9 clause-4
// gate ("remaining == 0 ⇒ safe to retire") is therefore void, and reading
// this gauge as a release precondition is a mistake — it now answers 0 for
// every possible world. The constant is pinned here so that anyone changing
// it has to confront that, rather than discovering it inside a release window.
func TestSessionRequestStatusBackfill_RemainingSQLContract(t *testing.T) {
	q := executableSQL(sessionRequestStatusRemainingSQL)
	if !statusRemainingConstantZeroRe.MatchString(q) {
		t.Errorf("remaining SQL must be the structural zero (SELECT 0::bigint), got: %s", q)
	}
	// ★ the v1 family is DROPPED (migration 890): naming it is a 42P01
	if strings.Contains(strings.ToLower(q), "public._request_logs") {
		t.Errorf("remaining SQL must not reference the dropped v1 family, got: %s", q)
	}
	// A LIMIT would report a sample, not a total.
	if strings.Contains(strings.ToUpper(q), "LIMIT") {
		t.Error("remaining SQL must count all remaining rows; a LIMIT would let the " +
			"release gate report done while work is left")
	}
}

// TestSessionRequestStatusBackfill_RemainingSQLNoLongerCoversAnySurface is the
// executable form of "the D9 release gate is void".
//
// ★ 2026-10-21 (R65 round): the test in this slot used to assert that the
// gauge covers BOTH storage surfaces (parent + hot). That premise died with
// the v1 family: the count is a constant 0 and mentions no table at all, so
// it cannot be scoped to any surface. The fact is worth pinning precisely
// because it is WHY the release gate must not be trusted any more — a gauge
// that reads 0 for every possible world is green forever.
func TestSessionRequestStatusBackfill_RemainingSQLNoLongerCoversAnySurface(t *testing.T) {
	q := strings.ToLower(executableSQL(sessionRequestStatusRemainingSQL))
	for _, surface := range []string{"public.session_turns", "public.session_turns_hot"} {
		if strings.Contains(q, surface) {
			t.Errorf("remaining SQL references %q — since 890 the count is the constant 0; "+
				"if a surface comes back, the D9 release gate needs an explicit re-derivation "+
				"(a gauge scoped to one surface reads 0 while the other still holds NULL labels)", surface)
		}
	}
}

// ── retired premise ─────────────────────────────────────────────────────────
//
// TestSessionRequestStatusBackfill_RemainingSQLCoversBothSurfaces was REMOVED
// on 2026-10-21 (R65 round), not by accident:
//
//   - It asserted that the remaining-count SQL covers BOTH storage surfaces
//     (public.session_turns + public.session_turns_hot), because the gauge was
//     the D9 clause-4 release gate and a surface-scoped gauge would read 0
//     while the other surface still held NULL labels.
//   - Migration 890 dropped the v1 label source, so the count of "rows that
//     could still be copied" became the CONSTANT 0 — it mentions no table at
//     all and therefore cannot be scoped to any surface. The premise the test
//     defended no longer exists; keeping the assertions would have demanded
//     the impossible (a table-scoped count over a source that is gone).
//   - Its replacement is TestSessionRequestStatusBackfill_RemainingSQLNoLonger
//     CoversAnySurface, which pins the same fact executably: the query names
//     no surface, which is exactly WHY the D9 release gate is void.
//
// ⚠ If a surface ever comes back (a new label source), the release gate must
// be re-derived from scratch — do not resurrect the old assertions blindly.

// TestSessionRequestStatusBackfill_SourceProbeCoversBothSurfaces: the gauge
// selects from session_turns_hot, so a probe that only checks the parent's
// column lets a half-applied 823 turn every gauge tick into an ERROR instead of
// a quiet degradation.
func TestSessionRequestStatusBackfill_SourceProbeCoversBothSurfaces(t *testing.T) {
	p := executableSQL(sessionRequestStatusSourceProbeSQL)
	for _, rel := range []string{"to_regclass('public.session_turns')", "to_regclass('public.session_turns_hot')"} {
		if !strings.Contains(p, rel) {
			t.Errorf("source probe does not check request_status on %s; a half-applied 823 would "+
				"make the remaining gauge raise instead of exiting quietly", rel)
		}
	}
}
