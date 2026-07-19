package bg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ProbeQueueTTL = 5 * time.Minute

type ProbeQueueStatus string

const (
	ProbeQueueReady   ProbeQueueStatus = "ready"
	ProbeQueueRunning ProbeQueueStatus = "running"
	ProbeQueueSuccess ProbeQueueStatus = "success"
	ProbeQueueFailed  ProbeQueueStatus = "failed"
	ProbeQueueExpired ProbeQueueStatus = "expired"
)

type ProbeQueueTask struct {
	ID           int64
	CredentialID int64
	ProviderID   int64
	TenantID     string
	Canonical    string
	RawModel     string
	Outbound     string
	Command      string
	Mode         string
	Priority     int16
	ReasonCode   string
	ReasonDetail string
	Attempt      int
	MaxAttempts  int
	NextRunAt    time.Time
	LeaseUntil   *time.Time
	Source       string
	SourceEvent  string
	ParentReqID  string
	DedupKey     string
	ExpiresAt    time.Time
}

type ProbeQueueResult struct {
	ID           int64
	Status       ProbeQueueStatus
	ReasonCode   string
	ReasonDetail string
	HTTPStatus   int
	LatencyMs    int
	BodyPreview  string
	NextRunAt    *time.Time
	LeaseUntil   *time.Time
	FinishedAt   *time.Time
}

type ProbeQueue struct {
	db *pgxpool.Pool
}

func NewProbeQueue(db *pgxpool.Pool) *ProbeQueue {
	return &ProbeQueue{db: db}
}

// Enqueue adds a task once for its active dedup key. A duplicate is a normal
// outcome: another source already owns the same probe within the TTL window.
func (q *ProbeQueue) Enqueue(ctx context.Context, task ProbeQueueTask) (int64, bool, error) {
	if q == nil || q.db == nil {
		return 0, false, fmt.Errorf("enqueue probe failed: database is unavailable (dedup_key=%s)", task.DedupKey)
	}
	if task.TenantID == "" {
		task.TenantID = "default"
	}
	if task.Mode == "" {
		task.Mode = "single"
	}
	if task.Source == "" {
		task.Source = "request_failure"
	}
	if task.MaxAttempts <= 0 {
		task.MaxAttempts = 1
	}
	if _, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET status='expired', updated_at=now()
		WHERE dedup_key=$1 AND status IN ('ready','running') AND expires_at <= now()`, task.DedupKey); err != nil {
		return 0, false, fmt.Errorf("enqueue probe failed: expire stale task: %w (dedup_key=%s)", err, task.DedupKey)
	}
	var id int64
	err := q.db.QueryRow(ctx, `
		INSERT INTO credential_probe_queue (
			credential_id, provider_id, tenant_id, canonical_model, raw_model,
			outbound_model, probe_command, probe_mode, priority, reason_code,
			reason_detail, max_attempts, source, source_event_id, parent_request_id,
			dedup_key, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now()+$17)
		ON CONFLICT DO NOTHING
		RETURNING id`, task.CredentialID, task.ProviderID, task.TenantID,
		nilString(task.Canonical), task.RawModel, nilString(task.Outbound),
		task.Command, task.Mode, task.Priority, nilString(task.ReasonCode),
		nilString(task.ReasonDetail), task.MaxAttempts, task.Source,
		nilString(task.SourceEvent), nilString(task.ParentReqID), task.DedupKey,
		ProbeQueueTTL).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("enqueue probe failed: %w (dedup_key=%s)", err, task.DedupKey)
	}
	return id, true, nil
}

// Claim atomically leases ready tasks. It is safe for multiple gateway
// instances to call this concurrently because rows are locked with SKIP LOCKED.
func (q *ProbeQueue) Claim(ctx context.Context, limit int, lease time.Duration) ([]ProbeQueueTask, error) {
	if q == nil || q.db == nil {
		return nil, fmt.Errorf("claim probes failed: database is unavailable (limit=%d)", limit)
	}
	if limit <= 0 {
		limit = 1
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim probes failed: begin transaction: %w (limit=%d)", err, limit)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT id FROM credential_probe_queue
			WHERE status='ready' AND next_run_at <= now() AND expires_at > now()
			ORDER BY priority DESC, next_run_at, id
			FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE credential_probe_queue q
		SET status='running', attempt=q.attempt+1,
			lease_until=now()+$2, started_at=COALESCE(q.started_at, now()), updated_at=now()
		FROM picked WHERE q.id=picked.id
		RETURNING q.id, q.credential_id, COALESCE(q.provider_id,0), q.tenant_id,
			COALESCE(q.canonical_model,''), q.raw_model, COALESCE(q.outbound_model,''),
			q.probe_command, q.probe_mode, q.priority, q.attempt, q.max_attempts,
			q.next_run_at, q.lease_until, q.source, COALESCE(q.source_event_id,''),
			COALESCE(q.parent_request_id,''), q.dedup_key, q.expires_at`, limit, lease)
	if err != nil {
		return nil, fmt.Errorf("claim probes failed: select tasks: %w (limit=%d)", err, limit)
	}
	defer rows.Close()
	var tasks []ProbeQueueTask
	for rows.Next() {
		var task ProbeQueueTask
		if err := rows.Scan(&task.ID, &task.CredentialID, &task.ProviderID, &task.TenantID, &task.Canonical,
			&task.RawModel, &task.Outbound, &task.Command, &task.Mode, &task.Priority,
			&task.Attempt, &task.MaxAttempts, &task.NextRunAt, &task.LeaseUntil,
			&task.Source, &task.SourceEvent, &task.ParentReqID, &task.DedupKey, &task.ExpiresAt); err != nil {
			return nil, fmt.Errorf("claim probes failed: scan task: %w (limit=%d)", err, limit)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim probes failed: iterate tasks: %w (limit=%d)", err, limit)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim probes failed: commit lease: %w (limit=%d)", err, limit)
	}
	return tasks, nil
}

func (q *ProbeQueue) Complete(ctx context.Context, id int64, result ProbeQueueResult) error {
	if q == nil || q.db == nil {
		return fmt.Errorf("complete probe failed: database is unavailable (queue_id=%d)", id)
	}
	terminal := result.Status == ProbeQueueSuccess || result.Status == ProbeQueueFailed || result.Status == ProbeQueueExpired
	if terminal && result.FinishedAt == nil {
		now := time.Now()
		result.FinishedAt = &now
	}
	_, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET status=$2, reason_code=$3, reason_detail=$4, result_http_status=$5,
			result_latency_ms=$6, result_body_preview=$7, next_run_at=COALESCE($8,next_run_at),
			lease_until=$9, finished_at=$10, updated_at=now()
		WHERE id=$1`, id, result.Status, nilString(result.ReasonCode), nilString(result.ReasonDetail),
		result.HTTPStatus, result.LatencyMs, nilString(result.BodyPreview), result.NextRunAt,
		result.LeaseUntil, result.FinishedAt)
	if err != nil {
		return fmt.Errorf("complete probe failed: %w (queue_id=%d)", err, id)
	}
	return nil
}

func (q *ProbeQueue) RequeueExpiredLeases(ctx context.Context) (int64, error) {
	if q == nil || q.db == nil {
		return 0, fmt.Errorf("requeue expired probes failed: database is unavailable (queue=credential_probe_queue)")
	}
	result, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET status=CASE WHEN expires_at <= now() THEN 'expired' ELSE 'ready' END,
			lease_until=NULL, updated_at=now()
		WHERE status='running' AND lease_until < now()`)
	if err != nil {
		return 0, fmt.Errorf("requeue expired probes failed: %w (queue=credential_probe_queue)", err)
	}
	return result.RowsAffected(), nil
}

func nilString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
