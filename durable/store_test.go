package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/pashagolub/pgxmock/v4"
)

func testKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": {7}}, "k1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return kr
}

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return NewStore(mock, testKeyring(t)), mock
}

// anyArgs 生成 n 个 AnyArg 匹配器（跳过密文等随机参数的精确匹配）。
func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func validNewTask() NewTask {
	return NewTask{
		TenantID:        "tenant-1",
		RequestID:       "req-1",
		SessionID:       "sess-1",
		Protocol:        "openai",
		Endpoint:        "/v1/chat/completions",
		Snapshot:        []byte(`{"model":"gpt-x"}`),
		SnapshotVersion: 1,
		RequestHash:     "hash-1",
		DeadlineAt:      time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		ExpiresAt:       time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		LeaseOwner:      "gw-1/req-1",
		LeaseUntil:      time.Date(2026, 8, 15, 11, 0, 30, 0, time.UTC),
		Policy:          []byte(`{"durable":true}`),
		Attempt:         1,
	}
}

// TestStore_CreateAndClaim：单事务完成「插入加密快照 + status=running +
// lease_owner/lease_until + fencing_token=1 + commit_state='none' +
// semantic_content_committed=false + accepted→running 事件」（doc 18 §11.3
// CreateAndClaim），快照以 durable-request 域 AAD 加密。
func TestStore_CreateAndClaim(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(anyArgs(17)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(anyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	task, err := store.CreateAndClaim(context.Background(), validNewTask())
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if task.Status != StatusRunning {
		t.Fatalf("status = %s, want running", task.Status)
	}
	if task.FencingToken != 1 {
		t.Fatalf("fencing token = %d, want 1", task.FencingToken)
	}
	if task.CommitState != CommitStateNone {
		t.Fatalf("commit state = %s, want none", task.CommitState)
	}
	if task.SemanticContentCommitted {
		t.Fatal("semantic_content_committed must start false")
	}
	if task.LeaseOwner != "gw-1/req-1" || task.LeaseUntil.IsZero() {
		t.Fatalf("lease = %q %v", task.LeaseOwner, task.LeaseUntil)
	}
	if task.NextRetryAt.IsZero() || !task.NextRetryAt.Equal(task.CreatedAt) {
		t.Fatalf("next retry = %v, created = %v; crashed first attempt must become reclaimable after lease expiry", task.NextRetryAt, task.CreatedAt)
	}
	if task.ID == "" {
		t.Fatal("task id must be generated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CreateAndClaim_RollsBackOnError：事务内任一语句失败必须回滚，
// 不得发送已接管状态或留下半创建任务（doc 18 §11.3）。
func TestStore_CreateAndClaim_RollsBackOnError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(anyArgs(17)...).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	if _, err := store.CreateAndClaim(context.Background(), validNewTask()); err == nil {
		t.Fatal("must propagate insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CreateAndClaim_RequiresKeyring：无密钥 fail closed（doc 18 §11.2）。
func TestStore_CreateAndClaim_RequiresKeyring(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	store := NewStore(mock, nil)
	if _, err := store.CreateAndClaim(context.Background(), validNewTask()); !errors.Is(err, secret.ErrAADNoKey) {
		t.Fatalf("err = %v, want ErrAADNoKey", err)
	}
}

// TestStore_CreateAndClaim_UniqueViolation：同 (tenant, request) 重复创建
// 返回 ErrDuplicateTask（幂等防重）。
func TestStore_CreateAndClaim_UniqueViolation(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(anyArgs(17)...).
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key"})
	mock.ExpectRollback()

	if _, err := store.CreateAndClaim(context.Background(), validNewTask()); !errors.Is(err, ErrDuplicateTask) {
		t.Fatalf("err = %v, want ErrDuplicateTask", err)
	}
}
