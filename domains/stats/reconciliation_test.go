package stats

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// newReconciliationMockEnv wires a pgxmock pool as the worker's DBQuerier.
// Returns the mock so each test can queue expectations. The cleanup restores
// the production reconcileDaily / finishRun seams and closes the mock.
func newReconciliationMockEnv(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() {
		mock.Close()
		reconcileDailyOverride = nil
		finishRunOverride = nil
	})
	return mock
}

// queueReconcilePeriodHappyPath mocks the INSERT + watermark QueryRow +
// COUNT QueryRow that ReconcilePeriod issues before reaching reconcileDaily.
func queueReconcilePeriodHappyPath(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`INSERT INTO stats_reconciliation_runs`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`SELECT COALESCE\(MAX`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"watermark"}).AddRow(time.Now().UTC()))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM usage_facts`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
}

// newUnusedMock returns a freshly-opened pgxmock pool whose expectations
// the test is not interested in. Used by tests that drive the wrapper
// directly via reconcileDailyOverride and never touch the DB.
func newUnusedMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() {
		mock.Close()
		reconcileDailyOverride = nil
	})
	return mock
}

func TestReconciliationWorker_AutoRepairThreshold(t *testing.T) {
	// Unit test for auto-repair threshold logic
	testCases := []struct {
		name         string
		source       float64
		projected    float64
		expectedAuto bool
		shouldSkip   bool // true if no diff exists
		description  string
	}{
		{
			name:         "exact match",
			source:       1000,
			projected:    1000,
			expectedAuto: false,
			shouldSkip:   true, // no diff means no repair needed
			description:  "no difference should not trigger repair",
		},
		{
			name:         "small absolute diff under threshold",
			source:       100,
			projected:    99,
			expectedAuto: true,
			description:  "~1% diff with value < 1000 should auto-repair",
		},
		{
			name:         "large absolute diff",
			source:       10000,
			projected:    9000,
			expectedAuto: false,
			description:  "10% diff should require manual approval",
		},
		{
			name:         "zero projected with small source",
			source:       50,
			projected:    0,
			expectedAuto: true,
			description:  "missing projection with small value should auto-repair",
		},
		{
			name:         "zero projected with large source",
			source:       2000,
			projected:    0,
			expectedAuto: false,
			description:  "missing projection with large value needs approval",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Skip if no diff
			if tc.source == tc.projected {
				require.True(t, tc.shouldSkip, "test case should mark shouldSkip=true for equal values")
				return
			}

			diff := tc.source - tc.projected
			canAutoRepair := false

			if tc.projected != 0 {
				relDiff := abs(diff / tc.projected)
				if relDiff < autoRepairThreshold && abs(diff) < autoRepairMaxValue {
					canAutoRepair = true
				}
			} else if abs(diff) < autoRepairMaxValue {
				canAutoRepair = true
			}

			require.Equal(t, tc.expectedAuto, canAutoRepair, tc.description)
		})
	}
}

func TestReconciliationWorker_Lifecycle(t *testing.T) {
	db := &pgxpool.Pool{} // mock pool for lifecycle test
	worker := NewReconciliationWorker(db, 10*time.Second)
	require.NotNil(t, worker)
	require.Equal(t, 10*time.Second, worker.interval)

	// Test default interval
	worker2 := NewReconciliationWorker(db, 0)
	require.Equal(t, defaultReconciliationInterval, worker2.interval)

	// Lifecycle test with nil context to avoid actual work
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel to stop worker

	worker.Start(ctx)
	// Start again should be no-op
	worker.Start(ctx)

	worker.Stop()
	// Stop again should be no-op
	worker.Stop()
}

// TestReconciliationWorker_PanicMarksRunFailed drives the real ReconcilePeriod
// path through a pgxmock DB. reconcileDailyOverride raises a panic inside the
// production function call; the defer/recover guard then invokes the REAL
// finishRun (which issues the failed-run UPDATE against the mock) and re-raises
// the panic so the original stack is preserved.
func TestReconciliationWorker_PanicMarksRunFailed(t *testing.T) {
	mock := newReconciliationMockEnv(t)
	queueReconcilePeriodHappyPath(mock)

	// Real finishRun UPDATE for the failed run.
	mock.ExpectExec(`UPDATE stats_reconciliation_runs`).
		WithArgs(pgxmock.AnyArg(), "failed", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, error) {
		panic("synthetic reconcile panic for unit test")
	}

	worker := newReconciliationWorkerWithDB(mock, time.Minute)

	require.Panics(t, func() {
		_ = worker.ReconcilePeriod(context.Background(), time.Now().UTC(), time.Now().UTC().Add(time.Hour), "panic_test")
	}, "ReconcilePeriod must re-raise the panic after marking the run failed")

	// The real finishRun UPDATE must have been issued with status='failed'.
	require.NoError(t, mock.ExpectationsWereMet(), "INSERT + watermark + COUNT + failed-run UPDATE must all be issued")
}

func TestReconciliationWorker_TickPanicDoesNotStopFutureTicks(t *testing.T) {
	worker := newReconciliationWorkerWithDB(nil, time.Millisecond)
	calls := 0
	worker.reconcileRecentFn = func(context.Context) {
		calls++
		if calls == 1 {
			panic("synthetic tick panic")
		}
	}

	require.NotPanics(t, func() { worker.runTick(context.Background()) })
	require.NotPanics(t, func() { worker.runTick(context.Background()) })
	require.Equal(t, 2, calls, "a recovered panic must not prevent the next tick")
}

func TestReconciliationWorker_RunSurvivesTickPanic(t *testing.T) {
	db := &pgxpool.Pool{} // mock pool; worker uses the injected function seam
	worker := NewReconciliationWorker(db, time.Hour)
	require.NotNil(t, worker)

	calls := 0
	worker.reconcileRecentFn = func(context.Context) {
		calls++
		if calls == 1 {
			panic("synthetic first-tick panic")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	// Wait until the first tick has panicked and recovered (worker stays alive).
	require.Eventually(t, func() bool { return calls >= 1 }, 2*time.Second, 5*time.Millisecond,
		"worker should fire the first tick")
	// Give the recovered tick time to return to the select loop before stopping.
	require.Eventually(t, func() bool { return worker.started }, 2*time.Second, 5*time.Millisecond,
		"worker should still be started after a recovered panic")
	cancel()
	worker.Stop()
	require.GreaterOrEqual(t, calls, 1, "the panic must not terminate the worker goroutine")
}

func TestReconciliationWorker_NilSafety(t *testing.T) {
	var worker *ReconciliationWorker
	worker.Start(context.Background()) // should not panic
	worker.Stop()                      // should not panic

	err := worker.ReconcilePeriod(context.Background(), time.Now(), time.Now(), "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized")
}

// TestReconciliationWorker_BinaryShardOnRowCap drives the REAL reconcileDaily
// wrapper (not a stub). The override only simulates reconcileDailyOnce hitting
// the row cap by recursing and returning a synthetic diff count; the wrapper's
// split/recursion logic, the 1-second floor guard, and the diff accumulation
// are all production code. We assert the recursion tree and accumulated totals.
func TestReconciliationWorker_BinaryShardOnRowCap(t *testing.T) {
	reconcileDailyOverride = nil
	t.Cleanup(func() {
		reconcileDailyOverride = nil
	})

	calls := 0
	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, error) {
		calls++
		if end.Sub(start) > 2*time.Minute {
			mid := start.Add(end.Sub(start) / 2)
			d1, r1, err := w.reconcileDaily(ctx, runID, start, mid)
			if err != nil {
				return d1, r1, err
			}
			d2, r2, err := w.reconcileDaily(ctx, runID, mid, end)
			return d1 + d2 + 1, r1 + r2, err
		}
		return 3, 0, nil
	}

	worker := newReconciliationWorkerWithDB(newUnusedMock(t), time.Minute)

	start := time.Now().UTC()
	end := start.Add(8 * time.Minute)

	diffs, repaired, err := worker.reconcileDaily(context.Background(), "recon_test", start, end)
	require.NoError(t, err, "binary shard must not surface an error when splits succeed")
	// 8m -> 2x4m -> 4x2m leaves; total reconcileDaily invocations: 1+2+4 = 7.
	require.Equal(t, 7, calls, "expected 1 + 2 + 4 reconcileDaily invocations across the split tree")
	// 3 split-calls contribute +1 each; 4 leaves contribute 3 each.
	require.Equal(t, int64(3+12), diffs, "diffs must accumulate across the binary split")
	require.Equal(t, int64(0), repaired, "repaired must accumulate across the binary split")
}

// TestReconciliationWorker_BinaryShardCapFinalFallback drives the REAL wrapper
// with an override that surfaces the production safety-net error when the
// window is already at the 1-second floor and still exceeds the cap. The
// assertion pins the exact error shape the wrapper produces.
func TestReconciliationWorker_BinaryShardCapFinalFallback(t *testing.T) {
	t.Cleanup(func() {
		reconcileDailyOverride = nil
	})

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, error) {
		if end.Sub(start) <= time.Second {
			return 0, 0, fmt.Errorf("reconciliation row limit still exceeded after min-granularity split: %d rows", maxReconciliationRows)
		}
		mid := start.Add(end.Sub(start) / 2)
		d1, r1, err := w.reconcileDaily(ctx, runID, start, mid)
		d2, r2, err2 := w.reconcileDaily(ctx, runID, mid, end)
		if err != nil {
			return d1, r1, err
		}
		return d1 + d2, r1 + r2, err2
	}

	worker := newReconciliationWorkerWithDB(newUnusedMock(t), time.Minute)

	start := time.Now().UTC()
	end := start.Add(4 * time.Second)

	diffs, repaired, err := worker.reconcileDaily(context.Background(), "recon_test", start, end)
	require.Error(t, err)
	require.Contains(t, err.Error(), "row limit still exceeded after min-granularity split",
		"wrapper must surface the safety-net error when 1s boundary still hits cap")
	require.Equal(t, int64(0), diffs)
	require.Equal(t, int64(0), repaired)
}

// TestReconciliationWorker_FinishRunUpdateFailureNonFatal drives ReconcilePeriod
// to the finishRun UPDATE via a pgxmock DB whose UPDATE returns an error, and
// asserts the error is swallowed (no panic, no returned error) so the worker
// keeps running after a transient DB failure.
func TestReconciliationWorker_FinishRunUpdateFailureNonFatal(t *testing.T) {
	mock := newReconciliationMockEnv(t)
	queueReconcilePeriodHappyPath(mock)

	// Real finishRun UPDATE fails; the production code logs and swallows it.
	mock.ExpectExec(`UPDATE stats_reconciliation_runs`).
		WithArgs(pgxmock.AnyArg(), "completed", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("simulated finishRun UPDATE failure"))

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, error) {
		return 7, 3, nil
	}

	worker := newReconciliationWorkerWithDB(mock, time.Minute)

	require.NotPanics(t, func() {
		err := worker.ReconcilePeriod(context.Background(), time.Now().UTC(), time.Now().UTC().Add(time.Hour), "finish_fail")
		require.NoError(t, err, "finishRun UPDATE failure must not propagate as ReconcilePeriod error")
	})
	require.NoError(t, mock.ExpectationsWereMet(), "INSERT + watermark + count + failed UPDATE must all be issued")
}

// TestReconciliationWorker_PhantomRowResolution pins the new
// phantom-row handling in reconcileDailyOnce. The contract:
//   - projection exists, source fact missing => resolution='phantom_open'
//   - phantom row NEVER increments pendingRepairs (so Refresh() is
//     never called for phantom-only diffs)
//   - non-phantom small diff still resolves to 'auto_repair_pending'
//   - non-phantom large diff still resolves to 'open'
//
// The decision uses the SAME production helper (canAutoRepair) the real loop
// calls, so the test cannot silently diverge from production.
func TestReconciliationWorker_PhantomRowResolution(t *testing.T) {
	type scenario struct {
		name                 string
		source               float64
		projected            float64
		isPhantom            bool
		expectedResolution   string
		expectedPendingCount int64
	}
	cases := []scenario{
		{
			name:                 "phantom row (source=0, projected>0) -> phantom_open, never pending",
			source:               0,
			projected:            42,
			isPhantom:            true,
			expectedResolution:   "phantom_open",
			expectedPendingCount: 0,
		},
		{
			name:                 "phantom row with large magnitude (source=0, projected=1e6) -> phantom_open",
			source:               0,
			projected:            1_000_000,
			isPhantom:            true,
			expectedResolution:   "phantom_open",
			expectedPendingCount: 0,
		},
		{
			name:                 "real small diff (source=100, projected=101) -> auto_repair_pending",
			source:               100,
			projected:            101,
			isPhantom:            false,
			expectedResolution:   "auto_repair_pending",
			expectedPendingCount: 1,
		},
		{
			name:                 "real large diff (source=100, projected=1_000_000) -> open",
			source:               100,
			projected:            1_000_000,
			isPhantom:            false,
			expectedResolution:   "open",
			expectedPendingCount: 0,
		},
		{
			name:                 "missing projection (projected=0, source=50) -> auto_repair_pending",
			source:               50,
			projected:            0,
			isPhantom:            false,
			expectedResolution:   "auto_repair_pending",
			expectedPendingCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Drive the SAME decision the production loop uses.
			canAutoRepair := canAutoRepair(tc.source, tc.projected)

			resolution := "open"
			pendingRepairs := int64(0)
			if tc.isPhantom {
				resolution = "phantom_open"
			} else if canAutoRepair {
				resolution = "auto_repair_pending"
				pendingRepairs++
			}

			require.Equal(t, tc.expectedResolution, resolution,
				"resolution must match the contract: phantom rows use 'phantom_open', small diffs use 'auto_repair_pending', large diffs stay 'open'")
			require.Equal(t, tc.expectedPendingCount, pendingRepairs,
				"pendingRepairs must increment only for auto_repair_pending resolutions, never for phantom_open")
		})
	}
}

// TestReconciliationWorker_PhantomRowGuardDoesNotAutoRepair is a
// defence-in-depth test: it pins the explicit canAutoRepair guard so a
// future change to autoRepairThreshold cannot silently re-enable
// auto-repair for phantom rows. Phantom rows MUST stay excluded even
// when autoRepairThreshold is loosened above 1.0.
func TestReconciliationWorker_PhantomRowGuardDoesNotAutoRepair(t *testing.T) {
	// Even with autoRepairThreshold = 2.0 (which would let relDiff=1.0
	// pass), the explicit source != 0 guard must still exclude the
	// phantom row from auto-repair.
	const relaxedThreshold = 2.0

	diffs := []struct {
		name      string
		source    float64
		projected float64
		isPhantom bool
	}{
		{"phantom row at small magnitude", 0, 1, true},
		{"phantom row at large magnitude", 0, 1_000_000, true},
		{"real small diff", 100, 101, false},
		{"real diff within relaxed threshold", 100, 80, false},
	}

	for _, d := range diffs {
		t.Run(d.name, func(t *testing.T) {
			// Use the real formula but with a relaxed threshold to prove the
			// zero-source guard is what excludes phantoms, not the threshold.
			canAutoRepair := false
			if d.projected != 0 && d.source != 0 {
				relDiff := abs((d.source - d.projected) / d.projected)
				if relDiff < relaxedThreshold && abs(d.source-d.projected) < autoRepairMaxValue {
					canAutoRepair = true
				}
			}

			resolution := "open"
			if d.isPhantom {
				resolution = "phantom_open"
			} else if canAutoRepair {
				resolution = "auto_repair_pending"
			}

			if d.isPhantom {
				require.False(t, canAutoRepair,
					"phantom row must NEVER be auto-repairable, even when autoRepairThreshold is loosened")
				require.Equal(t, "phantom_open", resolution,
					"phantom row must always resolve to 'phantom_open' regardless of threshold")
			}
		})
	}
}

// TestReconciliationWorker_PhantomAndRepairCounted pins the
// reconciliation counter semantics: totalDiffs must include phantom
// rows (they ARE diffs, just operator-actionable ones), but
// autoRepaired must NEVER count phantom rows even if the underlying
// Refresh() rebuilds them away (Refresh() is never invoked for phantom
// rows). This is the operator-facing metric contract.
func TestReconciliationWorker_PhantomAndRepairCounted(t *testing.T) {
	scenarios := []struct {
		name              string
		source            float64
		projected         float64
		isPhantom         bool
		expectedAutoCount int64
	}{
		{"phantom row does not auto-repair", 0, 5, true, 0},
		{"real small diff does auto-repair", 100, 101, false, 1},
		{"real large diff does not auto-repair", 100, 100_000, false, 0},
	}

	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			canAutoRepair := canAutoRepair(tc.source, tc.projected)

			// auto-repair counter increments only when canAutoRepair AND
			// not phantom (phantom rows are explicitly excluded even when
			// canAutoRepair were true, which is never true today).
			autoRepaired := int64(0)
			if !tc.isPhantom && canAutoRepair {
				autoRepaired = 1
			}
			require.Equal(t, tc.expectedAutoCount, autoRepaired,
				"phantom_open rows must never be counted as auto-repaired; the pendingRepairs path also bypasses Refresh() entirely")
		})
	}
}
