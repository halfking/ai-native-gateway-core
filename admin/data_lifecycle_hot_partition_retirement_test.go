package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retirementPromoteDB struct {
	calls atomic.Int32
	query func(context.Context, string, ...any) pgx.Row
}

func (db *retirementPromoteDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	db.calls.Add(1)
	return db.query(ctx, sql, args...)
}

type retirementPromoteRow struct {
	migrated  int64
	afterScan func()
}

func (row retirementPromoteRow) Scan(dest ...any) error {
	*dest[0].(*int64) = row.migrated
	if row.afterScan != nil {
		row.afterScan()
	}
	return nil
}

// TestHotPromoteRetirementEndpoints 钉住手动 promote 端点对退役族的拒绝形状。
//
// ★ 2026-10-29（方向修正）：退役族的拒绝**不再取决于退役开关的状态**。
//
//	旧实现是「开关关着 ⇒ 409，开着 ⇒ 放行」，那个方向是反的：放行放行的是
//	一次对**已被 890 DROP 的 promote 函数**的调用，而自动侧对同一个族是
//	**无条件**退役（bg/partition_manager.go 的 v1LifecycleRetired() 恒 true）。
//	⇒ 手动侧与自动侧方向一致：退役族在两种开关状态下都 409。
func TestHotPromoteRetirementEndpoints(t *testing.T) {
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot", "usage_ledger_hot", "session_bodies_hot"} {
		for _, enabled := range []bool{false, true} {
			for _, async := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/write=%t/async=%t", table, enabled, async), func(t *testing.T) {
					withRequestLogsWriteSetting(t, enabled)
					require.Equal(t, enabled, settings.RequestLogsWriteEnabled())
					h := &Handler{hotJobMgr: newHotJobManager()}
					called := make(chan struct{}, 1)
					db := &retirementPromoteDB{query: func(_ context.Context, sql string, args ...any) pgx.Row {
						assert.Equal(t, "SELECT "+hotPromoteTableMap[table]+"($1::interval, $2::int)", sql)
						assert.Equal(t, []any{"8 hours", 7}, args)
						called <- struct{}{}
						return retirementPromoteRow{}
					}}
					w := httptest.NewRecorder()
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"table_name":%q,"batch_size":7}`, table)))
					if async {
						h.handleDataLifecyclePromoteHotAsyncWithDB(w, r, db)
					} else {
						h.handleDataLifecyclePromoteHotWithDB(w, r, db)
					}
					retired := table == "_request_logs_hot" || table == "_request_logs_bodies_hot"
					if retired {
						// ★ 与开关状态无关：两种状态下形状相同。
						require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
						require.Contains(t, w.Body.String(), table)
						require.Contains(t, w.Body.String(), "retired request_logs family")
						require.Zero(t, db.calls.Load(), "closed gate must not query the DB")
						require.Empty(t, h.hotJobMgr.jobs, "closed gate must not admit jobs")
						require.Empty(t, h.hotJobMgr.perTable)
						return
					}
					if async {
						require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
						var resp promoteHotAsyncResponse
						require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
						require.NotEmpty(t, resp.RunID)
						require.True(t, resp.Async)
						require.Equal(t, string(HotJobStatusPending), resp.Status)
						require.Len(t, h.hotJobMgr.jobs, 1)
						t.Cleanup(h.hotJobMgr.jobs[resp.RunID].cancelFn)
					} else {
						require.Equal(t, http.StatusOK, w.Code, w.Body.String())
						var resp promoteHotResponse
						require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
						require.Equal(t, "success", resp.Status)
						require.Empty(t, h.hotJobMgr.jobs)
					}
					select {
					case <-called:
					case <-time.After(5 * time.Second):
						t.Fatal("allowed promotion did not reach the DB")
					}
					require.EqualValues(t, 1, db.calls.Load())
				})
			}
		}
	}
}

// TestHotPromoteRetirementGuardIsUnconditional 钉住 2026-10-29 的方向修正：
// **退役族的 promote 拒绝与退役开关的状态无关**。
//
// 为什么这道门单独存在（而不是只靠端点用例）：端点用例要跑完整的 handler
// 链（异步/同步两条），而这道门直接钉住**判据本身**——
// promoteHotRetiredReason 对退役族必须恒返回非空理由。
//
// ★ 反向自证：把 promoteHotRetiredReason 改回「开关开着 ⇒ 返回 ""（放行）」，
//
//	本用例在 write=true 那一档必须报红（端点用例同样会红）。
func TestHotPromoteRetirementGuardIsUnconditional(t *testing.T) {
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot"} {
		for _, writeEnabled := range []bool{false, true} {
			withRequestLogsWriteSetting(t, writeEnabled)
			require.Equal(t, writeEnabled, settings.RequestLogsWriteEnabled())
			reason := promoteHotRetiredReason(table)
			require.NotEmpty(t, reason,
				"退役族 %s 在退役开关=%t 时必须仍被拒绝：族已被 890 物理删除，"+
					"放行只会调到一个不存在的函数", table, writeEnabled)
			require.Contains(t, reason, table, "拒绝理由必须点名该表")
			require.Contains(t, reason, "retired request_logs family",
				"拒绝理由必须保持既有契约里的族标识")
		}
	}
	// 在位族不受影响（门只对退役族闭合）。
	for _, table := range []string{"usage_ledger_hot", "session_bodies_hot", "request_wal_hot"} {
		require.Empty(t, promoteHotRetiredReason(table), "在位族 %q 不该被退役门拦", table)
	}
}

// TestHotPromoteRetirementGuardDoesNotReadTheRetiredSwitch 是静态门：
// 退役判定**不得**再读 storage.request_logs_write_enabled。
//
// 为什么静态判据不是多余的：行为门只覆盖「退役族 ⇒ 恒拒绝」这一个方向；
// 这道门覆盖的是**成因**——只要判据还在读那个开关，它就随时可能被再次
// 写成「开关开着 ⇒ 放行」（那正是 2026-10-29 修掉的那个方向错误）。
//
// ★ 判据落在**调用形态**上（`RequestLogsWriteEnabled(` /
//
//	`KeyRequestLogsWriteEnabled` / 引号包起来的键字面量），不是裸标识符：
//	文件头必须能自由解释「这个开关为什么不该出现在这里」，否则这道门会
//	逼着作者闭嘴——那是把治理变成自我审查。
func TestHotPromoteRetirementGuardDoesNotReadTheRetiredSwitch(t *testing.T) {
	src, err := os.ReadFile("data_lifecycle_hot_partition.go")
	require.NoError(t, err)
	text := string(src)
	for _, needle := range []string{
		"RequestLogsWriteEnabled(",
		"KeyRequestLogsWriteEnabled",
		`"storage.request_logs_write_enabled"`,
	} {
		require.NotContains(t, text, needle,
			"退役判定不得再读退役开关：它现在只影响 lite 模式的 SQLite request_logs 表，"+
				"与已被 890 物理删除的 PG v1 族无关")
	}
}

func TestHotPromoteRetirementRejectsExistingAsyncJob(t *testing.T) {
	withRequestLogsWriteSetting(t, false)
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot"} {
		h := &Handler{hotJobMgr: newHotJobManager()}
		existing := &HotPromoteJob{ID: "existing", TableName: table, Status: HotJobStatusRunning, StartedAt: time.Now()}
		h.hotJobMgr.add(existing)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"table_name":%q}`, table)))
		h.handleDataLifecyclePromoteHotAsync(w, r)
		require.Equal(t, http.StatusConflict, w.Code)
		require.Len(t, h.hotJobMgr.jobs, 1)
		require.Same(t, existing, h.hotJobMgr.jobs["existing"])
		require.Equal(t, HotJobStatusRunning, existing.Status)
	}
}

// TestHotPromoteRetirementRunningJob 钉住 job 执行期的退役门。
//
// ★ 2026-10-29（方向修正）：退役族在**任何**开关状态下都必须在**第一次
//
//	查询之前**失败（0 批、0 次 DB 调用）。旧实现允许「开关开着 ⇒ 跑批次」，
//	而被调用的 promote 函数已被 890 DROP ⇒ 那是必然 42P01 的调用。
func TestHotPromoteRetirementRunningJob(t *testing.T) {
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot", "usage_ledger_hot"} {
		for _, writeEnabled := range []bool{false, true} {
			for _, closeAfterBatch := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/write=%t/closeAfterBatch=%t", table, writeEnabled, closeAfterBatch), func(t *testing.T) {
					withRequestLogsWriteSetting(t, writeEnabled)
					h := &Handler{}
					job := &HotPromoteJob{TableName: table, RetentionHours: 8, BatchSize: 7, StartedAt: time.Now().Add(-2 * time.Second)}
					db := &retirementPromoteDB{}
					db.query = func(_ context.Context, _ string, _ ...any) pgx.Row {
						if table != "usage_ledger_hot" {
							t.Errorf("retired table %s must not reach the DB", table)
						}
						if db.calls.Load() == 1 {
							return retirementPromoteRow{migrated: 3, afterScan: func() {
								if closeAfterBatch {
									withRequestLogsWriteSetting(t, false)
									require.False(t, settings.RequestLogsWriteEnabled())
								}
							}}
						}
						return retirementPromoteRow{}
					}
					h.runHotPromoteJobWithDB(context.Background(), job, 0, db)
					retired := table != "usage_ledger_hot"
					if retired {
						require.Zero(t, db.calls.Load(),
							"退役族必须在第一次查询之前失败（族已被 890 物理删除）")
						require.Equal(t, HotJobStatusFailed, job.Status)
						require.Contains(t, job.Error, "retired request_logs family")
						require.Zero(t, job.TotalMigrated)
						require.Zero(t, job.BatchesExecuted)
						require.Empty(t, job.Warning,
							"退役族不得下发任何清理指令（族已不存在，没什么可回收）")
						require.NotNil(t, job.FinishedAt)
						require.Equal(t, *job.FinishedAt, job.UpdatedAt)
						return
					}
					// 在位族：退役门不适用，写开关的变化也不影响它（门只管退役族）。
					require.EqualValues(t, 2, db.calls.Load())
					require.Equal(t, HotJobStatusSuccess, job.Status)
					require.Empty(t, job.Error)
					require.Equal(t, 2, job.BatchesExecuted)
					require.EqualValues(t, 3, job.TotalMigrated)
					require.Equal(t, job.BatchesExecuted, job.CurrentBatch)
					require.NotNil(t, job.FinishedAt)
					require.Equal(t, *job.FinishedAt, job.UpdatedAt)
					require.GreaterOrEqual(t, job.DurationSeconds, int64(2))
				})
			}
		}
	}
}

func TestHotPromoteRetirementQueuedJob(t *testing.T) {
	withRequestLogsWriteSetting(t, false)
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot"} {
		job := &HotPromoteJob{TableName: table, StartedAt: time.Now()}
		db := &retirementPromoteDB{query: func(context.Context, string, ...any) pgx.Row {
			t.Error("closed gate must reject before the first query")
			return retirementPromoteRow{}
		}}
		(&Handler{}).runHotPromoteJobWithDB(context.Background(), job, 0, db)
		require.Zero(t, db.calls.Load())
		require.Equal(t, HotJobStatusFailed, job.Status)
		require.NotNil(t, job.FinishedAt)
		require.Contains(t, job.Error, table)
	}
}
