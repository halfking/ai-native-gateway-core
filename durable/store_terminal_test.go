package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/pashagolub/pgxmock/v4"
)

func baseTask() *Task {
	return &Task{
		ID:           "task-1",
		TenantID:     "tenant-1",
		RequestID:    "req-1",
		SessionID:    "sess-1",
		RequestHash:  "hash-1",
		Status:       StatusRunning,
		CommitState:  CommitStateNone,
		FencingToken: 3,
		LeaseOwner:   "worker-1",
	}
}

// TestStore_CheckpointCommitState：write-ahead checkpoint（doc 18 §11.3）
// 携带 (lease_owner, fencing_token)，且 commit_state 只允许前进（SQL 内
// rank 比较）；tool_call checkpoint 同时保存载荷（tool call ID/类型/序号/
// 参数摘要 hash）。
func TestStore_CheckpointCommitState(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`UPDATE durable_llm_tasks`).
		WithArgs("task-1", "worker-1", int64(3), pgxmock.AnyArg(),
			"tool_call", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.CheckpointCommitState(context.Background(), CheckpointParams{
		TaskID: "task-1", LeaseOwner: "worker-1", FencingToken: 3,
		State:   CommitStateToolCall,
		Payload: []byte(`{"tool_call_id":"tc-1","seq":2,"args_hash":"h"}`),
	})
	if err != nil {
		t.Fatalf("CheckpointCommitState: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CheckpointCommitState_LeaseLost：token 失效或非法回退 checkpoint
// 均为 0 行 → ErrLeaseLost（不得写网络语义帧）。
func TestStore_CheckpointCommitState_LeaseLost(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`UPDATE durable_llm_tasks`).
		WithArgs(anyArgs(7)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.CheckpointCommitState(context.Background(), CheckpointParams{
		TaskID: "task-1", LeaseOwner: "worker-1", FencingToken: 2,
		State: CommitStateContent,
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

// terminalCommitExpectations：CommitTerminal 的 pgxmock 事务脚手架。
func terminalCommitExpectations(mock pgxmock.PgxPoolIface, updatedRows int64) {
	mock.ExpectBegin()
	verRows := pgxmock.NewRows([]string{"result_version"})
	if updatedRows > 0 {
		verRows.AddRow(int64(1))
	}
	mock.ExpectQuery(`UPDATE durable_llm_tasks`).
		WithArgs(anyArgs(12)...).
		WillReturnRows(verRows)
	if updatedRows > 0 {
		mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
			WithArgs(anyArgs(10)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec(`INSERT INTO durable_pending_outbox`).
			WithArgs(anyArgs(4)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	mock.ExpectCommit()
}

// TestStore_CommitTerminal_Completed：终态在单事务内原子写入 status=
// completed + durable-result 域加密结果 + result_hash/version/content_type/
// completed_at/commit_state='terminal'（doc 18 §11.3），并返回可投影
// PendingStore 的载荷。
func TestStore_CommitTerminal_Completed(t *testing.T) {
	store, mock := newMockStore(t)
	terminalCommitExpectations(mock, 1)

	proj, err := store.CommitTerminal(context.Background(), TerminalCommit{
		Task:        baseTask(),
		Outcome:     StatusCompleted,
		Body:        []byte(`{"ok":true}`),
		ContentType: "application/json",
	})
	if err != nil {
		t.Fatalf("CommitTerminal: %v", err)
	}
	if !proj.Committed {
		t.Fatal("committed = false, want true")
	}
	if proj.ResultVersion < 1 {
		t.Fatalf("result version = %d, want >= 1", proj.ResultVersion)
	}
	if proj.FencingToken != 3 || proj.TaskID != "task-1" {
		t.Fatalf("proj = %+v", proj)
	}
	if proj.ResultHash == "" {
		t.Fatal("result hash required")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CommitTerminal_FencedOut：终态竞态只有一个赢家——0 行更新返回
// Committed=false，旧 worker 不得投影 PendingStore（doc 18 §11.3/§11.4）。
func TestStore_CommitTerminal_FencedOut(t *testing.T) {
	store, mock := newMockStore(t)
	terminalCommitExpectations(mock, 0)

	proj, err := store.CommitTerminal(context.Background(), TerminalCommit{
		Task:    baseTask(),
		Outcome: StatusCompleted,
		Body:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CommitTerminal: %v", err)
	}
	if proj.Committed {
		t.Fatal("fenced-out terminal must not report committed")
	}
}

// TestStore_CommitTerminal_RequiresKeyring：无密钥 fail closed（§11.2）。
func TestStore_CommitTerminal_RequiresKeyring(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	store := NewStore(mock, nil)
	if _, err := store.CommitTerminal(context.Background(), TerminalCommit{
		Task: baseTask(), Outcome: StatusCompleted, Body: []byte("x"),
	}); !errors.Is(err, secret.ErrAADNoKey) {
		t.Fatalf("err = %v, want ErrAADNoKey", err)
	}
}

// TestStore_CommitTerminal_RejectsNonTerminalOutcome：outcome 必须是终态。
func TestStore_CommitTerminal_RejectsNonTerminalOutcome(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.CommitTerminal(context.Background(), TerminalCommit{
		Task: baseTask(), Outcome: StatusRunning, Body: []byte("x"),
	}); err == nil {
		t.Fatal("non-terminal outcome must be rejected")
	}
}

// reapExpectations：reaper 的 pgxmock 事务脚手架（SELECT id+from_status 行 +
// UPDATE RETURNING）。
func reapExpectations(mock pgxmock.PgxPoolIface, ids ...string) {
	rows := pgxmock.NewRows([]string{"id", "status"})
	for _, id := range ids {
		rows.AddRow(id, "running")
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(rows)
	if len(ids) > 0 {
		mock.ExpectQuery(`UPDATE durable_llm_tasks`).
			WithArgs(anyArgs(4)...).
			WillReturnRows(reapedReturnRows(ids...))
		mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
			WithArgs(anyArgs(8)...).
			WillReturnResult(pgxmock.NewResult("INSERT", int64(len(ids))))
		mock.ExpectExec(`INSERT INTO durable_pending_outbox`).
			WithArgs(anyArgs(4)...).
			WillReturnResult(pgxmock.NewResult("INSERT", int64(len(ids))))
	}
	mock.ExpectCommit()
}

func reapedReturnRows(ids ...string) *pgxmock.Rows {
	cols := []string{"id", "tenant_id", "request_id", "session_id", "request_hash",
		"status", "fencing_token", "expires_at", "content_type", "reason_code", "result_version"}
	rows := pgxmock.NewRows(cols)
	for _, id := range ids {
		rows.AddRow(id, "tenant-1", "req-1", "sess-1", "hash-1",
			"expired", int64(4), time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), "", "survival_expired", int64(1))
	}
	return rows
}

// TestStore_ReapDeadlines：deadline reaper 把所有非终态且 deadline_at <= now
// 的任务原子迁移为 expired（fencing+1、撤租约、reason=survival_expired、
// completed_at、结果版本），返回可投影的 failed 载荷（doc 18 §11.3）。
func TestStore_ReapDeadlines(t *testing.T) {
	store, mock := newMockStore(t)
	reapExpectations(mock, "task-1")

	reaped, err := store.ReapDeadlines(context.Background(), 16, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ReapDeadlines: %v", err)
	}
	if len(reaped) != 1 {
		t.Fatalf("reaped = %d, want 1", len(reaped))
	}
	if reaped[0].Status != StatusExpired || reaped[0].ReasonCode != ReasonSurvivalExpired {
		t.Fatalf("reaped = %+v", reaped[0])
	}
	if reaped[0].ResultVersion < 1 {
		t.Fatalf("reaper must set result version, got %d", reaped[0].ResultVersion)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_ReapDeadlines_None：无可回收任务返回空。
func TestStore_ReapDeadlines_None(t *testing.T) {
	store, mock := newMockStore(t)
	reapExpectations(mock)

	reaped, err := store.ReapDeadlines(context.Background(), 16, time.Now())
	if err != nil || len(reaped) != 0 {
		t.Fatalf("reaped = %v err = %v", reaped, err)
	}
}

// TestStore_ReapUnsafeCheckpointed：safety reaper 把非终态且 commit_state
// IN ('content','tool_call','terminal') 的任务原子迁移到
// resume_safety_blocked，绝不进入 ExecuteAttempt（doc 18 §11.3）。
func TestStore_ReapUnsafeCheckpointed(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status"}).AddRow("task-2", "running"))
	mock.ExpectQuery(`UPDATE durable_llm_tasks`).
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "request_id", "session_id", "protocol", "endpoint",
			"status", "commit_state", "semantic_content_committed",
			"error_kind", "reason_code", "attempt_count", "fencing_token",
			"lease_owner", "lease_until", "next_retry_at", "deadline_at", "expires_at",
			"request_hash", "snapshot_version", "encryption_key_id",
			"result_version", "created_at", "updated_at", "completed_at",
		}).AddRow(
			"task-2", "tenant-1", "req-1", "sess-1", "openai", "/v1/chat/completions",
			"resume_safety_blocked", "tool_call", true,
			"", "resume_safety_blocked", 2, int64(5),
			nil, nil, nil, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour),
			"hash-1", 1, "k1", int64(1), time.Now(), time.Now(), time.Now()))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(anyArgs(9)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_pending_outbox`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	blocked, err := store.ReapUnsafeCheckpointed(context.Background(), 16, time.Now())
	if err != nil {
		t.Fatalf("ReapUnsafeCheckpointed: %v", err)
	}
	if len(blocked) != 1 || blocked[0].Status != StatusResumeSafetyBlocked {
		t.Fatalf("blocked = %+v", blocked)
	}
	if blocked[0].CommitState != CommitStateToolCall {
		t.Fatalf("commit state = %s", blocked[0].CommitState)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_ReapUnsafeCheckpointed_LiveLeaseGuard（doc 28 §1 回归）：
// lease 仍活跃（前台持有者续租中）的语义检查点任务不得进入 safety reap
// 扫描结果——谓词携带 lease_until < now 护栏，活跃任务零行返回。
func TestStore_ReapUnsafeCheckpointed_LiveLeaseGuard(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	// 谓词必须包含 lease 护栏参数（$2 = now）——通过两个参数匹配钉住。
	mock.ExpectQuery(`lease_until IS NULL OR lease_until < \$2`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status"}))
	mock.ExpectCommit()

	now := time.Now()
	blocked, err := store.ReapUnsafeCheckpointed(context.Background(), 16, now)
	if err != nil {
		t.Fatalf("ReapUnsafeCheckpointed: %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("live-lease checkpointed tasks must not be reaped, got %+v", blocked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_ReapDeadlines_ExcludesCheckpointed：deadline reaper 的扫描必须
// 排除已越过语义检查点（commit_state content/tool_call）的任务——它们只能
// 由 safety reaper 终态化为 resume_safety_blocked（doc 18 §11.3 验收 19，
// 真实 PG 行为由 pg_integration_test.go 钉住，这里钉 SQL 门禁不回退）。
func TestStore_ReapDeadlines_ExcludesCheckpointed(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`commit_state IN \('none', 'metadata'\)`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status"}))
	mock.ExpectCommit()

	reaped, err := store.ReapDeadlines(context.Background(), 16, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil || len(reaped) != 0 {
		t.Fatalf("ReapDeadlines = %v err = %v", reaped, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
