package bg

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// TestCheckMVConsistency_ViewMissing verifies that CheckMVConsistency returns
// a zero result (no error) when the view doesn't exist (migration 632 not
// applied, or traffic-only role without database). Consumers already fall back
// to base queries, so this is a skip, not a failure.
func TestCheckMVConsistency_ViewMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// pg_matviews check returns false (view not present)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("routing_analytics_7d").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	res, err := CheckMVConsistency(context.Background(), mock, "routing_analytics_7d")
	require.NoError(t, err)
	require.False(t, res.ViewExists, "missing view must be marked skipped")
	require.Equal(t, float64(0), res.MaxPct)
	require.Equal(t, int64(0), res.MaxAbs)
	require.Equal(t, 0, res.BreachCount)
	require.Equal(t, 0, res.DiffRowCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCheckMVConsistency_NoDrift verifies the happy path: view exists, but
// the FULL OUTER JOIN query returns zero rows (perfect consistency).
func TestCheckMVConsistency_NoDrift(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// pg_matviews check returns true
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("routing_analytics_7d").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	// FULL OUTER JOIN returns no rows (no drift)
	mock.ExpectQuery("WITH mv_data AS").
		WillReturnRows(pgxmock.NewRows([]string{
			"task_type", "model", "mv_count", "base_count", "abs_diff", "diff_pct",
		}))

	res, err := CheckMVConsistency(context.Background(), mock, "routing_analytics_7d")
	require.NoError(t, err)
	require.True(t, res.ViewExists)
	require.Equal(t, 0.0, res.MaxPct)
	require.Equal(t, int64(0), res.MaxAbs)
	require.Equal(t, 0, res.BreachCount)
	require.Equal(t, 0, res.DiffRowCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCheckMVConsistency_SmallDrift verifies drift detection when a few
// buckets differ, but only one exceeds both thresholds (5% AND abs > 100).
func TestCheckMVConsistency_SmallDrift(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("routing_analytics_7d").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	// Two diff rows:
	//   1. unknown/gpt-4o: mv=1000, base=1050, abs=50, pct=4.76 (below 5% threshold, no breach)
	//   2. __specified__/claude-opus-4: mv=200, base=250, abs=50, pct=20 (above 5% but abs < 100, no breach)
	//   3. unknown/glm-5: mv=10000, base=10800, abs=800, pct=7.4 (both > threshold, BREACH)
	mock.ExpectQuery("WITH mv_data AS").
		WillReturnRows(pgxmock.NewRows([]string{
			"task_type", "model", "mv_count", "base_count", "abs_diff", "diff_pct",
		}).
			AddRow("unknown", "gpt-4o", int64(1000), int64(1050), int64(50), 4.76).
			AddRow("__specified__", "claude-opus-4", int64(200), int64(250), int64(50), 20.0).
			AddRow("unknown", "glm-5", int64(10000), int64(10800), int64(800), 7.4))

	res, err := CheckMVConsistency(context.Background(), mock, "routing_analytics_7d")
	require.NoError(t, err)
	require.True(t, res.ViewExists)
	require.Equal(t, 20.0, res.MaxPct, "max_pct should be the largest diff_pct across all rows")
	require.Equal(t, int64(800), res.MaxAbs, "max_abs should be the largest abs_diff")
	require.Equal(t, 1, res.BreachCount, "only the glm-5 row exceeds both thresholds")
	require.Equal(t, 3, res.DiffRowCount, "three rows had non-zero diff")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCheckMVConsistency_QueryError verifies that a SQL error (connection lost,
// query timeout, etc.) is propagated so the caller can increment the consistency
// error counter and skip updating the last_success timestamp.
func TestCheckMVConsistency_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("routing_analytics_7d").
		WillReturnError(context.DeadlineExceeded)

	res, err := CheckMVConsistency(context.Background(), mock, "routing_analytics_7d")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pg_matviews existence check")
	require.Equal(t, MVConsistencyResult{}, res)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCheckMVConsistency_UnsupportedView verifies that only routing_analytics_7d
// and routing_audit_summary_7d are currently supported. Other views return zero
// result with a warning log (not an error).
func TestCheckMVConsistency_UnsupportedView(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("unsupported_view").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	// No further query expected — unsupported view short-circuits
	res, err := CheckMVConsistency(context.Background(), mock, "unsupported_view")
	require.NoError(t, err)
	require.Equal(t, MVConsistencyResult{}, res)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCheckMVConsistency_AuditSummary verifies the audit summary view's
// tenant-level drift detection (migration 632's second MV). The query structure
// is similar but groups by tenant_id instead of (task_type, model).
func TestCheckMVConsistency_AuditSummary(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("routing_audit_summary_7d").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	// tenant-abc has drift: mv=5000, base=5550, abs=550, pct=9.91 (both > threshold, BREACH)
	// NULL-coalesced tenant ('') has small drift: mv=10000, base=10150, abs=150, pct=1.5 (pct below threshold, no breach)
	mock.ExpectQuery("WITH mv_data AS").
		WillReturnRows(pgxmock.NewRows([]string{
			"dim1", "dim2", "mv_count", "base_count", "abs_diff", "diff_pct",
		}).
			AddRow("tenant-abc", "", int64(5000), int64(5550), int64(550), 9.91).
			AddRow("", "", int64(10000), int64(10150), int64(150), 1.5))

	res, err := CheckMVConsistency(context.Background(), mock, "routing_audit_summary_7d")
	require.NoError(t, err)
	require.True(t, res.ViewExists)
	require.Equal(t, 9.91, res.MaxPct)
	require.Equal(t, int64(550), res.MaxAbs)
	require.Equal(t, 1, res.BreachCount, "only the tenant-abc row exceeds both thresholds")
	require.Equal(t, 2, res.DiffRowCount)
	require.NoError(t, mock.ExpectationsWereMet())
}
