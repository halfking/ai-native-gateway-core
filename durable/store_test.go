package durable

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRepository_RealPostgresLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping durable PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, durableTestSchema); err != nil {
		t.Fatalf("create temporary durable schema: %v", err)
	}
	repo := NewRepository(conn)
	deadline := time.Now().Add(time.Hour)
	task, err := repo.CreateAndClaim(ctx, CreateTaskInput{
		ID:                 "00000000-0000-0000-0000-000000000701",
		TenantID:           "tenant-durable",
		RequestID:          "request-durable",
		SessionID:          "session-durable",
		Protocol:           "openai-chat",
		Endpoint:           "/v1/chat/completions",
		SnapshotCiphertext: "encrypted-request",
		SnapshotVersion:    1,
		EncryptionKeyID:    "key-1",
		RequestHash:        "request-hash",
		DeadlineAt:         deadline,
	}, "gateway-a/request-durable", time.Minute)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if task.Status != StatusRunning || task.FencingToken != 1 || task.AttemptCount != 1 || task.LeaseOwner != "gateway-a/request-durable" {
		t.Fatalf("created task = %+v", task)
	}

	var transitions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM request_state_transitions WHERE request_id='request-durable'`).Scan(&transitions); err != nil {
		t.Fatalf("read transition: %v", err)
	}
	if transitions != 1 {
		t.Fatalf("CreateAndClaim transition count = %d, want 1", transitions)
	}

	if err := repo.Checkpoint(ctx, task.ID, task.LeaseOwner, task.FencingToken, CommitContent, nil, true); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := repo.Reschedule(ctx, task.ID, task.LeaseOwner, task.FencingToken, time.Now().Add(time.Minute), "retry"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("content checkpoint reschedule error = %v, want ErrLeaseConflict", err)
	}
	if n, err := repo.ReapUnsafeCommitState(ctx); err != nil || n != 1 {
		t.Fatalf("ReapUnsafeCommitState = %d, %v; want 1, nil", n, err)
	}
	var status, reason string
	if err := conn.QueryRow(ctx, `SELECT status, reason_code FROM durable_llm_tasks WHERE id=$1`, task.ID).Scan(&status, &reason); err != nil {
		t.Fatalf("read safety-blocked task: %v", err)
	}
	if status != StatusSafetyBlocked || reason != "survival_resume_safety_blocked" {
		t.Fatalf("safety-blocked row = %q/%q", status, reason)
	}

	waiting, err := repo.CreateAndClaim(ctx, CreateTaskInput{
		ID:                 "00000000-0000-0000-0000-000000000702",
		TenantID:           "tenant-durable",
		RequestID:          "request-expired",
		SessionID:          "session-durable",
		Protocol:           "openai-chat",
		Endpoint:           "/v1/chat/completions",
		SnapshotCiphertext: "encrypted-request",
		SnapshotVersion:    1,
		EncryptionKeyID:    "key-1",
		RequestHash:        "request-hash-2",
		DeadlineAt:         time.Now().Add(time.Minute),
	}, "gateway-a/request-expired", time.Minute)
	if err != nil {
		t.Fatalf("create deadline task: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE durable_llm_tasks SET deadline_at=now()-interval '1 second' WHERE id=$1`, waiting.ID); err != nil {
		t.Fatalf("expire task: %v", err)
	}
	if n, err := repo.ReapDeadlines(ctx); err != nil || n != 1 {
		t.Fatalf("ReapDeadlines = %d, %v; want 1, nil", n, err)
	}
	if err := conn.QueryRow(ctx, `SELECT status, reason_code FROM durable_llm_tasks WHERE id=$1`, waiting.ID).Scan(&status, &reason); err != nil {
		t.Fatalf("read expired task: %v", err)
	}
	if status != StatusExpired || reason != "survival_expired" {
		t.Fatalf("expired row = %q/%q", status, reason)
	}

	// HoldUntil keeps the lease while scheduling a future retry: the row must
	// stay running with its lease extended past the next retry time, so only a
	// crash (lease expiry) can hand ownership to the background worker.
	held, err := repo.CreateAndClaim(ctx, CreateTaskInput{
		ID:                 "00000000-0000-0000-0000-000000000703",
		TenantID:           "tenant-durable",
		RequestID:          "request-held",
		SessionID:          "session-durable",
		Protocol:           "openai-chat",
		Endpoint:           "/v1/chat/completions",
		SnapshotCiphertext: "encrypted-request",
		SnapshotVersion:    1,
		EncryptionKeyID:    "key-1",
		RequestHash:        "request-hash-3",
		DeadlineAt:         deadline,
	}, "gateway-a/request-held", time.Minute)
	if err != nil {
		t.Fatalf("create held task: %v", err)
	}
	nextRetry := time.Now().Add(2 * time.Minute)
	if err := repo.HoldUntil(ctx, held.ID, held.LeaseOwner, held.FencingToken, nextRetry, "provider_throttled", time.Minute); err != nil {
		t.Fatalf("HoldUntil: %v", err)
	}
	var heldStatus, heldReason, leaseOwner string
	var leaseUntil, nextRetryAt time.Time
	if err := conn.QueryRow(ctx, `SELECT status, reason_code, lease_owner, lease_until, next_retry_at FROM durable_llm_tasks WHERE id=$1`, held.ID).
		Scan(&heldStatus, &heldReason, &leaseOwner, &leaseUntil, &nextRetryAt); err != nil {
		t.Fatalf("read held task: %v", err)
	}
	if heldStatus != StatusRunning || heldReason != "provider_throttled" || leaseOwner != "gateway-a/request-held" {
		t.Fatalf("held row = status:%q reason:%q owner:%q", heldStatus, heldReason, leaseOwner)
	}
	if !leaseUntil.After(nextRetry) {
		t.Fatalf("HoldUntil must extend lease past next_retry_at: lease_until=%v next_retry_at=%v", leaseUntil, nextRetry)
	}
	if nextRetryAt.Sub(time.Now()) < time.Minute {
		t.Fatalf("next_retry_at not scheduled in the future: %v", nextRetryAt)
	}

	// A running task whose lease is still valid must NOT be claimable by the
	// worker (global lease-expiry gate, not the old status-excluding rule).
	if _, err := repo.Claim(ctx, "gateway-b/worker", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Fatalf("Claim during valid lease = %v, want ErrNoTask", err)
	}
}

type workerStoreStub struct {
	owner       string
	claimCount  int
	reschedules int
	commits     int
}

func (s *workerStoreStub) Claim(_ context.Context, owner string, _ time.Duration) (*Task, error) {
	s.owner = owner
	s.claimCount++
	if s.claimCount > 1 {
		return nil, ErrNoTask
	}
	return &Task{ID: "task", LeaseOwner: owner, FencingToken: 2}, nil
}
func (*workerStoreStub) Renew(context.Context, string, string, int64, time.Duration) (*Task, error) {
	return nil, nil
}
func (*workerStoreStub) Checkpoint(context.Context, string, string, int64, string, json.RawMessage, bool) error {
	return nil
}
func (s *workerStoreStub) Reschedule(context.Context, string, string, int64, time.Time, string) error {
	s.reschedules++
	return nil
}
func (s *workerStoreStub) CommitTerminal(context.Context, string, string, int64, TerminalResult) error {
	s.commits++
	return nil
}

type workerExecutorStub struct{ err error }

func (e workerExecutorStub) Execute(context.Context, *Task) (ExecutionResult, error) {
	return ExecutionResult{Terminal: TerminalResult{Status: StatusPermanentFailed}}, e.err
}

func TestRecoveryWorkerUsesConfiguredOwnerAndIsolatesExecutionError(t *testing.T) {
	store := &workerStoreStub{}
	worker, err := NewRecoveryWorker(store, workerExecutorStub{err: errors.New("upstream failed")}, WorkerConfig{
		Owner:     "gateway-instance-1",
		BatchSize: 2,
		Now:       func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewRecoveryWorker: %v", err)
	}
	if err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce must return the isolated executor failure")
	}
	if store.owner != "gateway-instance-1" || store.reschedules != 1 || store.commits != 0 {
		t.Fatalf("worker store calls = owner:%q reschedules:%d commits:%d", store.owner, store.reschedules, store.commits)
	}
}

func TestNewRecoveryWorkerRejectsMissingDependency(t *testing.T) {
	if _, err := NewRecoveryWorker(nil, workerExecutorStub{}, WorkerConfig{}); !errors.Is(err, ErrWorkerNotConfigured) {
		t.Fatalf("nil store error = %v", err)
	}
	if _, err := NewRecoveryWorker(&workerStoreStub{}, nil, WorkerConfig{}); !errors.Is(err, ErrWorkerNotConfigured) {
		t.Fatalf("nil executor error = %v", err)
	}
}

const durableTestSchema = `
CREATE TEMP TABLE durable_llm_tasks (
 id uuid PRIMARY KEY, tenant_id text NOT NULL, request_id text NOT NULL, session_id text NOT NULL,
 protocol text NOT NULL, endpoint text NOT NULL, request_snapshot_ciphertext text NOT NULL,
 snapshot_version integer NOT NULL, encryption_key_id text NOT NULL, request_hash text NOT NULL,
 status text NOT NULL, error_kind text, reason_code text, attempt_count integer NOT NULL,
 next_retry_at timestamptz NOT NULL, deadline_at timestamptz NOT NULL, lease_owner text,
 lease_until timestamptz, fencing_token bigint NOT NULL, semantic_content_committed boolean NOT NULL,
 commit_state text NOT NULL, commit_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 result_ciphertext text, result_object_ref text, result_hash text, result_version bigint NOT NULL DEFAULT 0,
 content_type text, policy jsonb NOT NULL DEFAULT '{}'::jsonb, connection_attached boolean NOT NULL DEFAULT true,
 last_disconnect_at timestamptz, expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE request_state_transitions (
 request_id text NOT NULL, tenant_id text NOT NULL, transition_type text NOT NULL,
 from_state text, to_state text, attempt_no integer, metadata jsonb, seq bigint NOT NULL,
 UNIQUE (request_id, seq)
) ON COMMIT PRESERVE ROWS;
`
