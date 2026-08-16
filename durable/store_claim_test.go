package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestClaimSelectSQLRequiresScheduledRetryToBeDue(t *testing.T) {
	if !strings.Contains(claimSelectSQL, "next_retry_at <= $2") {
		t.Fatalf("claim SQL must only claim tasks whose retry time is due: %s", claimSelectSQL)
	}
	if strings.Contains(claimSelectSQL, "INTERVAL") {
		t.Fatalf("claim SQL must not claim tasks before next_retry_at: %s", claimSelectSQL)
	}
}

func claimOpts() ClaimOptions {
	return ClaimOptions{
		Owner: "worker-1",
		Lease: 60 * time.Second,
		Batch: 8,
		Now:   time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	}
}

func claimedTaskRows() *pgxmock.Rows {
	cols := []string{
		"id", "tenant_id", "request_id", "session_id", "protocol", "endpoint",
		"status", "commit_state", "semantic_content_committed",
		"error_kind", "reason_code", "attempt_count", "fencing_token",
		"lease_owner", "lease_until", "next_retry_at", "deadline_at", "expires_at",
		"request_hash", "snapshot_version", "encryption_key_id",
		"result_version", "created_at", "updated_at", "completed_at",
	}
	return pgxmock.NewRows(cols).AddRow(
		"task-1", "tenant-1", "req-1", "sess-1", "openai", "/v1/chat/completions",
		"running", "none", false,
		"", "", 2, int64(3),
		"worker-1", time.Date(2026, 8, 15, 12, 1, 0, 0, time.UTC),
		nil, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		"hash-1", 1, "k1",
		nil, time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC), time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC), nil,
	)
}

// TestStore_ClaimRunnable：worker claim 在单事务内完成 SELECT ... FOR UPDATE
// SKIP LOCKED + fencing_token 递增 + 新 lease（doc 18 §11.3），SQL 携带全局
// commit_state IN ('none','metadata') 门禁与 lease/deadline 条件。
func TestStore_ClaimRunnable(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).
		WithArgs(8, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status"}).AddRow("task-1", "retry_scheduled"))
	mock.ExpectQuery(`UPDATE durable_llm_tasks`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(claimedTaskRows())
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(anyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	tasks, err := store.ClaimRunnable(context.Background(), claimOpts())
	if err != nil {
		t.Fatalf("ClaimRunnable: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	got := tasks[0]
	if got.Status != StatusRunning || got.CommitState != CommitStateNone {
		t.Fatalf("status/commit = %s/%s", got.Status, got.CommitState)
	}
	if got.FencingToken != 3 || got.LeaseOwner != "worker-1" {
		t.Fatalf("fencing/owner = %d/%s", got.FencingToken, got.LeaseOwner)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_ClaimRunnable_EmptyBatch：无可领任务返回空切片（worker 空转轮询）。
func TestStore_ClaimRunnable_EmptyBatch(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).
		WithArgs(8, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status"}))
	mock.ExpectCommit()

	tasks, err := store.ClaimRunnable(context.Background(), claimOpts())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks = %v err = %v", tasks, err)
	}
}

// TestStore_RenewLease：续租携带 (lease_owner, fencing_token) 条件。
func TestStore_RenewLease(t *testing.T) {
	store, mock := newMockStore(t)
	until := time.Date(2026, 8, 15, 12, 1, 0, 0, time.UTC)
	mock.ExpectExec(`UPDATE durable_llm_tasks`).
		WithArgs("task-1", "worker-1", int64(3), until, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := store.RenewLease(context.Background(), "task-1", "worker-1", 3, until); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
}

// TestStore_RenewLease_LeaseLost：0 行更新 = 租约被夺/token 失效，必须
// 返回 ErrLeaseLost，旧 worker 停止执行（doc 18 §11.3）。
func TestStore_RenewLease_LeaseLost(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`UPDATE durable_llm_tasks`).
		WithArgs("task-1", "worker-1", int64(3), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.RenewLease(context.Background(), "task-1", "worker-1", 3, time.Now())
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

// TestStore_Reschedule：重排到 runnable 状态携带 fencing 条件且 SQL 含
// commit_state IN ('none','metadata') 全局门禁（doc 18 §11.3），同事务写
// retry 事件。
func TestStore_Reschedule(t *testing.T) {
	store, mock := newMockStore(t)
	next := time.Date(2026, 8, 15, 12, 5, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE durable_llm_tasks`).
		WithArgs("task-1", "worker-1", int64(3), next, "quota_limited", "waiting_capacity").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "request_id", "session_id", "fencing_token"}).
			AddRow("tenant-1", "req-1", "sess-1", int64(3)))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err := store.Reschedule(context.Background(), RescheduleParams{
		TaskID: "task-1", LeaseOwner: "worker-1", FencingToken: 3,
		NextRetryAt: next, ErrorKind: "quota_limited", Reason: "waiting_capacity",
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_Reschedule_LeaseLost：门禁/fencing 不满足时 0 行 + 回滚 +
// ErrLeaseLost（已 checkpoint 任务即使被误写 retry_scheduled 也不可重排）。
func TestStore_Reschedule_LeaseLost(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE durable_llm_tasks`).
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "request_id", "session_id", "fencing_token"}))
	mock.ExpectRollback()

	err := store.Reschedule(context.Background(), RescheduleParams{
		TaskID: "task-1", LeaseOwner: "worker-1", FencingToken: 3,
		NextRetryAt: time.Now(),
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
