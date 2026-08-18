package bg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ProbeQueueTTL = 5 * time.Minute

// ErrProbeLeaseLost means the task was reclaimed or its lease expired before
// this worker wrote its result. Callers must not retry Complete with the stale
// task because a newer owner may already have recorded a result.
var ErrProbeLeaseLost = errors.New("probe queue lease lost")

type ProbeQueueStatus string

const (
	ProbeQueueReady     ProbeQueueStatus = "ready"
	ProbeQueueRunning   ProbeQueueStatus = "running"
	ProbeQueueSuccess   ProbeQueueStatus = "success"
	ProbeQueueFailed    ProbeQueueStatus = "failed"
	ProbeQueueExpired   ProbeQueueStatus = "expired"
	ProbeQueueCancelled ProbeQueueStatus = "cancelled"
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
	LeaseToken   string
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

// ProbeTaskDetailSink is the enriched (OBS-BE5, 25 号 §6) self-check SSE sink.
// bg.ProbeStreamEvent cannot grow fields without touching
// active_probe_emitter.go (owned by another workstream), so the richer
// transition — origin (scheduled|error|manual) and next_retry_at (智能回退
// 下一跳) — travels through this separate structural interface instead.
// *admin.ProbeSSEHub implements PublishProbeTransition directly; main.go wires
// the concrete hub via SetProbeTaskDetailSink. When set, the queue prefers
// this sink for its own lifecycle transitions so each transition is emitted
// exactly once.
type ProbeTaskDetailSink interface {
	PublishProbeTransition(t ProbeTaskTransition)
}

// ProbeTaskTransition is the enriched probe-lifecycle transition carried to
// the 自检 tab SSE stream. All fields map 1:1 onto the admin-side
// ProbeStreamTask; NextRetryAtMs is unix milliseconds (0 = unset/none).
type ProbeTaskTransition struct {
	ID            string // stable lifecycle id (dedup_key based)
	TaskType      string // node_probe | integrity_verify | selfcheck
	Source        string // request_failure | no_candidates | admin | planner ...
	Status        string // pending | in-flight | ok | fail
	CredentialID  int64
	ProviderID    int64
	RawModel      string
	Attempt       int
	Origin        string // scheduled | error | manual
	Reason        string
	NextRetryAtMs int64 // backoff next hop, 0 = none
	TimestampMs   int64
}

// probeQueueOriginFromSource maps a queue source onto the tri-state display
// origin badge (26 号 §4: origin scheduled/error/manual). Error-triggered
// probes are "error"; admin-created tasks are "manual"; everything else
// (integrity planner, periodic, watchdog) is "scheduled".
func probeQueueOriginFromSource(source string) string {
	switch source {
	case "request_failure", "no_candidates":
		return "error"
	case "admin":
		return "manual"
	default:
		return "scheduled"
	}
}

type ProbeQueue struct {
	db *pgxpool.Pool
	// probeSink (2026-08-11) mirrors enqueue (pending) and claim (in-flight)
	// transitions to the self-check SSE stream. Optional — nil is a no-op.
	probeSink ProbeEventSink
	// detailSink (2026-08-15, OBS-BE5) is the enriched transition sink. When
	// set it takes precedence over probeSink for the queue's own transitions
	// so origin/next_retry_at reach the dashboard without double-emitting.
	detailSink ProbeTaskDetailSink
}

func NewProbeQueue(db *pgxpool.Pool) *ProbeQueue {
	return &ProbeQueue{db: db}
}

// SetProbeSink wires the self-check SSE sink for the durable integrity-probe
// queue lifecycle. Terminal completed/failed is emitted by the
// ProbeQueueWorker's ActiveProbeEmitter; this sink covers enqueue + claim.
func (q *ProbeQueue) SetProbeSink(sink ProbeEventSink) {
	if q != nil {
		q.probeSink = sink
	}
}

// SetProbeTaskDetailSink wires the enriched transition sink (OBS-BE5). When
// set, the queue's own enqueue/claim/re-arm transitions are published through
// it (carrying origin + next_retry_at) instead of the legacy probeSink, so
// each transition is emitted exactly once. Nil (or unset) keeps the legacy
// behaviour. Safe on a nil queue.
func (q *ProbeQueue) SetProbeTaskDetailSink(sink ProbeTaskDetailSink) {
	if q != nil {
		q.detailSink = sink
	}
}

// publishProbeTask is the shared hook for the durable queue's lifecycle events.
// Best-effort: a nil sink or a publish error never blocks the enqueue/claim.
//
// The task ID uses the dedup_key so the lifecycle (enqueue → claim → terminal)
// collapses into one SSE tile. dedup_key is the natural stable identifier:
// it is set at enqueue time, preserved across retries, and is what Enqueue's
// ON CONFLICT clause uses to recognise a duplicate.
//
// When the enriched detailSink is wired (OBS-BE5) the transition is published
// through it instead — same envelope plus origin + next_retry_at — so the
// 自检 tab can badge scheduled/error/manual and show the backoff next hop
// without double-emitting the event.
func (q *ProbeQueue) publishProbeTask(task ProbeQueueTask, status string) {
	if q == nil {
		return
	}
	if q.detailSink != nil {
		q.detailSink.PublishProbeTransition(ProbeTaskTransition{
			ID:            probeQueueLifecycleID(task),
			TaskType:      probeQueueTaskType(task.Command),
			Source:        task.Source,
			Status:        status,
			CredentialID:  task.CredentialID,
			ProviderID:    task.ProviderID,
			RawModel:      task.RawModel,
			Attempt:       task.Attempt,
			Origin:        probeQueueOriginFromSource(task.Source),
			Reason:        task.Source,
			NextRetryAtMs: probeQueueNextRetryMs(nilIfZero(task.NextRunAt)),
			TimestampMs:   time.Now().UnixMilli(),
		})
		return
	}
	if q.probeSink == nil {
		return
	}
	id := task.DedupKey
	if id == "" {
		id = fmt.Sprintf("integrity:%d", task.ID)
	}
	taskType := task.Command
	if taskType == "" {
		taskType = "integrity_verify"
	}
	q.probeSink.PublishProbeEvent(ProbeStreamEvent{
		ID:           id,
		TaskType:     taskType,
		Source:       task.Source,
		Status:       status,
		CredentialID: task.CredentialID,
		ProviderID:   task.ProviderID,
		RawModel:     task.RawModel,
		Attempt:      task.Attempt,
		Reason:       task.Source,
		Scheduled:    task.Source != "request_failure" && task.Source != "no_candidates",
		TimestampMs:  time.Now().UnixMilli(),
	})
}

// publishRearmTransition emits the "failed this attempt, re-armed for the
// next backoff hop" transition (OBS-BE5, 25 号 §6.1 智能回退). There is no
// legacy equivalent — the old stream had no signal for the next retry time —
// so this only fires when the enriched sink is wired. Published after the
// Complete UPDATE succeeds (and only when the lease was still ours), so the
// dashboard never shows a backoff tile for a rolled-back completion.
func (q *ProbeQueue) publishRearmTransition(task ProbeQueueTask, nextRunAt *time.Time) {
	if q == nil || q.detailSink == nil {
		return
	}
	q.detailSink.PublishProbeTransition(ProbeTaskTransition{
		ID:            probeQueueLifecycleID(task),
		TaskType:      probeQueueTaskType(task.Command),
		Source:        task.Source,
		Status:        "pending",
		CredentialID:  task.CredentialID,
		ProviderID:    task.ProviderID,
		RawModel:      task.RawModel,
		Attempt:       task.Attempt,
		Origin:        probeQueueOriginFromSource(task.Source),
		Reason:        task.Source,
		NextRetryAtMs: probeQueueNextRetryMs(nextRunAt),
		TimestampMs:   time.Now().UnixMilli(),
	})
}

// probeQueueLifecycleID mirrors the legacy SSE id convention (dedup_key, or
// integrity:<id> fallback when the key is missing).
func probeQueueLifecycleID(task ProbeQueueTask) string {
	if task.DedupKey != "" {
		return task.DedupKey
	}
	return fmt.Sprintf("integrity:%d", task.ID)
}

// probeQueueTaskType defaults the SSE task_type badge.
func probeQueueTaskType(command string) string {
	if command == "" {
		return "integrity_verify"
	}
	return command
}

// probeQueueNextRetryMs converts a backoff next-run time to unix millis,
// returning 0 (unset) for nil/zero values.
func probeQueueNextRetryMs(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UnixMilli()
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
	// 2026-08-11: mirror the "task enqueued" transition so the 自检 tab shows
	// the pending integrity probe. Only fires on a real insert (dedup hits
	// return false above and skip this).
	task.ID = id
	q.publishProbeTask(task, "pending")
	return id, true, nil
}

// Cancel marks all active (ready/running) tasks for a dedup key as cancelled.
// This is the "remove self-check task" half of the public self-check API
// (需求 6, bullet 1: 让外部需要自检的操作不用关心细节，直接增加或删除自检任务).
// Cancelled tasks are skipped by Claim (which only selects status='ready') and
// are terminal-sticky: RequeueExpiredLeases and completeFailure never revive
// them. Returns the number of tasks cancelled and whether the caller held a
// running lease that could not be revoked (in which case the worker will still
// finish the in-flight probe — cancel is best-effort for running tasks).
func (q *ProbeQueue) Cancel(ctx context.Context, dedupKey string) (int64, error) {
	if q == nil || q.db == nil {
		return 0, fmt.Errorf("cancel probe failed: database is unavailable (dedup_key=%s)", dedupKey)
	}
	if dedupKey == "" {
		return 0, fmt.Errorf("cancel probe failed: empty dedup_key")
	}
	tag, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET status='cancelled', lease_until=NULL, lease_token=NULL,
		    finished_at=COALESCE(finished_at, now()), updated_at=now()
		WHERE dedup_key=$1 AND status IN ('ready','running')`, dedupKey)
	if err != nil {
		return 0, fmt.Errorf("cancel probe failed: %w (dedup_key=%s)", err, dedupKey)
	}
	if n := tag.RowsAffected(); n > 0 && q.probeSink != nil {
		q.probeSink.PublishProbeEvent(ProbeStreamEvent{
			ID:          dedupKey,
			TaskType:    "integrity_verify",
			Source:      "admin",
			Status:      "fail", // SSE has no "cancelled" tile; render as terminal-dismissed
			Reason:      "cancelled",
			TimestampMs: time.Now().UnixMilli(),
		})
	}
	return tag.RowsAffected(), nil
}

// CancelNodeProbe is a convenience wrapper that derives the canonical node-probe
// dedup key via buildNodeProbeTaskID ("node_probe:<credID>:<model>", sanitized)
// so callers cancelling an error-triggered probe do not need to know the key
// shape, and so it always matches the key used at Enqueue time.
func (q *ProbeQueue) CancelNodeProbe(ctx context.Context, credentialID int64, model string) (int64, error) {
	return q.Cancel(ctx, buildNodeProbeTaskID(int(credentialID), model))
}

// BuildProbeDedupKey derives the canonical dedup key for a probe task so
// external callers (the public POST/DELETE /api/admin/probe/tasks API, 需求 6
// bullet 1) do not need to know the per-command key shape. It must match the
// key the corresponding producer (NodeProbeWorker.submitViaQueue,
// IntegrityProbePlanner) uses so enqueue/cancel collapse on the same row.
func BuildProbeDedupKey(command string, credentialID int64, model string) string {
	switch command {
	case "node_probe", "":
		return buildNodeProbeTaskID(int(credentialID), model)
	case "integrity_verify":
		// matches IntegrityProbePlanner: "integrity:<tenant>:<cred>:<model>";
		// tenant is resolved at enqueue, so the public API uses the tenant-less form.
		return fmt.Sprintf("integrity:%d:%s", credentialID, model)
	default:
		return fmt.Sprintf("%s:%d:%s", command, credentialID, model)
	}
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
		// 2026-08-18 Agent B: default lease bumped 30s → ProbeQueueLeaseDefault
		// (5m) so a direct+gateway+side-effects run that takes >30s cannot be
		// reclaimed by RequeueExpiredLeases and double-settled. The lease
		// heartbeat in ProbeService.Run keeps the window refreshed every
		// ProbeQueueHeartbeatInterval while side effects run.
		lease = ProbeQueueLeaseDefault
	}
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim probes failed: begin transaction: %w (limit=%d)", err, limit)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT id FROM credential_probe_queue
			WHERE status='ready' AND next_run_at <= now() AND expires_at > now() AND attempt < max_attempts
			ORDER BY priority DESC, next_run_at, id
			FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE credential_probe_queue q
		SET status='running', attempt=q.attempt+1,
			lease_token=gen_random_uuid(), lease_until=now()+$2,
			started_at=COALESCE(q.started_at, now()), updated_at=now()
		FROM picked WHERE q.id=picked.id
		RETURNING q.id, q.credential_id, COALESCE(q.provider_id,0), q.tenant_id,
			COALESCE(q.canonical_model,''), q.raw_model, COALESCE(q.outbound_model,''),
			q.probe_command, q.probe_mode, q.priority, q.attempt, q.max_attempts,
			q.next_run_at, q.lease_until, q.lease_token::text, q.source, COALESCE(q.source_event_id,''),
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
			&task.Attempt, &task.MaxAttempts, &task.NextRunAt, &task.LeaseUntil, &task.LeaseToken,
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
	// 2026-08-11: mirror the "task claimed/running" transition for each leased
	// task. Fires after the commit so we never publish a started event for a
	// task whose lease was rolled back.
	for _, task := range tasks {
		q.publishProbeTask(task, "in-flight")
	}
	return tasks, nil
}

// completeProbeTaskSQL is extracted for guard tests. The expires_at CASE
// arm is the 2026-08-18 fix: failure re-arms must refresh the task TTL or
// the backoff ladder (up to 6h) always outlives the 5-minute expires_at and
// the task starves to death mid-ladder.
func completeProbeTaskSQL() string {
	return `
		UPDATE credential_probe_queue
		SET status=$2, reason_code=$3, reason_detail=$4, result_http_status=$5,
			result_latency_ms=$6, result_body_preview=$7, next_run_at=COALESCE($8,next_run_at),
			lease_until=$9, finished_at=$10, updated_at=now(),
			expires_at=CASE WHEN $2='ready' THEN now() + $12::interval ELSE expires_at END
		WHERE id=$1 AND status='running' AND lease_token=$11::uuid`
}

func (q *ProbeQueue) Complete(ctx context.Context, task ProbeQueueTask, result ProbeQueueResult) error {
	id := task.ID
	if q == nil || q.db == nil {
		return fmt.Errorf("complete probe failed: database is unavailable (queue_id=%d)", id)
	}
	terminal := result.Status == ProbeQueueSuccess || result.Status == ProbeQueueFailed || result.Status == ProbeQueueExpired
	if terminal && result.FinishedAt == nil {
		now := time.Now()
		result.FinishedAt = &now
	}
	tag, err := q.db.Exec(ctx, completeProbeTaskSQL(),
		id, result.Status, nilString(result.ReasonCode), nilString(result.ReasonDetail),
		result.HTTPStatus, result.LatencyMs, nilString(result.BodyPreview), result.NextRunAt,
		result.LeaseUntil, result.FinishedAt, task.LeaseToken,
		fmt.Sprintf("%d seconds", int(ProbeQueueTTL.Seconds())))
	if err != nil {
		return fmt.Errorf("complete probe failed: %w (queue_id=%d)", err, id)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w (queue_id=%d)", ErrProbeLeaseLost, id)
	}
	// OBS-BE5 (25 号 §6.1 智能回退): a failure re-arm (status back to 'ready'
	// with a future next_run_at) means "this attempt failed; the next backoff
	// hop is scheduled for next_run_at". Mirror it to the SSE stream so the
	// 自检 tab can render the next-hop countdown on the failed tile. This is
	// the ONLY new signal — enqueue/claim transitions keep their existing
	// events; scheduling semantics (first hop only, re-enqueue on expiry) are
	// untouched.
	if result.Status == ProbeQueueReady && result.NextRunAt != nil {
		q.publishRearmTransition(task, result.NextRunAt)
	}
	return nil
}

func (q *ProbeQueue) ExtendLease(ctx context.Context, task ProbeQueueTask, lease time.Duration) error {
	if q == nil || q.db == nil {
		return fmt.Errorf("extend probe lease failed: database is unavailable (queue_id=%d)", task.ID)
	}
	if lease <= 0 {
		lease = ProbeQueueLeaseDefault
	}
	tag, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET lease_until=now()+$3, updated_at=now()
		WHERE id=$1 AND status='running' AND lease_token=$2::uuid AND lease_until >= now() AND expires_at > now()`,
		task.ID, task.LeaseToken, lease)
	if err != nil {
		return fmt.Errorf("extend probe lease failed: %w (queue_id=%d)", err, task.ID)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w (queue_id=%d)", ErrProbeLeaseLost, task.ID)
	}
	return nil
}

func (q *ProbeQueue) OwnsLease(ctx context.Context, task ProbeQueueTask) (bool, error) {
	if q == nil || q.db == nil {
		return false, fmt.Errorf("check probe lease failed: database is unavailable (queue_id=%d)", task.ID)
	}
	var owned bool
	err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM credential_probe_queue
			WHERE id=$1 AND status='running' AND lease_token=$2::uuid
			  AND lease_until >= now() AND expires_at > now()
		)`, task.ID, task.LeaseToken).Scan(&owned)
	return owned, err
}
func (q *ProbeQueue) RequeueExpiredLeases(ctx context.Context) (int64, error) {
	if q == nil || q.db == nil {
		return 0, fmt.Errorf("requeue expired probes failed: database is unavailable (queue=credential_probe_queue)")
	}
	result, err := q.db.Exec(ctx, `
		UPDATE credential_probe_queue
		SET status=CASE
				WHEN expires_at <= now() THEN 'expired'
				WHEN attempt >= max_attempts THEN 'failed'
				ELSE 'ready'
			END,
			lease_until=NULL, lease_token=NULL,
			finished_at=CASE WHEN expires_at <= now() OR attempt >= max_attempts THEN now() ELSE finished_at END,
			updated_at=now()
		WHERE status='running' AND lease_until < now()`)
	if err != nil {
		return 0, fmt.Errorf("requeue expired probes failed: %w (queue=credential_probe_queue)", err)
	}
	return result.RowsAffected(), nil
}

// ReviveExpiredReady rescues zombie 'ready' rows whose expires_at already
// elapsed (2026-08-18 glm-5.2 incident: Claim requires expires_at > now(), so
// such rows can never be claimed, yet their dedup_key also blocks a fresh
// Enqueue insert unless the enqueue pre-step notices the expiry — rows left
// behind by a crashed worker or a restart sat forever as "ready but dead",
// and the queue looked alive on dashboards while executing nothing). Rows
// still under the attempt cap get a fresh TTL; rows past the cap or without
// remaining attempts are marked expired so they stop counting as pending.
func (q *ProbeQueue) ReviveExpiredReady(ctx context.Context) (int64, error) {
	if q == nil || q.db == nil {
		return 0, fmt.Errorf("revive expired ready probes failed: database is unavailable (queue=credential_probe_queue)")
	}
	result, err := q.db.Exec(ctx, reviveExpiredReadySQL(),
		fmt.Sprintf("%d seconds", int(ProbeQueueTTL.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("revive expired ready probes failed: %w (queue=credential_probe_queue)", err)
	}
	return result.RowsAffected(), nil
}

// reviveExpiredReadySQL is extracted for guard tests. Required invariants:
// only status='ready' rows with an elapsed expires_at are touched; rows
// still under the attempt cap get a TTL that covers their whole next backoff
// hop (GREATEST(next_run_at, now()) + TTL — otherwise a 30s sweep rewrites a
// deep-backoff row every few minutes for hours); rows at/over the cap are
// marked expired so dashboards stop counting them as pending.
func reviveExpiredReadySQL() string {
	return `
		UPDATE credential_probe_queue
		SET expires_at=CASE
				WHEN attempt < max_attempts THEN GREATEST(next_run_at, now()) + $1::interval
				ELSE expires_at
			END,
			status=CASE
				WHEN attempt < max_attempts THEN status
				ELSE 'expired'
			END,
			finished_at=CASE WHEN attempt < max_attempts THEN finished_at ELSE now() END,
			updated_at=now()
		WHERE status='ready' AND expires_at <= now()`
}

func nilString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nilIfZero converts a value time.Time into nil when zero — lets the enqueue
// path share probeQueueNextRetryMs's *time.Time signature.
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
