// store.go — DurableTaskStore：durable 任务的 PostgreSQL SSoT repository
// （doc 18 §11.3，SR-09）。
//
// 关键语义：
//   - CreateAndClaim：单事务「插入加密快照 + running + 初始 lease + fencing=1 +
//     commit_state='none' + accepted→running 事件」，前台 Coordinator 提交成功
//     后才可调用 ExecuteAttempt；
//   - 所有完成/重排/checkpoint 更新携带 (lease_owner, fencing_token) 条件，
//     更新 0 行即租约失效（ErrLeaseLost），旧 worker 必须丢弃结果；
//   - runnable claim / reschedule 带全局 commit_state IN ('none','metadata') 门禁；
//   - 终态在单事务内原子写入（含加密结果），终态不可回退。
//
// 表结构见 sql/migrations/startup/516_durable_llm_tasks.sql（SR-08）。
package durable

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// DB 是 Store 的最小依赖接口：*pgxpool.Pool 与 pgxmock.PgxPoolIface 均满足
// （后者内嵌 pgx.Tx，Begin 返回 pgx.Tx）。
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store 错误哨兵。
var (
	// ErrLeaseLost 更新影响 0 行：租约已过期/被抢占或任务已越过语义
	// checkpoint，调用方（旧 worker）必须丢弃结果、停止执行。
	ErrLeaseLost = errors.New("durable: lease lost or task transitioned (0 rows)")
	// ErrDuplicateTask 同 (tenant_id, request_id) 任务已存在（幂等防重）。
	ErrDuplicateTask = errors.New("durable: task already exists for (tenant, request)")
	// ErrNoKeyring 未配置密钥：durable 数据一律 fail closed（doc 18 §11.2）。
	ErrNoKeyring = secret.ErrAADNoKey
)

// Store 是 durable_llm_tasks / durable_llm_task_events 的 repository。
// 并发安全：无共享可变状态，所有方法独立开事务。
type Store struct {
	db DB
	kr *secret.Keyring
	// now 供测试注入时钟；nil 用 time.Now。
	now func() time.Time
}

// NewStore 构造 DurableTaskStore。kr 为 nil 时任何需要加/解密的操作
// fail closed（ErrNoKey）。
func NewStore(db DB, kr *secret.Keyring) *Store {
	return &Store{db: db, kr: kr}
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Task 是 durable_llm_tasks 行的 Go 投影（调度面字段；快照/结果密文不展开）。
type Task struct {
	ID                       string
	TenantID                 string
	RequestID                string
	SessionID                string
	Protocol                 string
	Endpoint                 string
	Status                   Status
	CommitState              CommitState
	SemanticContentCommitted bool
	ErrorKind                string
	ReasonCode               string
	AttemptCount             int
	FencingToken             int64
	LeaseOwner               string
	LeaseUntil               time.Time
	NextRetryAt              time.Time
	DeadlineAt               time.Time
	ExpiresAt                time.Time
	RequestHash              string
	SnapshotVersion          int
	EncryptionKeyID          string
	ResultVersion            int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
	CompletedAt              time.Time
}

// NewTask 是 CreateAndClaim 的输入。Snapshot 是版本化 DTO 明文，
// Store 在事务前用 durable-request 域 AAD 加密（绑定 tenant/task/request hash）。
type NewTask struct {
	TenantID        string
	RequestID       string
	SessionID       string
	Protocol        string
	Endpoint        string
	Snapshot        []byte
	SnapshotVersion int
	RequestHash     string
	DeadlineAt      time.Time
	// ExpiresAt = DeadlineAt + result_read_window（Redis 投影 TTL 基准）。
	ExpiresAt  time.Time
	LeaseOwner string
	LeaseUntil time.Time
	Policy     []byte
	Attempt    int
}

// Validate 校验创建输入的必填字段。
func (n NewTask) Validate() error {
	switch {
	case n.TenantID == "":
		return errors.New("durable: TenantID required")
	case n.RequestID == "":
		return errors.New("durable: RequestID required")
	case n.SessionID == "":
		return errors.New("durable: SessionID required")
	case len(n.Snapshot) == 0:
		return errors.New("durable: Snapshot required")
	case n.RequestHash == "":
		return errors.New("durable: RequestHash required")
	case n.DeadlineAt.IsZero():
		return errors.New("durable: DeadlineAt required")
	case n.ExpiresAt.IsZero():
		return errors.New("durable: ExpiresAt required")
	case n.LeaseOwner == "":
		return errors.New("durable: LeaseOwner required")
	case n.LeaseUntil.IsZero():
		return errors.New("durable: LeaseUntil required")
	}
	return nil
}

// CreateAndClaim 首次前台创建即领取（doc 18 §11.3）：
// 单事务插入任务（status='running'、lease_owner/lease_until、fencing_token=1、
// commit_state='none'、semantic_content_committed=false）并写 accepted→running
// append-only 事件。事务失败时不得发送 durable 已接管状态。
func (s *Store) CreateAndClaim(ctx context.Context, n NewTask) (*Task, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if s.kr == nil {
		return nil, ErrNoKeyring
	}
	taskID := uuid.NewString()
	binding := secret.AADBinding{TenantID: n.TenantID, TaskID: taskID, RequestHash: n.RequestHash}
	envelope, keyID, err := secret.EncryptWithAAD(n.Snapshot, s.kr, secret.AADDomainDurableRequest, binding)
	if err != nil {
		return nil, fmt.Errorf("durable: encrypt snapshot: %w", err)
	}
	if n.Attempt <= 0 {
		n.Attempt = 1
	}
	if n.SnapshotVersion <= 0 {
		n.SnapshotVersion = 1
	}

	now := s.clock()
	task := &Task{
		ID:              taskID,
		TenantID:        n.TenantID,
		RequestID:       n.RequestID,
		SessionID:       n.SessionID,
		Protocol:        n.Protocol,
		Endpoint:        n.Endpoint,
		Status:          StatusRunning,
		CommitState:     CommitStateNone,
		AttemptCount:    n.Attempt,
		FencingToken:    1,
		LeaseOwner:      n.LeaseOwner,
		LeaseUntil:      n.LeaseUntil,
		NextRetryAt:     now,
		DeadlineAt:      n.DeadlineAt,
		ExpiresAt:       n.ExpiresAt,
		RequestHash:     n.RequestHash,
		SnapshotVersion: n.SnapshotVersion,
		EncryptionKeyID: keyID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // 提交后 Rollback 是 no-op

	if _, err := tx.Exec(ctx, `
		INSERT INTO durable_llm_tasks (
			id, tenant_id, request_id, session_id, protocol, endpoint,
			request_snapshot_ciphertext, snapshot_version, encryption_key_id, request_hash,
			status, attempt_count, next_retry_at, deadline_at, expires_at,
			lease_owner, lease_until, fencing_token,
			semantic_content_committed, commit_state, policy,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			'running', $11, $17, $12, $13,
			$14, $15, 1,
			FALSE, 'none', $16,
			$17, $17
		)`,
		task.ID, n.TenantID, n.RequestID, n.SessionID, n.Protocol, n.Endpoint,
		envelope, n.SnapshotVersion, keyID, n.RequestHash,
		n.Attempt, n.DeadlineAt, n.ExpiresAt,
		n.LeaseOwner, n.LeaseUntil,
		jsonbOrNull(n.Policy),
		task.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateTask
		}
		return nil, fmt.Errorf("durable: insert task: %w", err)
	}

	if err := appendEvent(ctx, tx, task, "", StatusRunning, "create_and_claim", n.Attempt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit create: %w", err)
	}
	return task, nil
}

// appendEvent 写 durable_llm_task_events（append-only，§12.2）。
func appendEvent(ctx context.Context, tx pgx.Tx, task *Task, from, to Status, reason string, attempt int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO durable_llm_task_events (
			task_id, request_id, session_id, tenant_id,
			attempt, from_status, to_status, reason, fencing_token, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		task.ID, task.RequestID, task.SessionID, task.TenantID,
		attempt, string(from), string(to), reason, task.FencingToken, time.Now(),
	); err != nil {
		return fmt.Errorf("durable: append event: %w", err)
	}
	return nil
}

// jsonbOrNull 空切片落 NULL，非空 JSON 原样传递。
func jsonbOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// isUniqueViolation 识别 PG 23505。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
