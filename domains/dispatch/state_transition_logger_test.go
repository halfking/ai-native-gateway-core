package dispatch

// state_transition_logger_test.go — OBS-BE7 (2026-08-15)
//
// 覆盖状态变更旁路写入的可靠性语义（无真实 PG，mock Exec 接口，
// 模式参考 domains/streaming/anomaly_harvester_test.go）：
//   1. 失败注入 → 进入重试队列 → 重放成功（幂等 SQL 含 ON CONFLICT DO NOTHING + seq）
//   2. 指数退避：未到期的条目不会被提前重放
//   3. 重试 3 次仍失败 → 丢弃（队列不无限增长）
//   4. seq 分配：同一行在缓冲/重试全程保持同一 seq（幂等键稳定）
//   5. 清理任务：DELETE 过期行（retention 参数化、RowsAffected 透传）

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type fakeTransitionDB struct {
	failFirstN    int
	insertCalls   int
	beginCalls    int
	commitCalls   int
	rollbackCalls int
	execSQL       []string
	execArgs      [][]any
}

type fakeTransitionTx struct {
	db        *fakeTransitionDB
	committed bool
}

func (f *fakeTransitionDB) Begin(context.Context) (stateTransitionTx, error) {
	f.beginCalls++
	return &fakeTransitionTx{db: f}, nil
}

func (tx *fakeTransitionTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.db.execSQL = append(tx.db.execSQL, sql)
	tx.db.execArgs = append(tx.db.execArgs, args)
	if strings.Contains(sql, "INSERT INTO request_state_transitions") {
		tx.db.insertCalls++
		if tx.db.insertCalls <= tx.db.failFirstN {
			return pgconn.NewCommandTag(""), errors.New("injected db failure")
		}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	if strings.Contains(sql, "DELETE") {
		return pgconn.NewCommandTag("DELETE 7"), nil
	}
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (tx *fakeTransitionTx) Commit(context.Context) error {
	tx.committed = true
	tx.db.commitCalls++
	return nil
}

func (tx *fakeTransitionTx) Rollback(context.Context) error {
	tx.db.rollbackCalls++
	return nil
}

// newTestLogger 构造不启动后台 goroutine 的 logger（确定性测试）。
func newTestLogger(db stateTransitionDB, now func() time.Time) *StateTransitionLogger {
	return &StateTransitionLogger{
		db:               db,
		buf:              make([]pendingTransition, 0, 8),
		now:              now,
		flushInterval:    time.Hour,
		batchSize:        100,
		maxRetryAttempts: 3,
		retryBaseDelay:   time.Second,
		retryScanEvery:   time.Hour,
		cleanupInterval:  time.Hour,
		retention:        7 * 24 * time.Hour,
		maxRetryQueueLen: 100,
		stopCh:           make(chan struct{}),
		done:             make(chan struct{}),
		doneCleanup:      make(chan struct{}),
	}
}

func fixedNow() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) }

// TestFlushFailureEnqueuesRetryThenReplaySucceeds: 失败注入 → 重试队列 →
// 到期后重放成功，且 INSERT 是幂等形式（ON CONFLICT (request_id, seq) DO NOTHING）。
func TestFlushFailureEnqueuesRetryThenReplaySucceeds(t *testing.T) {
	db := &fakeTransitionDB{failFirstN: 2} // 两条插入都失败一次
	l := newTestLogger(db, fixedNow)

	l.LogRouteDecision("req-1", "admin", "arrived", "routed", nil)
	l.LogRetry("req-1", "admin", 1, "upstream_5xx", nil)
	l.Flush()

	if got := l.retryQueueLen(); got != 2 {
		t.Fatalf("after failed flush, retry queue len = %d, want 2", got)
	}
	if len(l.buf) != 0 {
		t.Fatalf("buf should be drained after flush, got %d", len(l.buf))
	}

	// 未到期（nextRetryAt = now+1s）不应重放。
	l.processRetriesDue(fixedNow())
	if got := l.retryQueueLen(); got != 2 {
		t.Fatalf("before backoff elapses, retry queue len = %d, want 2", got)
	}

	// 到期（now+1s）重放成功。
	l.processRetriesDue(fixedNow().Add(time.Second))
	if got := l.retryQueueLen(); got != 0 {
		t.Fatalf("after due replay, retry queue len = %d, want 0", got)
	}

	// 每次写入必须先在同一事务设置 tenant，再执行幂等 INSERT。
	if len(db.execSQL) != 8 {
		t.Fatalf("exec calls = %d, want 8 (4 tenant GUC + 4 inserts)", len(db.execSQL))
	}
	insertIndexes := []int{1, 3, 5, 7}
	for txIndex, insertIndex := range insertIndexes {
		gucIndex := insertIndex - 1
		if !strings.Contains(db.execSQL[gucIndex], "set_config('app.current_tenant'") {
			t.Errorf("tx[%d] first exec must set tenant, got:\n%s", txIndex, db.execSQL[gucIndex])
		}
		if got := db.execArgs[gucIndex][0]; got != "admin" {
			t.Errorf("tx[%d] tenant GUC = %v, want admin", txIndex, got)
		}
		if !strings.Contains(db.execSQL[insertIndex], "ON CONFLICT (request_id, seq) DO NOTHING") {
			t.Errorf("tx[%d] insert missing idempotent clause, got:\n%s", txIndex, db.execSQL[insertIndex])
		}
	}
	// 重放行的 (request_id, seq) 与首次失败行一致（seq 不变）。
	for i := 2; i < 4; i++ {
		original := insertIndexes[i-2]
		replay := insertIndexes[i]
		if got := db.execArgs[replay][0]; got != "req-1" {
			t.Errorf("replay[%d] request_id = %v, want req-1", i, got)
		}
		if seq := db.execArgs[replay][6]; seq != db.execArgs[original][6] {
			t.Errorf("replay[%d] seq = %v differs from original %v (idempotency key must be stable)",
				i, seq, db.execArgs[original][6])
		}
	}
	if db.commitCalls != 2 {
		t.Errorf("commits = %d, want 2 successful replay commits", db.commitCalls)
	}
}

func TestTransitionMetadataUsesJSONTextForSimpleProtocol(t *testing.T) {
	db := &fakeTransitionDB{}
	l := newTestLogger(db, fixedNow)
	l.LogRouteDecision("req-json", "admin", "arrived", "routed", map[string]any{
		"candidates": 4,
		"selected":   "glm-5.2",
	})
	l.Flush()

	if len(db.execArgs) != 2 {
		t.Fatalf("exec calls = %d, want tenant GUC + insert", len(db.execArgs))
	}
	metadata, ok := db.execArgs[1][5].(string)
	if !ok {
		t.Fatalf("metadata arg type = %T, want string for pgx simple protocol", db.execArgs[1][5])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(metadata), &decoded); err != nil {
		t.Fatalf("metadata arg is not valid JSON: %v", err)
	}
	if decoded["selected"] != "glm-5.2" {
		t.Fatalf("metadata selected = %v, want glm-5.2", decoded["selected"])
	}
}

func TestTransitionNilMetadataUsesSQLNull(t *testing.T) {
	db := &fakeTransitionDB{}
	l := newTestLogger(db, fixedNow)
	l.LogRouteDecision("req-null", "admin", "arrived", "routed", nil)
	l.Flush()

	if len(db.execArgs) != 2 {
		t.Fatalf("exec calls = %d, want tenant GUC + insert", len(db.execArgs))
	}
	if got := db.execArgs[1][5]; got != nil {
		t.Fatalf("nil metadata arg = %#v (%T), want nil", got, got)
	}
}

// TestRetryBackoffDropsAfterMaxAttempts: 持续失败 → 恰好重试 3 次后丢弃。
func TestRetryBackoffDropsAfterMaxAttempts(t *testing.T) {
	db := &fakeTransitionDB{failFirstN: 1 << 30} // 永远失败
	l := newTestLogger(db, fixedNow)

	l.LogError("req-2", "admin", "streaming", nil)
	l.Flush()
	if got := l.retryQueueLen(); got != 1 {
		t.Fatalf("retry queue len = %d, want 1", got)
	}

	// 重试 #1 (attempts=1, due at +1s)、#2 (+2s)、#3 (+4s) 全部失败后应被丢弃。
	l.processRetriesDue(fixedNow().Add(time.Second))      // retry 1 fails
	l.processRetriesDue(fixedNow().Add(3 * time.Second))  // retry 2 fails
	l.processRetriesDue(fixedNow().Add(10 * time.Second)) // retry 3 fails → drop
	if got := l.retryQueueLen(); got != 0 {
		t.Fatalf("after 3 failed retries, retry queue len = %d, want 0 (dropped)", got)
	}
	// 1 次首写 + 3 次重试 = 4 次 INSERT；全部失败，不应 commit。
	if db.insertCalls != 4 {
		t.Errorf("insert calls = %d, want 4 (initial + 3 retries)", db.insertCalls)
	}
	if db.commitCalls != 0 {
		t.Errorf("commits = %d, want 0 for failed transactions", db.commitCalls)
	}

	// 退避时长本身：1s → 2s → 4s。
	if got := l.retryBackoff(1); got != time.Second {
		t.Errorf("backoff(1) = %v, want 1s", got)
	}
	if got := l.retryBackoff(2); got != 2*time.Second {
		t.Errorf("backoff(2) = %v, want 2s", got)
	}
	if got := l.retryBackoff(3); got != 4*time.Second {
		t.Errorf("backoff(3) = %v, want 4s", got)
	}
}

// TestSeqAssignmentMonotonicAndStable: seq 全局单调、且缓冲期间固定。
func TestSeqAssignmentMonotonicAndStable(t *testing.T) {
	db := &fakeTransitionDB{}
	l := newTestLogger(db, fixedNow)

	l.LogRouteDecision("req-A", "admin", "a", "b", nil)
	l.LogRouteDecision("req-B", "admin", "a", "b", nil)
	l.LogRetry("req-A", "admin", 1, "timeout", nil)

	l.mu.Lock()
	seqs := []int64{l.buf[0].seq, l.buf[1].seq, l.buf[2].seq}
	requests := []string{l.buf[0].t.RequestID, l.buf[1].t.RequestID, l.buf[2].t.RequestID}
	l.mu.Unlock()

	for i, s := range seqs {
		if s != int64(i+1) {
			t.Errorf("seq[%d] = %d, want %d (monotonic)", i, s, i+1)
		}
	}
	if requests[0] != "req-A" || requests[1] != "req-B" || requests[2] != "req-A" {
		t.Errorf("buffer order changed: %v", requests)
	}
}

// TestCleanupOldTransitionsDeletesExpiredRows: 清理任务调用 DELETE，带 7 天
// retention 参数，并透传 RowsAffected。
func TestCleanupOldTransitionsDeletesExpiredRows(t *testing.T) {
	db := &fakeTransitionDB{}
	l := newTestLogger(db, fixedNow)

	n, err := l.CleanupOldTransitions(context.Background())
	if err != nil {
		t.Fatalf("CleanupOldTransitions() error = %v", err)
	}
	if n != 7 {
		t.Errorf("rows affected = %d, want 7 (from mock tag)", n)
	}
	if len(db.execSQL) != 2 {
		t.Fatalf("exec calls = %d, want bypass GUC + DELETE", len(db.execSQL))
	}
	if !strings.Contains(db.execSQL[0], "set_config('app.bypass_rls', 'true', true)") {
		t.Errorf("cleanup must set transaction-local RLS bypass first, got:\n%s", db.execSQL[0])
	}
	sql := db.execSQL[1]
	if !strings.Contains(sql, "DELETE FROM request_state_transitions") {
		t.Errorf("cleanup SQL must DELETE from request_state_transitions, got:\n%s", sql)
	}
	if !strings.Contains(sql, "created_at < NOW() - $1::interval") {
		t.Errorf("cleanup SQL must filter by created_at retention, got:\n%s", sql)
	}
	if got := db.execArgs[1][0]; got != "168h0m0s" {
		t.Errorf("retention arg = %v, want 168h0m0s (7 days)", got)
	}
	if db.commitCalls != 1 {
		t.Errorf("commits = %d, want 1", db.commitCalls)
	}
}

// TestNilDBNoOp: db 为 nil（DB 禁用/测试模式）时所有入口 no-op，不 panic。
func TestEmptyTenantDoesNotEnterBuffer(t *testing.T) {
	db := &fakeTransitionDB{}
	l := newTestLogger(db, fixedNow)
	l.LogRetry("req", "", 1, "timeout", nil)
	if len(l.buf) != 0 {
		t.Fatalf("empty-tenant transition entered buffer: %d", len(l.buf))
	}
}

func TestNilDBNoOp(t *testing.T) {
	l := newTestLogger(nil, fixedNow)
	l.LogRouteDecision("req", "admin", "a", "b", nil)
	l.Flush()
	l.processRetriesDue(fixedNow())
	if _, err := l.CleanupOldTransitions(context.Background()); err != nil {
		t.Fatalf("CleanupOldTransitions on nil db should be no-op, got %v", err)
	}
}
