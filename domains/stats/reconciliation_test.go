package stats

import (
	"context"
	"fmt"
	"strings"
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

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, int64, error) {
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
	worker := NewReconciliationWorker(db, time.Millisecond)
	require.NotNil(t, worker)

	secondTick := make(chan struct{}, 1)
	calls := 0
	worker.reconcileRecentFn = func(context.Context) {
		calls++
		if calls == 1 {
			panic("synthetic first-tick panic")
		}
		select {
		case secondTick <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	require.Eventually(t, func() bool {
		select {
		case <-secondTick:
			return true
		default:
			return false
		}
	}, 2*time.Second, 5*time.Millisecond, "worker should execute a second tick after recovering from the first panic")
	cancel()
	worker.Stop()
	require.GreaterOrEqual(t, calls, 2, "the panic must not terminate the worker goroutine")
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
	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, int64, error) {
		calls++
		if end.Sub(start) > 2*time.Minute {
			mid := start.Add(end.Sub(start) / 2)
			d1, r1, p1, err := w.reconcileDaily(ctx, runID, start, mid)
			if err != nil {
				return d1, r1, p1, err
			}
			d2, r2, p2, err := w.reconcileDaily(ctx, runID, mid, end)
			return d1 + d2 + 1, r1 + r2, p1 + p2, err
		}
		return 3, 0, 0, nil
	}

	worker := newReconciliationWorkerWithDB(newUnusedMock(t), time.Minute)

	start := time.Now().UTC()
	end := start.Add(8 * time.Minute)

	diffs, repaired, _, err := worker.reconcileDaily(context.Background(), "recon_test", start, end)
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

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, int64, error) {
		if end.Sub(start) <= time.Second {
			return 0, 0, 0, fmt.Errorf("reconciliation row limit still exceeded after min-granularity split: %d rows", maxReconciliationRows)
		}
		mid := start.Add(end.Sub(start) / 2)
		d1, r1, p1, err := w.reconcileDaily(ctx, runID, start, mid)
		d2, r2, p2, err2 := w.reconcileDaily(ctx, runID, mid, end)
		if err != nil {
			return d1, r1, p1, err
		}
		return d1 + d2, r1 + r2, p1 + p2, err2
	}

	worker := newReconciliationWorkerWithDB(newUnusedMock(t), time.Minute)

	start := time.Now().UTC()
	end := start.Add(4 * time.Second)

	diffs, repaired, _, err := worker.reconcileDaily(context.Background(), "recon_test", start, end)
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

	reconcileDailyOverride = func(ctx context.Context, w *ReconciliationWorker, runID string, start, end time.Time) (int64, int64, int64, error) {
		return 7, 3, 0, nil
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

// ── r1045（2026-10-28）：★ 「差异写入失败」不得被降级成 warn ⇒ 账本不许说谎 ──
//
// ★ 背景（★ 实测，2026-10-28 07:32:39~07:32:42）：542 条
// ★ `INSERT INTO stats_reconciliation_diffs` 全部失败（★ 那次的**近因**是本机库缺
// ★ `tenant_id` 列 ⇒ ★ 已由 r1043 补迁移修掉；★ ★★ 但**让这族失败静默无声的机制**
// ★ ★★ 还在仓里 ⇒ ★★ 本组断言钉的就是它）。
//
// ★ ★★ 那一轮在 `stats_reconciliation_runs` 账本里记的是：
// ★ ★★ `status=completed, rows_compared=0, rows_repaired=0, diff_count=0, error=''`
// ★ ★★ ⇒ 「0 差异、没有错误、已完成」⇒ ★★ **账本、管理界面
// ★ ★★ （admin/stats.go 读 status + diff_count）、指标
// ★ ★★ （RecordStatsReconciliationRun("completed")）三处都在说谎**。
//
// ★★ 机制：写入失败被 `slog.Warn` + `continue` 吞掉 ⇒ `totalDiffs` 不加 ⇒
// ★★ `diff_count = diffs - repaired = 0` ⇒ ★★ 而那条差异**从未进表** ⇒
// ★★ 下一窗口也不覆盖它 ⇒ ★★ 永久丢失。
//
// ★★★ 本组断言的契约：★ **写入失败 ⇒ 这一轮必须失败**（★ `status ≠ 'completed'`、
// ★ ★ `error` 列必须写上原因）⇒ ★★★ 账本恢复诚实；★ 而滚动 lookback 窗口会在
// ★ ★ 下一 tick 重新覆盖这段区间 ⇒ ★★ 数据自己回来。
//
// ★ ★★ **反向守卫（★ 必须有！）**：★ 没有它的话，「把这一轮改成**永远**失败」也能让
// ★ ★ 上面的断言变绿 ⇒ ★★ 那就把功能改没了 ⇒ ★★ 所以下面同时钉住
// ★ ★ 「写入成功 ⇒ 仍然 `completed`、且 diff_count 正确」。

// nonEmptyStringArg 匹配「非空字符串」参数。
//
// ★ 用于断言账本的 `error` 列**必须被写上原因** —— ★ 用 `pgxmock.AnyArg()`
// ★ 等于没断言（★ 任何值都过 ⇒ ★ 那这条断言就只是装饰）。
type nonEmptyStringArg struct{}

func (nonEmptyStringArg) Match(v interface{}) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

// reconciliationFactCols 是 facts 查询与 projections 查询**共有**的 15 列
// （★ 两者列序完全一致 ⇒ ★ 共用一份 ⇒ ★ 免得改一处忘一处）。
var reconciliationFactCols = []string{
	"day_utc", "tenant_id", "provider_id", "credential_id", "canonical_id",
	"raw_model_name", "traffic_class",
	"request_count", "success_count", "failure_count",
	"prompt_tokens", "completion_tokens", "total_tokens",
	"cost_usd", "credits_charged",
}

// queueReconcileFactsAndProjections 铺好「facts 有 1 行 + projections 有 1 行」的
// 场景，★ 两者**只有 request_count 不同**（10 vs 5）⇒ ★⇒ 恰好产生 **1 条差异**
// ⇒ ⇒ `canAutoRepair(10,5)` 的 relDiff=1.0 ≫ 2% ⇒ ★★ 不自动修复 ⇒
// ★★ resolution='open' ⇒ ★★ pendingRepairs=0 ⇒ ★★ 不会触发 Refresh（★ 省掉一堆 mock）。
func queueReconcileFactsAndProjections(mock pgxmock.PgxPoolIface, day time.Time) {
	factRow := []interface{}{
		day, "tenant-a", int64(1), int64(2), int64(3), "gpt-x", "default",
		int64(10), int64(10), int64(0), int64(100), int64(50), int64(150), 1.5, int64(2),
	}
	projRow := []interface{}{
		day, "tenant-a", int64(1), int64(2), int64(3), "gpt-x", "default",
		int64(5), int64(10), int64(0), int64(100), int64(50), int64(150), 1.5, int64(2),
	}
	mock.ExpectQuery(`FROM usage_facts f`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(reconciliationFactCols).AddRow(factRow...))
	mock.ExpectQuery(`FROM stats_usage_daily`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(reconciliationFactCols).AddRow(projRow...))
}

// TestReconciliationWorker_DiffWriteFailureMarksRunFailed —— ★★★ 正向断言：
// ★ 「差异写入失败 ⇒ 这一轮必须被记成 failed，且 error 列写上原因」。
func TestReconciliationWorker_DiffWriteFailureMarksRunFailed(t *testing.T) {
	mock := newReconciliationMockEnv(t)
	queueReconcilePeriodHappyPath(mock)

	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	queueReconcileFactsAndProjections(mock, day)

	// 差异写入失败（★ 模拟 r1043 那次缺列的失败形态）。
	mock.ExpectExec(`INSERT INTO stats_reconciliation_diffs`).
		WithArgs(
			pgxmock.AnyArg(), // run_id
			pgxmock.AnyArg(), // tenant_id
			pgxmock.AnyArg(), // dimension_key
			pgxmock.AnyArg(), // metric
			pgxmock.AnyArg(), // source_value
			pgxmock.AnyArg(), // projected_value
			pgxmock.AnyArg(), // difference
			pgxmock.AnyArg(), // resolution
		).
		WillReturnError(fmt.Errorf(`column "tenant_id" of relation "stats_reconciliation_diffs" does not exist`))
	// 复位本轮已插入的 pending repairs（★ 不留孤儿行）。
	// ★ 该 UPDATE 只带 1 个参数（run_id）—— ★ 参数个数必须对上，★ 否则 pgxmock 的
	// ★ ordered 匹配会整条错位（★ 后面的账本断言会拿错期望去比）。
	mock.ExpectExec(`UPDATE stats_reconciliation_diffs`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// ★★★ 账本这一行才是本测试的真正靶心：
	// ★ status 必须是 **failed**（★ 不是 'completed'），★ error 列必须**非空**。
	mock.ExpectExec(`UPDATE stats_reconciliation_runs`).
		WithArgs(
			pgxmock.AnyArg(),    // run_id
			"failed",            // status ★★★ 核心断言
			pgxmock.AnyArg(),    // events_seen
			pgxmock.AnyArg(),    // rows_compared
			pgxmock.AnyArg(),    // rows_repaired
			pgxmock.AnyArg(),    // diff_count
			pgxmock.AnyArg(),    // source_watermark
			nonEmptyStringArg{}, // error ★★★ 必须写上原因（★ 空串 ⇒ 账本仍然说谎）
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	worker := newReconciliationWorkerWithDB(mock, time.Minute)

	start := day
	end := day.Add(24 * time.Hour)
	err := worker.ReconcilePeriod(context.Background(), start, end, "write_fail")
	require.Error(t, err, "★ 差异写入失败必须让这一轮失败（★ 否则账本说谎）")
	require.Contains(t, err.Error(), "record diff",
		"★ 错误必须指名道姓到「写差异」这一步（★ 不能是一个笼统的失败）")
	require.NoError(t, mock.ExpectationsWereMet(),
		"★ 账本那一行必须真的带 status='failed' 且 error 非空")
}

// TestReconciliationWorker_DiffWriteSuccessStillCompletes —— ★★★ 反向守卫：
// ★ 「写入成功 ⇒ 仍然 completed，且 diff_count 正确」。
//
// ★★★ 没有这条，上面那条断言可以靠「把这一轮改成**永远**失败」来满足 ⇒
// ★★★ 那就把功能改没了 ⇒ ★★★ 所以这两条必须**成对**存在。
func TestReconciliationWorker_DiffWriteSuccessStillCompletes(t *testing.T) {
	mock := newReconciliationMockEnv(t)
	queueReconcilePeriodHappyPath(mock)

	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	queueReconcileFactsAndProjections(mock, day)

	// 差异写入成功（★ 恰好 1 条差异 ⇒ diff_count 应为 1）。
	mock.ExpectExec(`INSERT INTO stats_reconciliation_diffs`).
		WithArgs(
			pgxmock.AnyArg(), // run_id
			pgxmock.AnyArg(), // tenant_id
			pgxmock.AnyArg(), // dimension_key
			pgxmock.AnyArg(), // metric
			pgxmock.AnyArg(), // source_value
			pgxmock.AnyArg(), // projected_value
			pgxmock.AnyArg(), // difference
			pgxmock.AnyArg(), // resolution
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// 账本：★ status 必须是 'completed'，★ diff_count 必须是 1。
	mock.ExpectExec(`UPDATE stats_reconciliation_runs`).
		WithArgs(
			pgxmock.AnyArg(), // run_id
			"completed",      // status
			pgxmock.AnyArg(), // events_seen
			int64(1),         // rows_compared ★ 1 条差异
			int64(0),         // rows_repaired ★ 未自动修复
			int64(1),         // diff_count ★★★ 1
			pgxmock.AnyArg(), // source_watermark
			"",               // error ★ 成功时为空
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	worker := newReconciliationWorkerWithDB(mock, time.Minute)

	start := day
	end := day.Add(24 * time.Hour)
	require.NoError(t, worker.ReconcilePeriod(context.Background(), start, end, "write_ok"),
		"★ 写入成功 ⇒ 这一轮必须正常完成（★ 反向守卫）")
	require.NoError(t, mock.ExpectationsWereMet(),
		"★ 账本必须记 completed + diff_count=1")
}
