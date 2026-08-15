package durabletask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// Store persists durable tasks, leases, events, results, and projection outbox rows.
type Store struct {
	db DB
	kr *secret.Keyring
}

// NewStore creates a PostgreSQL durable task store.
func NewStore(db DB, kr *secret.Keyring) *Store { return &Store{db: db, kr: kr} }

// CreateAndClaimParams contains the immutable request snapshot and initial foreground lease.
type CreateAndClaimParams struct {
	Snapshot        DurableRequestSnapshotV1
	LeaseOwner      string
	LeaseUntil      time.Time
	DeadlineAt      time.Time
	ResultExpiresAt time.Time
}

// CreateAndClaim atomically creates a running task, its initial fence, and accepted-to-running event.
func (s *Store) CreateAndClaim(ctx context.Context, params CreateAndClaimParams) (Lease, error) {
	if s == nil || s.db == nil {
		return Lease{}, errors.New("durabletask: store database is not configured")
	}
	if params.LeaseOwner == "" || params.LeaseUntil.IsZero() || params.DeadlineAt.IsZero() {
		return Lease{}, errors.New("durabletask: lease owner, lease until, and deadline are required")
	}
	if !params.DeadlineAt.After(params.LeaseUntil) {
		return Lease{}, errors.New("durabletask: deadline must be after initial lease")
	}
	if params.ResultExpiresAt.IsZero() {
		params.ResultExpiresAt = params.DeadlineAt.Add(24 * time.Hour)
	}
	if params.ResultExpiresAt.Before(params.DeadlineAt) {
		return Lease{}, errors.New("durabletask: result expiry must not precede deadline")
	}
	ciphertext, keyID, err := EncryptRequestSnapshotV1(params.Snapshot, s.kr)
	if err != nil {
		return Lease{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Lease{}, fmt.Errorf("durabletask: begin create: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	_, err = tx.Exec(ctx, `
		INSERT INTO durable_llm_tasks (
			id, tenant_id, request_id, parent_request_id, session_id, protocol, endpoint,
			request_snapshot_ciphertext, snapshot_version, encryption_key_id, request_hash,
			status, attempt_count, next_retry_at, deadline_at, lease_owner, lease_until,
			fencing_token, semantic_content_committed, commit_state, policy, expires_at,
			created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			'running',1,now(),$12,$13,$14,1,false,'none',$15,$16,now(),now()
		)`, params.Snapshot.TaskID, params.Snapshot.TenantID, params.Snapshot.RequestID,
		params.Snapshot.ParentRequestID, params.Snapshot.SessionID, params.Snapshot.ClientProtocol,
		params.Snapshot.Endpoint, ciphertext, SnapshotVersionV1, keyID, params.Snapshot.RequestHash,
		params.DeadlineAt, params.LeaseOwner, params.LeaseUntil, policyJSON(params.Snapshot.Policy),
		params.ResultExpiresAt)
	if err != nil {
		return Lease{}, fmt.Errorf("durabletask: insert task: %w", err)
	}
	if err = appendEvent(ctx, tx, Event{TaskID: params.Snapshot.TaskID, TenantID: params.Snapshot.TenantID,
		RequestID: params.Snapshot.RequestID, SessionID: params.Snapshot.SessionID, AttemptCount: 1,
		FromStatus: StatusAccepted, ToStatus: StatusRunning, ReasonCode: ReasonCreated, FencingToken: 1}); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Lease{}, fmt.Errorf("durabletask: commit create: %w", err)
	}
	return Lease{TaskID: params.Snapshot.TaskID, Owner: params.LeaseOwner, FencingToken: 1, LeaseUntil: params.LeaseUntil}, nil
}

// ClaimRunnable leases due replay-safe tasks using row locks and SKIP LOCKED.
func (s *Store) ClaimRunnable(ctx context.Context, owner string, limit int, lease time.Duration) (tasks []ClaimedTask, err error) {
	if s == nil || s.db == nil {
		return nil, errors.New("durabletask: store database is not configured")
	}
	if owner == "" {
		return nil, errors.New("durabletask: claim owner is required")
	}
	if limit <= 0 {
		limit = 1
	}
	if lease <= 0 {
		lease = time.Minute
	}
	leaseUntil := time.Now().Add(lease)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durabletask: begin claim: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT id, status AS from_status
			FROM durable_llm_tasks
			WHERE commit_state IN ('none','metadata')
			  AND status IN ('waiting_recovery','retry_scheduled','running')
			  AND (status <> 'running' OR lease_until < now())
			  AND next_retry_at <= now() AND deadline_at > now()
			  AND (lease_until IS NULL OR lease_until < now())
			ORDER BY next_retry_at, id FOR UPDATE SKIP LOCKED LIMIT $1
		), claimed AS (
			UPDATE durable_llm_tasks t SET status='running', lease_owner=$2, lease_until=$3,
				fencing_token=t.fencing_token+1, attempt_count=t.attempt_count+1, updated_at=now()
			FROM picked p WHERE t.id=p.id
			RETURNING t.id,t.tenant_id,t.request_id,t.session_id,t.request_hash,
				t.request_snapshot_ciphertext,t.snapshot_version,t.encryption_key_id,
				t.attempt_count,t.deadline_at,t.commit_state,t.fencing_token,p.from_status
		)
		SELECT * FROM claimed`, limit, owner, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("durabletask: claim runnable: %w", err)
	}
	type claimedEvent struct {
		task ClaimedTask
		from Status
	}
	var claimed []claimedEvent
	for rows.Next() {
		var item claimedEvent
		item.task.Owner = owner
		item.task.LeaseUntil = leaseUntil
		if err = rows.Scan(&item.task.TaskID, &item.task.TenantID, &item.task.RequestID,
			&item.task.SessionID, &item.task.RequestHash, &item.task.SnapshotCiphertext,
			&item.task.SnapshotVersion, &item.task.EncryptionKeyID, &item.task.AttemptCount,
			&item.task.DeadlineAt, &item.task.CommitState, &item.task.FencingToken, &item.from); err != nil {
			rows.Close()
			return nil, fmt.Errorf("durabletask: scan claim: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("durabletask: claim rows: %w", err)
	}
	rows.Close()
	for _, item := range claimed {
		if err = appendEvent(ctx, tx, Event{TaskID: item.task.TaskID, TenantID: item.task.TenantID,
			RequestID: item.task.RequestID, SessionID: item.task.SessionID, AttemptCount: item.task.AttemptCount,
			FromStatus: item.from, ToStatus: StatusRunning, ReasonCode: ReasonLeaseClaimed,
			FencingToken: item.task.FencingToken}); err != nil {
			return nil, err
		}
		tasks = append(tasks, item.task)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durabletask: commit claim: %w", err)
	}
	return tasks, nil
}

// RenewLease extends an active lease when its fencing capability still matches.
func (s *Store) RenewLease(ctx context.Context, lease Lease, until time.Time) error {
	if until.IsZero() {
		return errors.New("durabletask: lease renewal deadline is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE durable_llm_tasks SET lease_until=$4,updated_at=now()
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'`,
		lease.TaskID, lease.Owner, lease.FencingToken, until)
	if err != nil {
		return fmt.Errorf("durabletask: renew lease: %w", err)
	}
	return requireFencedRow(tag.RowsAffected())
}

// Checkpoint persists the strongest commit state before any semantic network write.
func (s *Store) Checkpoint(ctx context.Context, lease Lease, state CommitState) error {
	if state == CommitNone {
		return nil
	}
	if state != CommitMetadata && state != CommitContent && state != CommitToolCall && state != CommitTerminal {
		return fmt.Errorf("durabletask: invalid commit state %q", state)
	}
	semantic := state == CommitContent || state == CommitToolCall || state == CommitTerminal
	tag, err := s.db.Exec(ctx, `UPDATE durable_llm_tasks SET commit_state=$4,
		semantic_content_committed=semantic_content_committed OR $5,updated_at=now()
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'
		  AND CASE commit_state WHEN 'none' THEN 0 WHEN 'metadata' THEN 1 WHEN 'content' THEN 2 WHEN 'tool_call' THEN 3 ELSE 4 END
		      <= CASE $4 WHEN 'none' THEN 0 WHEN 'metadata' THEN 1 WHEN 'content' THEN 2 WHEN 'tool_call' THEN 3 ELSE 4 END`,
		lease.TaskID, lease.Owner, lease.FencingToken, state, semantic)
	if err != nil {
		return fmt.Errorf("durabletask: checkpoint: %w", err)
	}
	return requireFencedRow(tag.RowsAffected())
}

// RescheduleParams describes a replay-safe transition back to a runnable state.
type RescheduleParams struct {
	Status                Status
	NextRetryAt           time.Time
	ErrorKind, ReasonCode string
}

// Reschedule releases a replay-safe lease and schedules the next attempt.
func (s *Store) Reschedule(ctx context.Context, lease Lease, params RescheduleParams) (err error) {
	if params.Status != StatusWaitingRecovery && params.Status != StatusRetryScheduled {
		return fmt.Errorf("durabletask: invalid reschedule status %q", params.Status)
	}
	if params.NextRetryAt.IsZero() {
		return errors.New("durabletask: next retry time is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("durabletask: begin reschedule: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	var event Event
	err = tx.QueryRow(ctx, `UPDATE durable_llm_tasks SET status=$4,error_kind=$5,reason_code=$6,
		next_retry_at=$7,lease_owner=NULL,lease_until=NULL,updated_at=now()
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'
		  AND commit_state IN ('none','metadata')
		RETURNING id,tenant_id,request_id,session_id,attempt_count,fencing_token`,
		lease.TaskID, lease.Owner, lease.FencingToken, params.Status, params.ErrorKind,
		params.ReasonCode, params.NextRetryAt).Scan(&event.TaskID, &event.TenantID, &event.RequestID,
		&event.SessionID, &event.AttemptCount, &event.FencingToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("durabletask: reschedule: %w", err)
	}
	event.FromStatus = StatusRunning
	event.ToStatus = params.Status
	event.ReasonCode = params.ReasonCode
	if err = appendEvent(ctx, tx, event); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("durabletask: commit reschedule: %w", err)
	}
	return nil
}

// CompleteParams contains the normalized final response persisted atomically with completion.
type CompleteParams struct {
	Body        []byte
	ContentType string
}

// Complete encrypts and atomically commits the successful terminal result, event, and outbox row.
func (s *Store) Complete(ctx context.Context, lease Lease, params CompleteParams) (item OutboxItem, err error) {
	if len(params.Body) == 0 {
		return item, errors.New("durabletask: completed result body is required")
	}
	if params.ContentType == "" {
		params.ContentType = "application/json"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return item, fmt.Errorf("durabletask: begin complete: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	var tenantID, requestID, sessionID, requestHash string
	var attempt int
	var from Status
	var currentVersion int64
	err = tx.QueryRow(ctx, `SELECT tenant_id,request_id,session_id,request_hash,attempt_count,status,result_version
		FROM durable_llm_tasks WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running' FOR UPDATE`,
		lease.TaskID, lease.Owner, lease.FencingToken).Scan(&tenantID, &requestID, &sessionID, &requestHash, &attempt, &from, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrLeaseLost
	}
	if err != nil {
		return item, fmt.Errorf("durabletask: lock completion: %w", err)
	}
	hashBytes := sha256.Sum256(params.Body)
	resultHash := hex.EncodeToString(hashBytes[:])
	ciphertext, _, err := secret.EncryptWithAAD(params.Body, s.kr, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: tenantID, TaskID: lease.TaskID, RequestHash: requestHash})
	if err != nil {
		return item, fmt.Errorf("durabletask: encrypt result: %w", err)
	}
	version := currentVersion + 1
	tag, err := tx.Exec(ctx, `UPDATE durable_llm_tasks SET status='completed',result_ciphertext=$4,
		result_hash=$5,result_version=$6,content_type=$7,completed_at=now(),updated_at=now(),
		lease_owner=NULL,lease_until=NULL,reason_code=''
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'`,
		lease.TaskID, lease.Owner, lease.FencingToken, ciphertext, resultHash, version, params.ContentType)
	if err != nil {
		return item, fmt.Errorf("durabletask: complete: %w", err)
	}
	if err = requireFencedRow(tag.RowsAffected()); err != nil {
		return item, err
	}
	event := Event{TaskID: lease.TaskID, TenantID: tenantID, RequestID: requestID, SessionID: sessionID,
		AttemptCount: attempt, FromStatus: from, ToStatus: StatusCompleted, ReasonCode: "completed", FencingToken: lease.FencingToken}
	if err = appendEvent(ctx, tx, event); err != nil {
		return item, err
	}
	item = OutboxItem{TaskID: lease.TaskID, TenantID: tenantID, RequestID: requestID, SessionID: sessionID,
		Status: StatusCompleted, FencingToken: lease.FencingToken, ResultVersion: version, ResultHash: resultHash,
		RequestHash: requestHash, ResultCiphertext: ciphertext, ContentType: params.ContentType}
	if item.ID, err = insertOutbox(ctx, tx, item); err != nil {
		return item, err
	}
	if err = tx.Commit(ctx); err != nil {
		return item, fmt.Errorf("durabletask: commit completion: %w", err)
	}
	return item, nil
}

// FailureParams contains a terminal durable failure.
type FailureParams struct {
	Status                Status
	ReasonCode, ErrorKind string
}

// Fail atomically commits one terminal failure, event, and pending outbox row.
func (s *Store) Fail(ctx context.Context, lease Lease, params FailureParams) (item OutboxItem, err error) {
	if !isFailureTerminal(params.Status) {
		return item, ErrInvalidTerminalStatus
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return item, fmt.Errorf("durabletask: begin fail: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	var event Event
	err = tx.QueryRow(ctx, `UPDATE durable_llm_tasks SET status=$4,reason_code=$5,error_kind=$6,
		result_version=result_version+1,completed_at=now(),updated_at=now(),lease_owner=NULL,lease_until=NULL
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running'
		RETURNING id,tenant_id,request_id,session_id,attempt_count,fencing_token,result_version`,
		lease.TaskID, lease.Owner, lease.FencingToken, params.Status, params.ReasonCode, params.ErrorKind).
		Scan(&event.TaskID, &event.TenantID, &event.RequestID, &event.SessionID, &event.AttemptCount, &event.FencingToken, &item.ResultVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrLeaseLost
	}
	if err != nil {
		return item, fmt.Errorf("durabletask: fail: %w", err)
	}
	event.FromStatus = StatusRunning
	event.ToStatus = params.Status
	event.ReasonCode = params.ReasonCode
	if err = appendEvent(ctx, tx, event); err != nil {
		return item, err
	}
	item.TaskID = event.TaskID
	item.TenantID = event.TenantID
	item.RequestID = event.RequestID
	item.SessionID = event.SessionID
	item.Status = params.Status
	item.ReasonCode = params.ReasonCode
	item.FencingToken = event.FencingToken
	if item.ID, err = insertOutbox(ctx, tx, item); err != nil {
		return item, err
	}
	if err = tx.Commit(ctx); err != nil {
		return item, fmt.Errorf("durabletask: commit failure: %w", err)
	}
	return item, nil
}

// ReapDeadlines expires due non-terminal tasks while invalidating active leases.
// Tasks that already committed semantic content are protected — they must be
// terminalized only by ReapUnsafeCheckpoints so they can never be reclaimed.
func (s *Store) ReapDeadlines(ctx context.Context, limit int) ([]OutboxItem, error) {
	return s.reap(ctx, limit, `deadline_at <= now() AND commit_state IN ('none','metadata')`, StatusExpired, ReasonSurvivalExpired)
}

// ReapUnsafeCheckpoints terminalizes semantic-checkpointed tasks so they can never be reclaimed.
func (s *Store) ReapUnsafeCheckpoints(ctx context.Context, limit int) ([]OutboxItem, error) {
	return s.reap(ctx, limit, `commit_state IN ('content','tool_call','terminal')`, StatusResumeSafetyBlocked, ReasonResumeSafetyBlocked)
}

func (s *Store) reap(ctx context.Context, limit int, predicate string, status Status, reason string) (items []OutboxItem, err error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durabletask: begin reaper: %w", err)
	}
	defer rollbackUnlessCommitted(ctx, tx, &err)
	query := fmt.Sprintf(`WITH picked AS (SELECT id,status AS from_status FROM durable_llm_tasks
		WHERE status NOT IN ('completed','permanent_failed','expired','cancelled','resume_safety_blocked') AND %s
		ORDER BY deadline_at,id FOR UPDATE SKIP LOCKED LIMIT $1), changed AS (
		UPDATE durable_llm_tasks t SET status=$2,reason_code=$3,result_version=result_version+1,
			fencing_token=fencing_token+1,lease_owner=NULL,lease_until=NULL,completed_at=now(),updated_at=now()
		FROM picked p WHERE t.id=p.id RETURNING t.id,t.tenant_id,t.request_id,t.session_id,
			t.attempt_count,t.fencing_token,t.result_version,p.from_status)
		SELECT * FROM changed`, predicate)
	rows, err := tx.Query(ctx, query, limit, status, reason)
	if err != nil {
		return nil, fmt.Errorf("durabletask: reap: %w", err)
	}
	type reaped struct {
		item    OutboxItem
		attempt int
		from    Status
	}
	var all []reaped
	for rows.Next() {
		var r reaped
		r.item.Status = status
		r.item.ReasonCode = reason
		if err = rows.Scan(&r.item.TaskID, &r.item.TenantID, &r.item.RequestID, &r.item.SessionID, &r.attempt,
			&r.item.FencingToken, &r.item.ResultVersion, &r.from); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, r := range all {
		if err = appendEvent(ctx, tx, Event{TaskID: r.item.TaskID, TenantID: r.item.TenantID,
			RequestID: r.item.RequestID, SessionID: r.item.SessionID, AttemptCount: r.attempt, FromStatus: r.from,
			ToStatus: status, ReasonCode: reason, FencingToken: r.item.FencingToken}); err != nil {
			return nil, err
		}
		if r.item.ID, err = insertOutbox(ctx, tx, r.item); err != nil {
			return nil, err
		}
		items = append(items, r.item)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

// ActiveTaskCounts returns authoritative non-terminal task counts per tenant.
func (s *Store) ActiveTaskCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT tenant_id,count(*) FROM durable_llm_tasks
		WHERE status NOT IN ('completed','permanent_failed','expired','cancelled','resume_safety_blocked') GROUP BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("durabletask: active counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var tenant string
		var count int64
		if err := rows.Scan(&tenant, &count); err != nil {
			return nil, err
		}
		out[tenant] = count
	}
	return out, rows.Err()
}

// ClaimOutbox leases pending terminal projections for idempotent delivery.
func (s *Store) ClaimOutbox(ctx context.Context, owner string, limit int, lease time.Duration) (items []OutboxItem, err error) {
	if owner == "" {
		return nil, errors.New("durabletask: outbox owner required")
	}
	if limit <= 0 {
		limit = 100
	}
	if lease <= 0 {
		lease = time.Minute
	}
	until := time.Now().Add(lease)
	rows, err := s.db.Query(ctx, `WITH picked AS (SELECT id FROM durable_pending_outbox
		WHERE (status IN ('pending','failed') AND next_attempt_at<=now()) OR (status='processing' AND lease_until<now())
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT $1), claimed AS (
		UPDATE durable_pending_outbox o SET status='processing',lease_owner=$2,lease_until=$3,
			attempt_count=o.attempt_count+1,updated_at=now() FROM picked p WHERE o.id=p.id
			RETURNING o.id,o.task_id,o.tenant_id,o.request_id,o.session_id,o.projection_status,
				o.fencing_token,o.result_version,coalesce(o.result_hash,''),o.attempt_count,o.created_at)
		SELECT c.*,coalesce(t.reason_code,''),t.request_hash,coalesce(t.result_ciphertext,''),coalesce(t.content_type,'')
		FROM claimed c JOIN durable_llm_tasks t ON t.id=c.task_id`, limit, owner, until)
	if err != nil {
		return nil, fmt.Errorf("durabletask: claim outbox: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item OutboxItem
		var status string
		if err = rows.Scan(&item.ID, &item.TaskID, &item.TenantID, &item.RequestID,
			&item.SessionID, &status, &item.FencingToken, &item.ResultVersion, &item.ResultHash,
			&item.AttemptCount, &item.CreatedAt, &item.ReasonCode, &item.RequestHash,
			&item.ResultCiphertext, &item.ContentType); err != nil {
			return nil, err
		}
		item.Status = Status(status)
		items = append(items, item)
	}
	return items, rows.Err()
}

// MarkOutboxDelivered records a successfully projected outbox row.
func (s *Store) MarkOutboxDelivered(ctx context.Context, id int64, owner string) error {
	tag, err := s.db.Exec(ctx, `UPDATE durable_pending_outbox SET status='delivered',delivered_at=now(),updated_at=now(),lease_owner=NULL,lease_until=NULL
		WHERE id=$1 AND lease_owner=$2 AND status='processing'`, id, owner)
	if err != nil {
		return err
	}
	return requireFencedRow(tag.RowsAffected())
}

// MarkOutboxFailed releases a projection for retry.
func (s *Store) MarkOutboxFailed(ctx context.Context, id int64, owner, message string, next time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE durable_pending_outbox SET status='failed',last_error=$3,next_attempt_at=$4,
		updated_at=now(),lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND status='processing'`, id, owner, message, next)
	if err != nil {
		return err
	}
	return requireFencedRow(tag.RowsAffected())
}

func appendEvent(ctx context.Context, tx pgx.Tx, event Event) error {
	_, err := tx.Exec(ctx, `INSERT INTO durable_llm_task_events
		(task_id,tenant_id,request_id,session_id,attempt_no,event_type,from_status,to_status,reason_code,fencing_token)
		VALUES($1,$2,$3,$4,$5,'state_transition',$6,$7,$8,$9)`, event.TaskID, event.TenantID, event.RequestID,
		event.SessionID, event.AttemptCount, event.FromStatus, event.ToStatus, event.ReasonCode, event.FencingToken)
	if err != nil {
		return fmt.Errorf("durabletask: append event: %w", err)
	}
	return nil
}

func insertOutbox(ctx context.Context, tx pgx.Tx, item OutboxItem) (int64, error) {
	payload, _ := json.Marshal(map[string]any{"task_id": item.TaskID, "tenant_id": item.TenantID, "request_id": item.RequestID,
		"session_id": item.SessionID, "status": item.Status, "reason_code": item.ReasonCode, "fencing_token": item.FencingToken,
		"result_version": item.ResultVersion, "result_hash": item.ResultHash, "content_type": item.ContentType})
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO durable_pending_outbox
		(task_id,tenant_id,request_id,session_id,fencing_token,result_version,result_hash,projection_status,projection_payload)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(task_id,result_version) DO UPDATE SET updated_at=now()
		RETURNING id`, item.TaskID, item.TenantID, item.RequestID, item.SessionID, item.FencingToken, item.ResultVersion,
		item.ResultHash, item.Status, payload).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("durabletask: insert outbox: %w", err)
	}
	return id, nil
}

func rollbackUnlessCommitted(ctx context.Context, tx pgx.Tx, errp *error) {
	if errp != nil && *errp == nil {
		return
	}
	_ = tx.Rollback(ctx)
}
func requireFencedRow(rows int64) error {
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}
func policyJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
func isFailureTerminal(status Status) bool {
	return status == StatusPermanentFailed || status == StatusExpired || status == StatusCancelled || status == StatusResumeSafetyBlocked
}
