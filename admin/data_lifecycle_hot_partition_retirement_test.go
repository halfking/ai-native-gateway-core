package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
					if retired && !enabled {
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

func TestHotPromoteRetirementRunningJob(t *testing.T) {
	for _, table := range []string{"_request_logs_hot", "_request_logs_bodies_hot", "usage_ledger_hot"} {
		for _, closeAfterBatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/closeAfterBatch=%t", table, closeAfterBatch), func(t *testing.T) {
				withRequestLogsWriteSetting(t, true)
				h := &Handler{}
				job := &HotPromoteJob{TableName: table, RetentionHours: 8, BatchSize: 7, StartedAt: time.Now().Add(-2 * time.Second)}
				db := &retirementPromoteDB{}
				db.query = func(_ context.Context, _ string, _ ...any) pgx.Row {
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
				if closeAfterBatch && retired {
					require.EqualValues(t, 1, db.calls.Load(), "closure must prevent the second query")
					require.Equal(t, HotJobStatusFailed, job.Status)
					require.Contains(t, job.Error, "retired request_logs family")
					require.Equal(t, 1, job.BatchesExecuted)
				} else {
					require.EqualValues(t, 2, db.calls.Load())
					require.Equal(t, HotJobStatusSuccess, job.Status)
					require.Empty(t, job.Error)
					require.Equal(t, 2, job.BatchesExecuted)
				}
				require.EqualValues(t, 3, job.TotalMigrated)
				require.Equal(t, job.BatchesExecuted, job.CurrentBatch)
				require.NotNil(t, job.FinishedAt)
				require.Equal(t, *job.FinishedAt, job.UpdatedAt)
				require.GreaterOrEqual(t, job.DurationSeconds, int64(2))
			})
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
