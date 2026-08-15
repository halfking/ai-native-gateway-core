package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

type Repository struct {
	db  DB
	now func() time.Time
}

type RepositoryOption func(*Repository)

func WithClock(now func() time.Time) RepositoryOption {
	return func(r *Repository) {
		if now != nil {
			r.now = now
		}
	}
}

func NewRepository(db DB, opts ...RepositoryOption) *Repository {
	r := &Repository{db: db, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

const taskColumns = `id, tenant_id, request_id, session_id, protocol, endpoint,
 request_snapshot_ciphertext, snapshot_version, encryption_key_id, request_hash,
 status, error_kind, reason_code, attempt_count, next_retry_at, deadline_at,
 lease_owner, lease_until, fencing_token, semantic_content_committed, commit_state,
 commit_metadata, result_ciphertext, result_object_ref, result_hash, result_version,
 content_type, policy, connection_attached, last_disconnect_at, expires_at,
 created_at, updated_at, completed_at`

func (r *Repository) CreateAndClaim(ctx context.Context, in CreateTaskInput, owner string, leaseDuration time.Duration) (*Task, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("durable repository has no database")
	}
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	if len(in.Policy) == 0 {
		in.Policy = json.RawMessage(`{}`)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create durable task: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	q := `INSERT INTO durable_llm_tasks
 (id, tenant_id, request_id, session_id, protocol, endpoint, request_snapshot_ciphertext,
  snapshot_version, encryption_key_id, request_hash, status, attempt_count, next_retry_at, deadline_at,
  lease_owner, lease_until, fencing_token, semantic_content_committed, commit_state, policy)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'running',1,now(),$11,$12,now()+$13,1,false,'none',$14)
 RETURNING ` + taskColumns
	t := &Task{}
	if err := scanTask(tx.QueryRow(ctx, q, in.ID, in.TenantID, in.RequestID, in.SessionID, in.Protocol, in.Endpoint,
		in.SnapshotCiphertext, in.SnapshotVersion, in.EncryptionKeyID, in.RequestHash, in.DeadlineAt, owner, leaseDuration, in.Policy), t); err != nil {
		return nil, fmt.Errorf("create durable task: scan inserted task: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{"task_id": in.ID, "fencing_token": int64(1)})
	if err != nil {
		return nil, fmt.Errorf("create durable task: marshal transition metadata: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO request_state_transitions
 (request_id, tenant_id, transition_type, from_state, to_state, attempt_no, metadata, seq)
 SELECT $1,$2,'survival_running','accepted','running',1,$3,
        COALESCE(MAX(seq), 0) + 1
 FROM request_state_transitions
 WHERE request_id=$1`, in.RequestID, in.TenantID, metadata); err != nil {
		return nil, fmt.Errorf("create durable task: record running transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create durable task: commit transaction: %w", err)
	}
	metrics.SurvivalActiveTasks.WithLabelValues(t.TenantID).Inc()
	return t, nil
}

func (r *Repository) Claim(ctx context.Context, owner string, leaseDuration time.Duration) (*Task, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("claim durable task: repository has no database")
	}
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim durable task: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := `WITH candidate AS (
 SELECT id FROM durable_llm_tasks
	 WHERE status IN ('waiting_recovery','retry_scheduled','running')
	   AND commit_state IN ('none','metadata') AND deadline_at > now()
	   AND next_retry_at <= now() AND (lease_until IS NULL OR lease_until <= now())

 ORDER BY next_retry_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1
), claimed AS (
 UPDATE durable_llm_tasks t SET status='running', lease_owner=$1, lease_until=now()+$2,
   fencing_token=t.fencing_token+1, attempt_count=t.attempt_count+1, updated_at=now()
 FROM candidate c WHERE t.id=c.id RETURNING t.*
) SELECT ` + taskColumns + ` FROM claimed`
	t := &Task{}
	if err := scanTask(tx.QueryRow(ctx, q, owner, leaseDuration), t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTask
		}
		return nil, fmt.Errorf("claim durable task: scan claimed task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim durable task: commit transaction: %w", err)
	}
	return t, nil
}

func (r *Repository) Renew(ctx context.Context, id, owner string, token int64, leaseDuration time.Duration) (*Task, error) {
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	return r.mutateReturning(ctx, `UPDATE durable_llm_tasks SET lease_until=now()+$4, updated_at=now()
 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running' AND completed_at IS NULL
 RETURNING `+taskColumns, id, owner, token, leaseDuration)
}

func (r *Repository) Checkpoint(ctx context.Context, id, owner string, token int64, state string, metadata json.RawMessage, semantic bool) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	return r.mutate(ctx, `UPDATE durable_llm_tasks SET commit_state=$4, commit_metadata=$5,
 semantic_content_committed=$6, updated_at=now() WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3
 AND status NOT IN ('completed','permanent_failed','expired','cancelled','resume_safety_blocked')`, id, owner, token, state, metadata, semantic)
}

// HoldUntil keeps the current lease while persisting a future retry time.
func (r *Repository) HoldUntil(ctx context.Context, id, owner string, token int64, nextRetryAt time.Time, reason string, leaseDuration time.Duration) error {
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	return r.mutate(ctx, `UPDATE durable_llm_tasks SET next_retry_at=$4, reason_code=$5,
	 lease_until=GREATEST(now()+$6, $4+$6), updated_at=now()
	 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'
	   AND completed_at IS NULL AND commit_state IN ('none','metadata')`,
		id, owner, token, nextRetryAt, reason, leaseDuration)
}

func (r *Repository) Reschedule(ctx context.Context, id, owner string, token int64, nextRetryAt time.Time, reason string) error {
	return r.mutate(ctx, `UPDATE durable_llm_tasks SET status='retry_scheduled', next_retry_at=$4,
 reason_code=$5, lease_owner=NULL, lease_until=NULL, updated_at=now()
 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND completed_at IS NULL
   AND commit_state IN ('none','metadata')`, id, owner, token, nextRetryAt, reason)
}

func (r *Repository) CommitTerminal(ctx context.Context, id, owner string, token int64, result TerminalResult) error {
	if result.Status == "" {
		result.Status = StatusPermanentFailed
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("commit terminal task: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var tenant string
	err = tx.QueryRow(ctx, `UPDATE durable_llm_tasks SET status=$4, reason_code=$5, error_kind=$6,
 result_ciphertext=$7, result_object_ref=$8, result_hash=$9, result_version=$10, content_type=$11,
 commit_state='terminal', completed_at=now(), lease_owner=NULL, lease_until=NULL, updated_at=now()
 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND completed_at IS NULL
 RETURNING tenant_id`, id, owner, token, result.Status, result.ReasonCode, result.ErrorKind,
		result.ResultCiphertext, result.ResultObjectRef, result.ResultHash, result.ResultVersion, result.ContentType).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.SurvivalLeaseConflictsTotal.Inc()
		return ErrLeaseConflict
	}
	if err != nil {
		return fmt.Errorf("commit terminal task: update task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit terminal task: commit transaction: %w", err)
	}
	metrics.SurvivalActiveTasks.WithLabelValues(tenant).Dec()
	return nil
}

// ReapUnsafeCommitState fences and terminates non-terminal tasks that cannot be replayed safely.
func (r *Repository) ReapUnsafeCommitState(ctx context.Context) (int64, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("reap unsafe durable tasks: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT id FROM durable_llm_tasks
 WHERE completed_at IS NULL
   AND status NOT IN ('completed','permanent_failed','expired','cancelled','resume_safety_blocked')
   AND commit_state IN ('content','tool_call','terminal')
 FOR UPDATE SKIP LOCKED
), updated AS (
 UPDATE durable_llm_tasks t SET status='resume_safety_blocked',
   reason_code='survival_resume_safety_blocked', commit_state='terminal',
   result_version=GREATEST(result_version, 1), completed_at=now(),
   lease_owner=NULL, lease_until=NULL, fencing_token=fencing_token+1, updated_at=now()
 FROM candidates c WHERE t.id=c.id RETURNING t.tenant_id
) SELECT tenant_id FROM updated`)
	if err != nil {
		return 0, fmt.Errorf("reap unsafe durable tasks: update candidates: %w", err)
	}
	tenants, err := collectTenants(rows)
	if err != nil {
		return 0, fmt.Errorf("reap unsafe durable tasks: collect tenants: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reap unsafe durable tasks: commit transaction: %w", err)
	}
	for _, tenant := range tenants {
		metrics.SurvivalActiveTasks.WithLabelValues(tenant).Dec()
		metrics.SurvivalResumeSafetyBlockedTotal.WithLabelValues("unknown").Inc()
	}
	return int64(len(tenants)), nil
}

// ReapDeadlines fences and expires every non-terminal task beyond its durable deadline.
func (r *Repository) ReapDeadlines(ctx context.Context) (int64, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("reap deadline durable tasks: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT id FROM durable_llm_tasks
 WHERE deadline_at <= now() AND completed_at IS NULL
   AND status NOT IN ('completed','permanent_failed','expired','cancelled','resume_safety_blocked')
 FOR UPDATE SKIP LOCKED
), updated AS (
 UPDATE durable_llm_tasks t SET status='expired', reason_code='survival_expired',
   commit_state='terminal', result_version=GREATEST(result_version, 1), completed_at=now(),
   lease_owner=NULL, lease_until=NULL, fencing_token=fencing_token+1, updated_at=now()
 FROM candidates c WHERE t.id=c.id RETURNING t.tenant_id
) SELECT tenant_id FROM updated`)
	if err != nil {
		return 0, fmt.Errorf("reap deadline durable tasks: update candidates: %w", err)
	}
	tenants, err := collectTenants(rows)
	if err != nil {
		return 0, fmt.Errorf("reap deadline durable tasks: collect tenants: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reap deadline durable tasks: commit transaction: %w", err)
	}
	for _, tenant := range tenants {
		metrics.SurvivalActiveTasks.WithLabelValues(tenant).Dec()
	}
	return int64(len(tenants)), nil
}

func collectTenants(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	return tenants, rows.Err()
}

func (r *Repository) mutate(ctx context.Context, query string, args ...any) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("durable task mutation: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	res, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("durable task mutation: execute: %w", err)
	}
	if res.RowsAffected() != 1 {
		metrics.SurvivalLeaseConflictsTotal.Inc()
		return ErrLeaseConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("durable task mutation: commit transaction: %w", err)
	}
	return nil
}

func (r *Repository) mutateReturning(ctx context.Context, query string, args ...any) (*Task, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable task returning mutation: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	t := &Task{}
	if err := scanTask(tx.QueryRow(ctx, query, args...), t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			metrics.SurvivalLeaseConflictsTotal.Inc()
			return nil, ErrLeaseConflict
		}
		return nil, fmt.Errorf("durable task returning mutation: scan task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable task returning mutation: commit transaction: %w", err)
	}
	return t, nil
}

func scanTask(row pgx.Row, t *Task) error {
	var leaseUntil, lastDisconnect, expires, completed *time.Time
	var owner, errorKind, reason, resultCipher, resultRef, resultHash, content *string
	err := row.Scan(&t.ID, &t.TenantID, &t.RequestID, &t.SessionID, &t.Protocol, &t.Endpoint,
		&t.SnapshotCiphertext, &t.SnapshotVersion, &t.EncryptionKeyID, &t.RequestHash,
		&t.Status, &errorKind, &reason, &t.AttemptCount, &t.NextRetryAt, &t.DeadlineAt,
		&owner, &leaseUntil, &t.FencingToken, &t.SemanticContentCommitted, &t.CommitState,
		&t.CommitMetadata, &resultCipher, &resultRef, &resultHash, &t.ResultVersion,
		&content, &t.Policy, &t.ConnectionAttached, &lastDisconnect, &expires,
		&t.CreatedAt, &t.UpdatedAt, &completed)
	if err != nil {
		return err
	}
	if owner != nil {
		t.LeaseOwner = *owner
	}
	t.LeaseUntil = leaseUntil
	t.ErrorKind, t.ReasonCode = deref(errorKind), deref(reason)
	t.ResultCiphertext, t.ResultObjectRef, t.ResultHash = deref(resultCipher), deref(resultRef), deref(resultHash)
	t.ContentType = deref(content)
	t.LastDisconnectAt, t.ExpiresAt, t.CompletedAt = lastDisconnect, expires, completed
	return nil
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
