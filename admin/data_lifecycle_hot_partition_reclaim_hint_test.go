package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/require"
)

// §97: the retired v1 hot tables measure count(*)=0 yet hold 291 MB + 59 MB in
// TOAST and index pages (see runbook §97). These gates pin the two properties of
// the operator guidance that make it usable, so the defect cannot come back:
//
//  1. Coverage — every retired v1 hot table carries the hint. The old inline
//     message hardcoded `_request_logs_hot` and silently skipped
//     `_request_logs_bodies_hot`, which holds 83% of the reclaimable space.
//  2. Operation — the hint names TRUNCATE and must not send operators to
//     VACUUM FULL for TOAST space. Plain VACUUM is measured not to return it;
//     only TRUNCATE rewrites the TOAST relation itself.
func TestHotReclaimHintCoversEveryRetiredTableAndNamesTruncate(t *testing.T) {
	retired := make([]string, 0, len(retiredV1HotTables))
	for table := range retiredV1HotTables {
		retired = append(retired, table)
	}
	require.NotEmpty(t, retired, "retired family must not be empty or this gate is vacuous")

	for _, table := range retired {
		hint := hotReclaimHint(table)
		require.NotEmpty(t, hint, "retired table %s must carry a reclaim hint", table)
		require.Contains(t, hint, table, "hint must name the table it is about")
		require.Contains(t, hint, "TRUNCATE",
			"hint for %s must name TRUNCATE: it is the operation that rewrites the TOAST relation", table)
		require.NotContains(t, strings.ToUpper(hint), "VACUUM FULL",
			"hint for %s must not send operators to VACUUM FULL to reclaim TOAST space", table)
		require.Contains(t, hint, "count(*)",
			"hint must state its own precondition — TRUNCATE destroys every row")
	}
}

// A live table with active writes must never receive TRUNCATE advice: the hint
// destroys data. Non-retired hot tables are promoted on a normal schedule.
func TestHotReclaimHintStaysSilentForNonRetiredTables(t *testing.T) {
	for _, table := range []string{
		"usage_ledger_hot", "request_wal_hot", "session_bodies_hot",
		"credit_ledger_hot", "session_censors_hot", "",
	} {
		require.Empty(t, hotReclaimHint(table),
			"table %q is not retired and must not be told to TRUNCATE", table)
	}
}

// Characterization of a known gap (runbook §97), not a target state.
//
// While request_logs writes are disabled, promoteHotRetiredReason fails the job
// before any batch runs, so TotalMigrated never exceeds 0 and the hint is never
// delivered. The hint is therefore only reachable in the one state where the
// cleanup is not wanted. This assertion records that asymmetry so it cannot be
// forgotten: if you make the family reachable again, revisit this test and the
// hint's precondition together.
func TestHotReclaimHintIsUnreachableWhileTheFamilyIsRetired(t *testing.T) {
	withRequestLogsWriteSetting(t, false)
	require.False(t, settings.RequestLogsWriteEnabled())

	for table := range retiredV1HotTables {
		require.NotEmpty(t, promoteHotRetiredReason(table),
			"precondition of this characterization: %s must be gated shut", table)

		job := &HotPromoteJob{TableName: table, RetentionHours: 8, BatchSize: 7, StartedAt: time.Now()}
		db := &retirementPromoteDB{query: func(context.Context, string, ...any) pgx.Row {
			t.Errorf("retired table %s must not reach the DB", table)
			return retirementPromoteRow{}
		}}
		(&Handler{}).runHotPromoteJobWithDB(context.Background(), job, 0, db)
		require.Equal(t, HotJobStatusFailed, job.Status)
		require.Empty(t, job.Warning,
			"no cleanup instruction may be emitted while the family is gated shut")
		require.Zero(t, job.TotalMigrated)
	}
}
