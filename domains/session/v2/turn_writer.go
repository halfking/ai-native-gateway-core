// Package v2 implements Sessions V2 storage architecture
//
// This package provides parallel session storage alongside the existing
// request_logs system. The V2 architecture splits data into:
//   - sessions: session snapshots (one per session)
//   - session_turns: turn metadata (no bodies)
//   - session_bodies: incremental message deltas (columnar storage)
//   - session_turn_logs: processing stage logs (24h TTL)
//
// V2 can be enabled via Feature Flags without affecting V1 (request_logs).
package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TurnWriter writes turn metadata to public.session_turns
//
// It ensures turn_no is monotonically increasing within each session
// using PostgreSQL advisory locks to prevent concurrent conflicts.
type turnDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type TurnWriter struct {
	db turnDB
}

// sessionAdvisoryLockSQL 也把本事务的 max_parallel_workers_per_gather 压到 0
// （set_config 第三参 true = SET LOCAL 语义，commit/rollback 自动还原）。
// 2026-09-24 252 SQL 日志审计轮实测：MAX(turn_no) 走
// session_turns_with_current_month（hot 反连接臂 + 全分区 Append），planner
// 每次 Gather 孵化 2 个并行 worker —— EXPLAIN ANALYZE 并行 242-284ms vs
// 关并行 90ms（2.7×），且每次写轮次的 DSM 段分配/释放在 /dev/shm=64MB 的
// pg-252-pg17 上是 "could not map dynamic shared memory segment" ×338 +
// parallel worker FATAL ×654/45min 的主源。按 (tenant, session) 取锁后的
// 点读点写从不受益于并行，整个持锁事务统一退出并行。
const sessionAdvisoryLockSQL = `
	SELECT set_config('max_parallel_workers_per_gather', '0', true),
	       pg_advisory_xact_lock(
		       public.session_turns_advisory_lock_key($1, $2)
	       )
`

// NewTurnWriter creates a new TurnWriter instance
func NewTurnWriter(db *pgxpool.Pool) *TurnWriter {
	return newTurnWriter(db)
}

func newTurnWriter(db turnDB) *TurnWriter {
	return &TurnWriter{db: db}
}

// TurnRecord represents a single turn's metadata
type TurnRecord struct {
	SessionID       string
	TurnNo          int // Turn number within session (populated on read)
	TenantID        string
	RequestID       string
	Ts              time.Time
	ProjectID       string
	Namespace       string
	ParentRequestID string
	TaskType        string

	// Submit mode detection
	SubmitMode string // full | delta | snapshot | inferred_compressed | attachment_only

	// Compression metadata
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

	// Governance verdicts (L2 cache mirror)
	InjectionVerdict string // pass | warn | block | skip
	OutputVerdict    string // pass | warn | block | skip

	// Routing & model
	Model        string
	Provider     string
	CredentialID string

	// Usage & cost
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	// Performance
	LatencyMs  int
	StatusCode int
	Success    bool
	ErrorKind  string

	// Data quality
	SourceKind string // live | backfill
	Quality    string // verified | inferred | partial | rejected

	// Attachment metadata (added in migration 431)
	AttachmentCount      int      // Number of attachments in this turn
	AttachmentTotalBytes int64    // Total bytes of all attachments
	MultimodalTypes      []string // Types present: ["image", "audio", "video", "document"]

	// Turn-level title / summary (migration 456). The admin turns-list UI
	// renders these as one-line previews when present. Populated by
	// SessionWriterV2.Write from the first user / first assistant message so
	// the list is non-empty without waiting for an async LLM summarizer.
	Title   string
	Summary string

	// DigestJSON is the versioned, administrator-safe digest envelope persisted
	// with the turn. It is nil for rows that cannot produce a useful digest.
	DigestJSON []byte

	// V3.1 dispatch 9-stage (10 timestamps) queue timestamps (migration 513).
	// Mirrors the same columns on public.request_logs_hot so the session_turns
	// table can answer timeline queries without joining request_logs. Populated
	// by SessionWriterV2.Write from ProcessedRequest.T0ArrivedAt..T9ResponseEndAt,
	// which the telemetry mirror carries from RequestLogEntry. nil-safe —
	// old entries (pre-513) keep null timestamps.
	T0ArrivedAt       *time.Time
	T1TotalEnqueuedAt *time.Time
	T2TotalDequeuedAt *time.Time
	T3ModelEnqueuedAt *time.Time
	T4ModelDequeuedAt *time.Time
	T5CredEnqueuedAt  *time.Time
	T6CredDequeuedAt  *time.Time
	T7ForwardStartAt  *time.Time
	T8ResponseStartAt *time.Time
	T9ResponseEndAt   *time.Time

	// ── 存储优化方案 v2 S1a（migration 707）：request_logs 独有数据补采列。
	// 零值即 NULL；镜像 ProcessedRequest 同名字段（SessionWriterV2.Write
	// 负责搬运）。分组语义见 ProcessedRequest 注释与 storage-optimization-
	// plan.md §3 D1。
	//
	// 正文组：RequestDeltaJSON/ResponseDeltaJSON 由 writer 在
	// storage.session_turns_bodies_enabled 开启时填入（nil 即 NULL，旧行为）。
	RequestDeltaJSON   []byte
	ResponseDeltaJSON  []byte
	IsFinalSuccess     bool

	// 计费组
	APIKeyID      string
	ApplicationID string
	EndUserID     string
	CustomerID    int64
	CreditsCharged int64
	CostDisplay   float64
	CostCurrency  string
	WorkType      string
	TokenBand     string
	UsageSource   string

	// 路由组
	IsAutoRequest   bool
	AutoDecision    string
	AutoConfidence  float64
	TaskTypeChosen  string
	RoutingAttempts []byte
	RoutingSummary  string
	CanonicalID     int64
	CanonicalModel  string
	RawModelName    string

	// 诊断组
	TraceEvents         []byte
	FailureStage        string
	FailureDetailCode   string
	UpstreamStatusCode  int
	UpstreamFinishReason string
	StreamFirstChunkMs  int
	StreamChunkCount    int
	StreamInterrupted   bool
	StreamDoneSent      bool
	ClientRequestID     string
	ClientEndpoint      string
	ClientTimeout       bool
	EgressProtocol      string

	// 检索/完整性组
	SearchText        string
	RequestPreview    string
	ResponsePreview   string
	TransformSummary  string
	IdentityHash      string
	RequestChecksum   string
	ResponseChecksum  string
	SystemFingerprint string
	OriginStage       string
	OriginActor       string
	ClientIP          string
	ClientForwardedFor string
	AgentName         string
	AgentType         string
	VirtualClientID   string
}

// AppendTurn appends a new turn to the session, returning the assigned turn_no
//
// This is the backwards-compatible wrapper that owns its own transaction. It
// begins a tx, delegates to AppendTurnInTx, and commits on success.
//
// Prefer AppendTurnInTx when you need turn + bodies to commit atomically
// (spec §6.2). This method is kept so existing callers (and their tests) are
// unaffected.
func (w *TurnWriter) AppendTurn(ctx context.Context, rec TurnRecord) (turnNo int, err error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	turnNo, err = w.AppendTurnInTx(ctx, tx, rec)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return turnNo, nil
}

// LockSessionInTx serializes all turn state reads and writes for one tenant/session.
// Callers that derive a delta from the latest persisted body must acquire this
// lock before the read so the derivation and appended turn share one snapshot.
func (w *TurnWriter) LockSessionInTx(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) error {
	if _, err := tx.Exec(ctx, sessionAdvisoryLockSQL, tenantID, sessionID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	return nil
}

// AppendTurnInTx appends a new turn within a caller-managed transaction.
//
// It does NOT begin or commit; the caller controls the tx lifecycle so the
// turn INSERT can be committed atomically with the bodies INSERT (spec §6.2 —
// "turn 与 bodies 必须同事务，失败时整体可重试，无孤儿 turn"). The advisory
// lock is still acquired inside this tx (pg_advisory_xact_lock releases on
// commit/rollback), so concurrency semantics are identical to AppendTurn.
//
// Process:
//  1. Acquire advisory lock based on (tenant_id, session_id) hash
//  2. Query MAX(turn_no) for this session
//  3. Insert new turn with turn_no = MAX + 1 (ON CONFLICT DO NOTHING)
//  4. On conflict (RowsAffected == 0), re-read the real turn_no for the
//     idempotency key (request_id, partition_date) so retries / concurrent
//     inserts see a stable number.
//
// 2026-07-28 Step 3 Round 3: the previous pre-INSERT probe (SELECT turn_no
// WHERE request_id=… before the INSERT) was removed. ON CONFLICT DO NOTHING
// + RowsAffected==0 post-read is sufficient and avoids a redundant round
// trip on the hot path.
func (w *TurnWriter) AppendTurnInTx(ctx context.Context, tx pgx.Tx, rec TurnRecord) (turnNo int, err error) {
	if err := w.LockSessionInTx(ctx, tx, rec.TenantID, rec.SessionID); err != nil {
		return 0, err
	}
	return w.appendTurnInLockedTx(ctx, tx, rec)
}

// appendTurnInLockedTx appends a turn after the caller has acquired the
// tenant/session advisory lock in the same transaction.
func (w *TurnWriter) appendTurnInLockedTx(ctx context.Context, tx pgx.Tx, rec TurnRecord) (turnNo int, err error) {
	if _, err := tx.Exec(ctx, sessionAdvisoryLockSQL, rec.TenantID, "request:"+rec.RequestID); err != nil {
		return 0, fmt.Errorf("acquire request advisory lock: %w", err)
	}

	if rec.SubmitMode == "" {
		rec.SubmitMode = "full"
	}
	if rec.InjectionVerdict == "" {
		rec.InjectionVerdict = "skip"
	}
	if rec.OutputVerdict == "" {
		rec.OutputVerdict = "skip"
	}
	if rec.SourceKind == "" {
		rec.SourceKind = "live"
	}
	if rec.Quality == "" {
		rec.Quality = "verified"
	}

	// 2. Get next turn_no
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(turn_no), 0) + 1
		FROM public.session_turns_with_current_month
		WHERE tenant_id = $1 AND session_id = $2
	`, rec.TenantID, rec.SessionID).Scan(&turnNo)
	if err != nil {
		return 0, fmt.Errorf("get next turn_no: %w", err)
	}

	partitionDate := calendarDate(rec.Ts)

	// 3. Serialize compression_meta to JSONB
	compressionMetaJSON, err := json.Marshal(rec.CompressionMeta)
	if err != nil {
		return 0, fmt.Errorf("marshal compression_meta: %w", err)
	}
	// 2026-07-22: 转为 string 以使用 ::text::jsonb cast，避免 22P02 错误
	// 与 analysis/bus/publisher.go 和 telemetry/client.go 保持一致
	compressionMetaStr := string(compressionMetaJSON)

	// 4. Insert new live turns into the independent hot table. The anti-join
	// keeps retries idempotent when an earlier copy has already been promoted.
	// 707: columns $48..$97 are the storage-plan-v2 S1a backfill groups
	// (bodies / final-success / billing / routing / diagnostics /
	// search+integrity). All nullable; empty Go values persist as SQL NULL via
	// the nilIf* helpers so fill-rate acceptance SQL (plan §8-B) measures real
	// collection, not zero-value noise.
	result, err := tx.Exec(ctx, `
			INSERT INTO public.session_turns_hot (
				session_id, turn_no, tenant_id, request_id, ts,
				project_id, namespace, parent_request_id, task_type,
				submit_mode,
				compression_applied, compression_strategy, compression_meta, compression_tokens_saved,
				injection_verdict, output_verdict,
				model, provider, credential_id,
				prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
				latency_ms, status_code, success, error_kind,
				source_kind, quality,
				attachment_count, attachment_total_bytes, multimodal_types,
				title, summary, digest,
				t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at,
				t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at,
				t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at,
				t9_response_end_at,
				partition_date,
				request_delta, response_delta, is_final_success,
				api_key_id, application_id, end_user_id, customer_id,
				credits_charged, cost_display, cost_currency, work_type, token_band, usage_source,
				is_auto_request, auto_decision, auto_confidence, task_type_chosen,
				routing_attempts, routing_summary, canonical_id, canonical_model, raw_model_name,
				trace_events, failure_stage, failure_detail_code,
				upstream_status_code, upstream_finish_reason,
				stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent,
				client_request_id, client_endpoint, client_timeout, egress_protocol,
				search_text, request_preview, response_preview, transform_summary,
				identity_hash, request_checksum, response_checksum, system_fingerprint,
				origin_stage, origin_actor, client_ip, client_forwarded_for,
				agent_name, agent_type, virtual_client_id
			) SELECT
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10,
				$11, $12, $13::text::jsonb, $14,
				$15, $16,
				$17, $18, $19,
				$20, $21, $22, $23, $24,
				$25, $26, $27, $28,
				$29, $30,
				$31, $32, $33,
				$34, $35, $36::text::jsonb,
				$37, $38, $39, $40, $41, $42, $43, $44, $45, $46,
				$47,
				$48::text::jsonb, $49::text::jsonb, $50,
				$51, $52, $53, $54,
				$55, $56, $57, $58, $59, $60,
				$61, $62, $63, $64,
				$65::text::jsonb, $66, $67, $68, $69,
				$70::text::jsonb, $71, $72,
				$73, $74,
				$75, $76, $77, $78,
				$79, $80, $81, $82,
				$83, $84, $85, $86,
				$87, $88, $89, $90,
				$91, $92, $93, $94,
				$95, $96, $97
			WHERE NOT EXISTS (
				SELECT 1
				FROM public.session_turns_with_current_month
				WHERE tenant_id = $3 AND request_id = $4
			)
			ON CONFLICT (tenant_id, request_id, partition_date) DO NOTHING
		`,
		rec.SessionID, turnNo, rec.TenantID, rec.RequestID, rec.Ts,
		rec.ProjectID, rec.Namespace, rec.ParentRequestID, rec.TaskType,
		rec.SubmitMode,
		rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr, rec.TokensSaved,
		rec.InjectionVerdict, rec.OutputVerdict,
		rec.Model, rec.Provider, rec.CredentialID,
		rec.PromptTokens, rec.CompletionTokens, rec.CacheReadTokens, rec.CacheWriteTokens, rec.CostUSD,
		rec.LatencyMs, rec.StatusCode, rec.Success, rec.ErrorKind,
		rec.SourceKind, rec.Quality,
		rec.AttachmentCount, rec.AttachmentTotalBytes, rec.MultimodalTypes,
		rec.Title, rec.Summary, string(rec.DigestJSON),
		rec.T0ArrivedAt, rec.T1TotalEnqueuedAt, rec.T2TotalDequeuedAt,
		rec.T3ModelEnqueuedAt, rec.T4ModelDequeuedAt, rec.T5CredEnqueuedAt,
		rec.T6CredDequeuedAt, rec.T7ForwardStartAt, rec.T8ResponseStartAt,
		rec.T9ResponseEndAt,
		partitionDate,
		// 707 backfill groups
		nilIfEmptyJSON(rec.RequestDeltaJSON), nilIfEmptyJSON(rec.ResponseDeltaJSON), boolOrNil(rec.IsFinalSuccess),
		nilIfEmpty(rec.APIKeyID), nilIfEmpty(rec.ApplicationID), nilIfEmpty(rec.EndUserID), nilIfZeroI64(rec.CustomerID),
		nilIfZeroI64(rec.CreditsCharged), nilIfZeroF64(rec.CostDisplay), nilIfEmpty(rec.CostCurrency), nilIfEmpty(rec.WorkType), nilIfEmpty(rec.TokenBand), nilIfEmpty(rec.UsageSource),
		rec.IsAutoRequest, nilIfEmpty(rec.AutoDecision), nilIfZeroF64(rec.AutoConfidence), nilIfEmpty(rec.TaskTypeChosen),
		nilIfEmptyJSON(rec.RoutingAttempts), nilIfEmpty(rec.RoutingSummary), nilIfZeroI64(rec.CanonicalID), nilIfEmpty(rec.CanonicalModel), nilIfEmpty(rec.RawModelName),
		nilIfEmptyJSON(rec.TraceEvents), nilIfEmpty(rec.FailureStage), nilIfEmpty(rec.FailureDetailCode),
		nilIfZeroInt(rec.UpstreamStatusCode), nilIfEmpty(rec.UpstreamFinishReason),
		nilIfZeroInt(rec.StreamFirstChunkMs), nilIfZeroInt(rec.StreamChunkCount), rec.StreamInterrupted, rec.StreamDoneSent,
		nilIfEmpty(rec.ClientRequestID), nilIfEmpty(rec.ClientEndpoint), rec.ClientTimeout, nilIfEmpty(rec.EgressProtocol),
		nilIfEmpty(rec.SearchText), nilIfEmpty(rec.RequestPreview), nilIfEmpty(rec.ResponsePreview), nilIfEmpty(rec.TransformSummary),
		nilIfEmpty(rec.IdentityHash), nilIfEmpty(rec.RequestChecksum), nilIfEmpty(rec.ResponseChecksum), nilIfEmpty(rec.SystemFingerprint),
		nilIfEmpty(rec.OriginStage), nilIfEmpty(rec.OriginActor), nilIfEmpty(rec.ClientIP), nilIfEmpty(rec.ClientForwardedFor),
		nilIfEmpty(rec.AgentName), nilIfEmpty(rec.AgentType), nilIfEmpty(rec.VirtualClientID),
	)

	if err != nil {
		return 0, fmt.Errorf("insert turn: %w", err)
	}

	// Concurrent insert may have raced past us; resolve to the real turn_no
	// for the idempotency key so retries see the existing row's number.
	if result.RowsAffected() == 0 {
		var existingSessionID string
		var existingPartitionDate time.Time
		err = tx.QueryRow(ctx, `
				SELECT session_id, turn_no, partition_date
				FROM public.session_turns_with_current_month
				WHERE tenant_id = $1 AND request_id = $2
				ORDER BY partition_date ASC
				LIMIT 1
			`, rec.TenantID, rec.RequestID).Scan(&existingSessionID, &turnNo, &existingPartitionDate)

		if err != nil {
			return 0, fmt.Errorf("read existing turn_no: %w", err)
		}
		if existingSessionID != rec.SessionID {
			return 0, fmt.Errorf("request %s already belongs to session %s", rec.RequestID, existingSessionID)
		}
		if !existingPartitionDate.Equal(partitionDate) {
			return 0, fmt.Errorf("request %s already belongs to partition date %s", rec.RequestID, existingPartitionDate.Format("2006-01-02"))
		}

		// 2026-08-05 (v2 mirror bug): the mirror fires this write TWICE per
		// request via telemetry onPersisted — once for the INSERT-persist
		// (before the session compressor has run, so compression_strategy is
		// empty and submit_mode is only LCS-inferred) and once for the
		// UPDATE-persist (after compression, carrying the real
		// compression_strategy and the authoritative X-Gw-Submit-Mode verdict).
		// The initial insert wins the ON CONFLICT DO NOTHING above, so the
		// second fire's fields were silently dropped: session_turns.
		// compression_strategy stayed empty for every row and submit_mode was
		// pinned to the first-fire value. Backfill those late-arriving fields
		// on the conflict path. COALESCE(NULLIF(...)) guarantees a later empty
		// fire can never blank a value an earlier fire populated (monotonic
		// enrichment), so this stays idempotent under retries.
		for _, table := range []string{"public.session_turns_hot", "public.session_turns"} {
			_, err = tx.Exec(ctx, `
					UPDATE `+table+`
					   SET compression_applied      = $5 OR compression_applied,
					       compression_strategy     = COALESCE(NULLIF($6, ''), compression_strategy),
					       compression_meta         = CASE
						                              WHEN $7 <> '' AND $7 <> 'null'
						                              THEN $7::text::jsonb
						                              ELSE compression_meta
						                          END,
					       compression_tokens_saved = CASE
						                              WHEN $8 <> 0 THEN $8
						                              ELSE compression_tokens_saved
						                          END,
					       submit_mode              = CASE
						                              WHEN $9 <> '' AND $9 <> 'full'
						                              THEN $9
						                              ELSE submit_mode
						                          END,
					       title                    = COALESCE(NULLIF($10, ''), title),
					       summary                  = COALESCE(NULLIF($11, ''), summary),
					       digest                   = CASE WHEN $12 <> '' AND $12 <> 'null' THEN $12::text::jsonb ELSE digest END,
					       project_id               = COALESCE(NULLIF($13, ''), project_id),
					       namespace                = COALESCE(NULLIF($14, ''), namespace),
					       parent_request_id        = COALESCE(NULLIF($15, ''), parent_request_id),
					       task_type                = COALESCE(NULLIF($16, ''), task_type),
					       t0_arrived_at            = COALESCE($17, t0_arrived_at),
					       t1_total_enqueued_at     = COALESCE($18, t1_total_enqueued_at),
					       t2_total_dequeued_at     = COALESCE($19, t2_total_dequeued_at),
					       t3_model_enqueued_at     = COALESCE($20, t3_model_enqueued_at),
					       t4_model_dequeued_at     = COALESCE($21, t4_model_dequeued_at),
					       t5_cred_enqueued_at      = COALESCE($22, t5_cred_enqueued_at),
					       t6_cred_dequeued_at      = COALESCE($23, t6_cred_dequeued_at),
					       t7_forward_start_at      = COALESCE($24, t7_forward_start_at),
					       t8_response_start_at     = COALESCE($25, t8_response_start_at),
					       t9_response_end_at       = COALESCE($26, t9_response_end_at)
					 WHERE session_id = $1 AND tenant_id = $2
					   AND request_id = $3 AND partition_date = $4
				`,
				rec.SessionID, rec.TenantID, rec.RequestID, partitionDate,
				rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr,
				rec.TokensSaved, rec.SubmitMode, rec.Title, rec.Summary, string(rec.DigestJSON),
				rec.ProjectID, rec.Namespace, rec.ParentRequestID, rec.TaskType,
				rec.T0ArrivedAt, rec.T1TotalEnqueuedAt, rec.T2TotalDequeuedAt,
				rec.T3ModelEnqueuedAt, rec.T4ModelDequeuedAt, rec.T5CredEnqueuedAt,
				rec.T6CredDequeuedAt, rec.T7ForwardStartAt, rec.T8ResponseStartAt,
				rec.T9ResponseEndAt,
			)
			if err != nil {
				return 0, fmt.Errorf("enrich turn in %s: %w", table, err)
			}
		}

	}

	return turnNo, nil
}

// BeginTx begins a new transaction on the underlying pool.
//
// Exposed so SessionWriterV2 can begin one tx and feed it to both
// AppendTurnInTx and WriteBodiesInTx (spec §6.2 atomicity).
func (w *TurnWriter) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return w.db.Begin(ctx)
}

// ── 707 backfill helpers ───────────────────────────────────────────────────
//
// The 707 backfill columns must distinguish "not collected" (SQL NULL) from
// collected-but-zero/empty, otherwise the plan §8-B fill-rate acceptance SQL
// (count(*) FILTER (WHERE col IS NOT NULL)) counts zero-value noise as
// collected data. Go zero values therefore persist as NULL. Bool columns keep
// their value (false is a legitimate state, and the partial final-success
// index ignores both false and NULL).

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZeroI64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfZeroInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfZeroF64(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func boolOrNil(v bool) any {
	if !v {
		return nil
	}
	return true
}

// nilIfEmptyJSON passes JSON payloads as strings for ::text::jsonb casts
// (the same 22P02 avoidance as compression_meta), mapping empty payloads to
// SQL NULL rather than jsonb 'null'.
func nilIfEmptyJSON(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return string(b)
}

// GetTurn retrieves a tenant-scoped turn by request_id.
func (w *TurnWriter) GetTurn(ctx context.Context, tenantID, requestID string) (*TurnRecord, error) {
	var rec TurnRecord
	var compressionMetaJSON []byte

	query := `
		SELECT
			session_id, turn_no, tenant_id, request_id, ts,
			COALESCE(project_id, ''), COALESCE(namespace, ''),
			COALESCE(parent_request_id, ''), COALESCE(task_type, ''),
			submit_mode,
			compression_applied, compression_strategy, compression_meta,
			COALESCE(compression_tokens_saved, 0),
			COALESCE(injection_verdict, 'skip'),
			COALESCE(output_verdict, 'skip'),
			model, provider, credential_id,
			COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0),
			COALESCE(cost_usd, 0),
			COALESCE(latency_ms, 0), COALESCE(status_code, 0),
			COALESCE(success, false), error_kind,
			source_kind, quality,
			COALESCE(attachment_count, 0),
			COALESCE(attachment_total_bytes, 0),
			COALESCE(multimodal_types, '{}'),
			t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at,
			t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at,
			t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at,
			t9_response_end_at
			FROM public.session_turns_with_current_month
			WHERE tenant_id = $1 AND request_id = $2
			LIMIT 1
		`

	err := w.db.QueryRow(ctx, query, tenantID, requestID).Scan(
		&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
		&rec.ProjectID, &rec.Namespace, &rec.ParentRequestID, &rec.TaskType,
		&rec.SubmitMode,
		&rec.CompressionApplied, &rec.CompressionStrategy, &compressionMetaJSON, &rec.TokensSaved,
		&rec.InjectionVerdict, &rec.OutputVerdict,
		&rec.Model, &rec.Provider, &rec.CredentialID,
		&rec.PromptTokens, &rec.CompletionTokens,
		&rec.CacheReadTokens, &rec.CacheWriteTokens,
		&rec.CostUSD,
		&rec.LatencyMs, &rec.StatusCode,
		&rec.Success, &rec.ErrorKind,
		&rec.SourceKind, &rec.Quality,
		&rec.AttachmentCount, &rec.AttachmentTotalBytes, &rec.MultimodalTypes,
		&rec.T0ArrivedAt, &rec.T1TotalEnqueuedAt, &rec.T2TotalDequeuedAt,
		&rec.T3ModelEnqueuedAt, &rec.T4ModelDequeuedAt, &rec.T5CredEnqueuedAt,
		&rec.T6CredDequeuedAt, &rec.T7ForwardStartAt, &rec.T8ResponseStartAt,
		&rec.T9ResponseEndAt,
	)

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("turn not found for tenant %s: %s", tenantID, requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("query turn: %w", err)
	}

	// Parse compression_meta
	if len(compressionMetaJSON) > 0 {
		err = json.Unmarshal(compressionMetaJSON, &rec.CompressionMeta)
		if err != nil {
			return nil, fmt.Errorf("unmarshal compression_meta: %w", err)
		}
	}

	return &rec, nil
}

// ListTurns retrieves all turns for a session, ordered by turn_no
func (w *TurnWriter) ListTurns(ctx context.Context, tenantID, sessionID string, limit int) ([]TurnRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT
			session_id, turn_no, tenant_id, request_id, ts,
			COALESCE(project_id, ''), COALESCE(namespace, ''),
			COALESCE(parent_request_id, ''), COALESCE(task_type, ''),
			submit_mode,
			compression_applied, compression_strategy, compression_meta,
			COALESCE(compression_tokens_saved, 0),
			COALESCE(injection_verdict, 'skip'),
			COALESCE(output_verdict, 'skip'),
			model, provider, credential_id,
			COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0),
			COALESCE(cost_usd, 0),
			COALESCE(latency_ms, 0), COALESCE(status_code, 0),
			COALESCE(success, false), error_kind,
			source_kind, quality,
			COALESCE(attachment_count, 0),
			COALESCE(attachment_total_bytes, 0),
			COALESCE(multimodal_types, '{}'),
			t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at,
			t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at,
			t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at,
			t9_response_end_at
		FROM public.session_turns_with_current_month
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
		LIMIT $3
	`

	rows, err := w.db.Query(ctx, query, tenantID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer rows.Close()

	var turns []TurnRecord
	for rows.Next() {
		var rec TurnRecord
		var compressionMetaJSON []byte

		err := rows.Scan(
			&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
			&rec.ProjectID, &rec.Namespace, &rec.ParentRequestID, &rec.TaskType,
			&rec.SubmitMode,
			&rec.CompressionApplied, &rec.CompressionStrategy, &compressionMetaJSON, &rec.TokensSaved,
			&rec.InjectionVerdict, &rec.OutputVerdict,
			&rec.Model, &rec.Provider, &rec.CredentialID,
			&rec.PromptTokens, &rec.CompletionTokens,
			&rec.CacheReadTokens, &rec.CacheWriteTokens,
			&rec.CostUSD,
			&rec.LatencyMs, &rec.StatusCode,
			&rec.Success, &rec.ErrorKind,
			&rec.SourceKind, &rec.Quality,
			&rec.AttachmentCount, &rec.AttachmentTotalBytes, &rec.MultimodalTypes,
			&rec.T0ArrivedAt, &rec.T1TotalEnqueuedAt, &rec.T2TotalDequeuedAt,
			&rec.T3ModelEnqueuedAt, &rec.T4ModelDequeuedAt, &rec.T5CredEnqueuedAt,
			&rec.T6CredDequeuedAt, &rec.T7ForwardStartAt, &rec.T8ResponseStartAt,
			&rec.T9ResponseEndAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}

		// Parse compression_meta
		if len(compressionMetaJSON) > 0 {
			err = json.Unmarshal(compressionMetaJSON, &rec.CompressionMeta)
			if err != nil {
				return nil, fmt.Errorf("unmarshal compression_meta: %w", err)
			}
		}

		turns = append(turns, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}

	return turns, nil
}
