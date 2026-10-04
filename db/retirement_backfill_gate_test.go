package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackfillBlocksRetirement reports whether a column that the session family can
// only fill by copying from v1 is still waiting for that copy.
//
// ⚠️ §9.223. The constraint "run backfill_final_success_marks.sql /
// backfill_client_protocol.sql **before** request_logs is dropped" lived in a
// SQL script's header comment, and on 252 both scripts had never been run —
// `session_turns.is_final_success` and `client_protocol` were 100% NULL over
// all 817,140 rows while the v1 side was 100% populated and 100% matchable.
// So the ordering was never enforced anywhere a build could catch it.
//
// The shape of the trap is asymmetric and that is what makes it worth a gate:
// the backfill is **one-shot and idempotent** (`is_final_success IS NOT TRUE`
// guard), it copies from a source that is **itself on a monthly DROP list**, and
// the coverage it can achieve is bounded by how much of the session family the
// source still covers at all — 1.225% and 2.890% measured on 252. Running it
// later gets strictly less; running it after the DROP gets nothing.
//
// Why this is a separate gate rather than another row in
// TestSessionFamilyColumnAvailability: that test asserts the measured set equals
// RetirementUnservableColumns, so a column that goes empty *keeps it passing*.
// It is a registry-consistency check. The question "may we drop the source yet"
// is a different question and had no home.
//
// sourceExists=false is the retired case and is deliberately not a block:
// once v1 is gone there is nothing left to copy from, and the column is simply
// what it is. The gate is about the window while the window is still open.
func BackfillBlocksRetirement(col string, sourceExists bool, sessionNonNull, sessionTotal int64, thresholdPP float64) (bool, string) {
	if !sourceExists {
		return false, ""
	}
	if sessionTotal == 0 {
		// No session rows to fill: nothing is being lost by not running it, and
		// calling that a block would fire on an empty deployment.
		return false, ""
	}
	pp := 100 * float64(sessionNonNull) / float64(sessionTotal)
	if pp < thresholdPP {
		return true, fmt.Sprintf(
			"%s is %.4f%% filled on the session side (%d/%d rows) while request_logs still exists; "+
				"its only source is the v1 copy, and the backfill is a one-shot repair that becomes "+
				"impossible once request_logs is dropped. Run sql/scripts/backfill_%s before retiring v1.",
			col, pp, sessionNonNull, sessionTotal, backfillScriptFor(col))
	}
	return false, ""
}

// backfillScriptFor maps a column to the script that fills it. Kept as a switch
// rather than a string concat so an unmapped column fails a test instead of
// producing a filename nobody can run.
func backfillScriptFor(col string) string {
	switch col {
	case "is_final_success":
		return "final_success_marks.sql"
	case "client_protocol":
		return "client_protocol.sql"
	default:
		return "UNKNOWN_COLUMN_NO_SCRIPT"
	}
}

// RetirementBackfilledColumns are the columns whose session-side values can
// only come from a v1 copy, and which therefore must be backfilled while v1
// still exists. work_type is deliberately absent: §9.216.1 measured its v1
// fill at 1.93%, so a copy would move ~2% of rows and the column is already
// registered as unservable. Listing it here would produce a permanent red that
// nobody can act on.
var RetirementBackfilledColumns = []string{"is_final_success", "client_protocol"}

// backfillFillThresholdPP is the session-side fill below which the copy has not
// happened. It is a threshold rather than an exact zero for the same reason
// §9.209 gave: the printed rate and the classifier must use the same precision,
// or a column holding a handful of rows reads as "0.00%" and "not empty" at the
// same time. 0.005% is that same constant.
const backfillFillThresholdPP = 0.005

// TestBackfillBlocksRetirement_PositiveAndNegative is the control pair.
//
// A gate that has only ever been observed not firing is indistinguishable from
// a gate that cannot fire, and this one guards a destructive, irreversible
// ordering. Both directions are asserted, plus the two edges that decide
// whether it is usable at all: the source being gone, and an empty deployment.
func TestBackfillBlocksRetirement_PositiveAndNegative(t *testing.T) {
	const total = 817140 // session_turns row count measured on 252, §9.223
	cases := []struct {
		name          string
		col           string
		sourceExists  bool
		nonNull       int64
		total         int64
		wantBlocked   bool
		wantSubstring string
	}{
		{
			// Production as measured on 252 on 2026-10-05: both columns 100% NULL.
			name:          "unrun backfill with the source still present",
			col:           "is_final_success",
			sourceExists:  true,
			nonNull:       0,
			total:         total,
			wantBlocked:   true,
			wantSubstring: "sql/scripts/backfill_final_success_marks.sql before retiring v1",
		},
		{
			name:          "unrun client_protocol with the source still present",
			col:           "client_protocol",
			sourceExists:  true,
			nonNull:       0,
			total:         total,
			wantBlocked:   true,
			wantSubstring: "sql/scripts/backfill_client_protocol.sql before retiring v1",
		},
		{
			// Negative control: the backfill has run. Coverage is still small —
			// 1.225% is all 252 can reach — but non-zero, and the gate must not
			// demand 100% or it would be un-actionable forever.
			name:         "backfill already ran (partial coverage is expected)",
			col:          "is_final_success",
			sourceExists: true,
			nonNull:      10006,
			total:        total,
			wantBlocked:  false,
		},
		{
			// Negative control: v1 is already gone. There is nothing to copy
			// from, so the column is simply what it is — blocking here would
			// make the gate un-clearable after the very event it guards.
			name:         "source already retired",
			col:          "is_final_success",
			sourceExists: false,
			nonNull:      0,
			total:        total,
			wantBlocked:  false,
		},
		{
			// Negative control: an empty deployment. No rows means no loss.
			name:         "no session rows at all",
			col:          "client_protocol",
			sourceExists: true,
			nonNull:      0,
			total:        0,
			wantBlocked:  false,
		},
		{
			// Edge: just under the threshold must block, just over must not.
			// Same constant on both sides of the comparison, per §9.209.
			name:          "just under the threshold",
			col:           "client_protocol",
			sourceExists:  true,
			nonNull:       4, // 4/817140 = 0.00049% < 0.005%
			total:         total,
			wantBlocked:   true,
			wantSubstring: "before retiring v1",
		},
		{
			name:         "just over the threshold",
			col:          "client_protocol",
			sourceExists: true,
			nonNull:      41, // 41/817140 = 0.00502% > 0.005%
			total:        total,
			wantBlocked:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocked, why := BackfillBlocksRetirement(tc.col, tc.sourceExists, tc.nonNull, tc.total, backfillFillThresholdPP)
			if blocked != tc.wantBlocked {
				t.Fatalf("BackfillBlocksRetirement = %v, want %v (why=%q)", blocked, tc.wantBlocked, why)
			}
			if tc.wantSubstring != "" && !strings.Contains(why, tc.wantSubstring) {
				t.Errorf("message %q does not contain %q — the operator needs a runnable command, "+
					"not just a diagnosis", why, tc.wantSubstring)
			}
			if !tc.wantBlocked && why != "" {
				t.Errorf("no block expected, but got reason %q", why)
			}
		})
	}
}

// TestRetirementBlockedByUnrunBackfills applies the gate to a real database.
//
// It is a separate function from the availability instrument on purpose: that
// one asserts a registry matches a measurement, this one asserts an ordering
// that nothing else in the repository enforces.
func TestRetirementBlockedByUnrunBackfills(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var sourceExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema='public' AND table_name='request_logs')`).
		Scan(&sourceExists); err != nil {
		t.Fatalf("probe source table: %v", err)
	}

	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_turns`).Scan(&total); err != nil {
		t.Fatalf("count session_turns: %v", err)
	}
	t.Logf("request_logs still present: %v   session_turns rows: %d", sourceExists, total)

	var blocked []string
	for _, col := range RetirementBackfilledColumns {
		var nonNull int64
		if err := pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(%s) FROM session_turns`, col)).Scan(&nonNull); err != nil {
			t.Fatalf("count %s: %v", col, err)
		}
		pp := 0.0
		if total > 0 {
			pp = 100 * float64(nonNull) / float64(total)
		}
		t.Logf("%-20s %10.4f%%  (%d/%d rows)", col, pp, nonNull, total)
		if isBlocked, why := BackfillBlocksRetirement(col, sourceExists, nonNull, total, backfillFillThresholdPP); isBlocked {
			blocked = append(blocked, why)
		}
	}

	if len(blocked) > 0 {
		t.Errorf("retiring request_logs is blocked by %d unrun backfill(s):\n  - %s\n"+
			"Each of these columns is filled by copying from v1, the copy is a one-shot idempotent "+
			"repair, and v1 is itself on a monthly DROP list (RETAIN_MONTHS=2 on 252). The coverage "+
			"reachable shrinks every day: measured on 252 the two scripts together can reach only "+
			"1.225%% and 2.890%% of session_turns, because the v1 source no longer covers the older "+
			"part of the session family at all. Running them after the DROP recovers nothing.",
			len(blocked), blocked[0])
		for _, extra := range blocked[1:] {
			t.Logf("  - %s", extra)
		}
	}
}
