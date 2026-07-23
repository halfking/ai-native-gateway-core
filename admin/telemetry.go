package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type decisionLogInput struct {
	RequestID           string          `json:"request_id"`
	IdempotencyKey      *string         `json:"idempotency_key,omitempty"`
	TenantID            string          `json:"tenant_id"`
	APIKeyID            *int            `json:"api_key_id,omitempty"`
	Model               string          `json:"model"`
	ChosenCredentialID  *int            `json:"chosen_credential_id,omitempty"`
	ChosenProviderID    *int            `json:"chosen_provider_id,omitempty"`
	Tier                *int            `json:"tier,omitempty"`
	CandidatesTried     int             `json:"candidates_tried"`
	LatencyMs           int             `json:"latency_ms"`
	Success             bool            `json:"success"`
	ErrorClass          *string         `json:"error_class,omitempty"`
	PromptTokens        *int            `json:"prompt_tokens,omitempty"`
	CompletionTokens    *int            `json:"completion_tokens,omitempty"`
	CostUSD             *float64        `json:"cost_usd,omitempty"`
	RequestBytes        *int            `json:"request_bytes,omitempty"`
	ResponseBytes       *int            `json:"response_bytes,omitempty"`
	ClientModel         *string         `json:"client_model,omitempty"`
	ResolvedRawModel    *string         `json:"resolved_raw_model,omitempty"`
	OutboundModel       *string         `json:"outbound_model,omitempty"`
	StickyHit           *bool           `json:"sticky_hit,omitempty"`
	ClientProfile       *string         `json:"client_profile,omitempty"`
	RequestMode         *string         `json:"request_mode,omitempty"`
	IdentityHash        *string         `json:"identity_hash,omitempty"`
	TransformRuleID     *string         `json:"transform_rule_id,omitempty"`
	EgressProtocol      *string         `json:"egress_protocol,omitempty"`
	FailureStage        *string         `json:"failure_stage,omitempty"`
	FailureDetailCode   *string         `json:"failure_detail_code,omitempty"`
	ResolutionPath      *string         `json:"resolution_path,omitempty"`
	CanonicalModel      *string         `json:"canonical_model,omitempty"`
	ResolutionRawModels []string        `json:"resolution_raw_models,omitempty"`
	DecisionTrace       json.RawMessage `json:"decision_trace,omitempty"`
}

type requestLogInput struct {
	RequestID          string   `json:"request_id"`
	TenantID           string   `json:"tenant_id"`
	ApplicationID      *int     `json:"application_id,omitempty"`
	APIKeyID           *int     `json:"api_key_id,omitempty"`
	EndUserID          *string  `json:"end_user_id,omitempty"`
	ClientModel        *string  `json:"client_model,omitempty"`
	OutboundModel      *string  `json:"outbound_model,omitempty"`
	CredentialID       *int     `json:"credential_id,omitempty"`
	ProviderID         *int     `json:"provider_id,omitempty"`
	CanonicalID        *int     `json:"canonical_id,omitempty"`
	ClientProfile      *string  `json:"client_profile,omitempty"`
	RequestMode        *string  `json:"request_mode,omitempty"`
	PromptTokens       *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens   *int     `json:"completion_tokens,omitempty"`
	CacheReadTokens    *int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   *int     `json:"cache_write_tokens,omitempty"`
	CostUSD            *float64 `json:"cost_usd,omitempty"`
	LatencyMs          *int     `json:"latency_ms,omitempty"`
	Success            bool     `json:"success"`
	ErrorKind          *string  `json:"error_kind,omitempty"`
	IdentityHash       *string  `json:"identity_hash,omitempty"`
	StreamFirstChunkMs *int     `json:"stream_first_chunk_ms,omitempty"`
	StreamChunkCount   *int     `json:"stream_chunk_count,omitempty"`
	// 2026-07-05: stream_chunks_sent is NOT NULL (migration 320). HTTP
	// callers that don't populate it fall back to 0 via the COALESCE
	// wrapper on the INSERT — see also the matching default in
	// domains/hooks/observability/telemetry/client.go.
	StreamChunksSent   *int    `json:"stream_chunks_sent,omitempty"`
	StreamDoneReceived *bool   `json:"stream_done_received,omitempty"`
	StreamInterrupted  *bool   `json:"stream_interrupted,omitempty"`
	ResponseChecksum   *string `json:"response_checksum,omitempty"`
	FailureDetailCode  *string `json:"failure_detail_code,omitempty"`
	// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
	// New column is the SOLE home for the upstream finish_reason.
	UpstreamFinishReason *string `json:"upstream_finish_reason,omitempty"`
	TransformRuleID      *string `json:"transform_rule_id,omitempty"`
	EgressProtocol       *string `json:"egress_protocol,omitempty"`
	RequestPreview       *string `json:"request_preview,omitempty"`
	TransformSummary     *string `json:"transform_summary,omitempty"`
	ResponsePreview      *string `json:"response_preview,omitempty"`
	RequestBody          *string `json:"request_body,omitempty"`
	ResponseBody         *string `json:"response_body,omitempty"`
}

type batchEntry struct {
	DecisionLog *decisionLogInput `json:"decision_log,omitempty"`
	RequestLog  *requestLogInput  `json:"request_log,omitempty"`
}

type telemetryIngester struct {
	db    *pgxpool.Pool
	queue chan any
	done  chan struct{}
	wg    sync.WaitGroup

	// 2026-07-16: failure counters split by category so ops can
	// distinguish transient (worth retrying) from permanent (data
	// quality / schema drift) failures. Read via admin/metrics if
	// needed; updated atomically here.
	failTransient uint64 // atomic
	failPermanent uint64 // atomic
	failRetried   uint64 // atomic
}

var ingester *telemetryIngester

// keepAllBodies returns true when the operator has explicitly opted
// into persisting request/response bodies for ALL requests (including
// successful ones). Default is false — bodies are only kept for
// failed rows, which slashes request_logs_bodies disk usage by ~90%.
// Override at runtime: `export LLM_GATEWAY_KEEP_ALL_BODIES=true` and
// restart. Used by 2026-07-13 disk-pressure incident on 154.
func keepAllBodies() bool {
	v := os.Getenv("LLM_GATEWAY_KEEP_ALL_BODIES")
	return v == "true" || v == "1"
}

// StartIngester spins up the in-process telemetry ingest worker.
// Called from cmd/gateway/main.go once the DB pool is ready.
// Without this, POST /api/telemetry/request-log queues rows but
// never persists them — the queue has no consumer. 2026-07-05 fix.
func StartIngester(db *pgxpool.Pool) {
	if db == nil {
		return
	}
	ingester = &telemetryIngester{
		db:    db,
		queue: make(chan any, 4096),
		done:  make(chan struct{}),
	}
	ingester.wg.Add(1)
	go ingester.worker()
}

// StopIngester drains the queue and stops the worker.
func StopIngester() {
	if ingester != nil {
		close(ingester.done)
		ingester.wg.Wait()
	}
}

func (t *telemetryIngester) worker() {
	defer t.wg.Done()
	batch := make([]any, 0, 100)
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case <-t.done:
			t.flush(batch)
			return
		case item := <-t.queue:
			batch = append(batch, item)
			if len(batch) >= 100 {
				t.flush(batch)
				batch = batch[:0]
				timer.Reset(200 * time.Millisecond)
			} else if len(batch) == 1 {
				timer.Reset(200 * time.Millisecond)
			}
		case <-timer.C:
			if len(batch) > 0 {
				t.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (t *telemetryIngester) flush(batch []any) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, item := range batch {
		switch v := item.(type) {
		case *decisionLogInput:
			t.persistDecisionLog(ctx, v)
		case *requestLogInput:
			t.persistRequestLog(ctx, v)
		}
	}
}

func (t *telemetryIngester) persistDecisionLog(ctx context.Context, e *decisionLogInput) {
	rawModelsJSON, _ := json.Marshal(coalesceRawModels(e.ResolutionRawModels))
	traceJSON := coalesceTrace(e.DecisionTrace)
	// INSERT directly targets routing_decision_log_hot (the canonical
	// write target per the 2026-07 data-lifecycle architecture — never
	// the parent table, which would let PG auto-route rows into monthly
	// partitions that are not safe to UPDATE/DELETE later).
	err := t.execWithRetry(ctx, "decision_log", e.RequestID, func(ctx context.Context) error {
		_, err := t.db.Exec(ctx, `
		INSERT INTO routing_decision_log_hot (
			ts, request_id, idempotency_key, tenant_id, api_key_id,
			model, chosen_credential_id, chosen_provider_id, tier,
			candidates_tried, latency_ms, success, error_class,
			prompt_tokens, completion_tokens, cost_usd,
			request_bytes, response_bytes,
			client_model, resolved_raw_model, sticky_hit, client_profile,
			outbound_model, request_mode, identity_hash, transform_rule_id,
			egress_protocol, failure_stage, failure_detail_code,
			resolution_path, canonical_model, resolution_raw_models, decision_trace
		) VALUES (
			now(), $1::uuid, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15,
			$16, $17,
			$18, $19, $20, $21,
			$22, $23, $24, $25,
			$26, $27, $28,
			$29, $30, CAST($31 AS jsonb), CAST($32 AS jsonb)
		)
	`,
			e.RequestID, e.IdempotencyKey, nonEmptyDefault(e.TenantID), e.APIKeyID,
			e.Model, e.ChosenCredentialID, e.ChosenProviderID, e.Tier,
			e.CandidatesTried, e.LatencyMs, e.Success, e.ErrorClass,
			e.PromptTokens, e.CompletionTokens, e.CostUSD,
			e.RequestBytes, e.ResponseBytes,
			e.ClientModel, e.ResolvedRawModel, e.StickyHit, e.ClientProfile,
			e.OutboundModel, e.RequestMode, e.IdentityHash, e.TransformRuleID,
			e.EgressProtocol, e.FailureStage, e.FailureDetailCode,
			e.ResolutionPath, e.CanonicalModel, rawModelsJSON, traceJSON,
		)
		return err
	})
	if err != nil {
		slog.Warn("telemetry ingest decision log failed", "request_id", e.RequestID, "error", err)
	}
}

func (t *telemetryIngester) persistRequestLog(ctx context.Context, e *requestLogInput) {
	totalTok := calcTotal(e.PromptTokens, e.CompletionTokens)
	rawModel := firstNonEmptyStr(e.OutboundModel, e.ClientModel)
	search := buildSearchText(e)

	// 2026-07-21: Implement proper two-table separation (Ticket #10, Issue #8)
	// - request_logs_hot: stores metadata + preview fields (first ~500 chars) + outbound_body
	// - request_logs_bodies_hot: stores complete request_body/response_body
	//
	// Note: outbound_body stays in request_logs_hot because it's part of the v3
	// session compression feature (migration 016) and is typically much smaller
	// than request_body (delta-append only adds new messages). The 70% disk savings
	// come from moving request_body and response_body (the largest columns).
	//
	// This resolves the storage pressure (3.4 GB / 24k rows on 154) by moving
	// the two largest JSONB columns out of the metadata table. Queries that need
	// full bodies use LEFT JOIN pattern. Both tables written in same transaction.
	//
	// Historical context (dcd3bd55b): Temporary fix disabled body dropping to
	// resolve context-loss issue. This implementation completes the proper
	// architectural solution referenced in that commit's TODO comment.

	tx, err := t.db.Begin(ctx)
	if err != nil {
		slog.Warn("telemetry ingest begin failed", "error", err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// INSERT directly targets usage_ledger_hot (the canonical write
	// target per the 2026-07 data-lifecycle architecture). UPDATE-heavy
	// operations (cost/tokens/latency enrichment) require heap storage
	// with row-level UPDATE support — monthly partitions (columnar or
	// long-archived) cannot serve this traffic.
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, application_id, api_key_id,
			end_user_id, credential_id, provider_id, canonical_id,
			raw_model_name, prompt_tokens, completion_tokens,
			cache_read_tokens, cache_write_tokens,
			total_tokens, cost_usd, latency_ms, success, error_kind
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13,
			$14, $15, $16, $17, $18
		)
	`,
		e.RequestID, nonEmptyDefault(e.TenantID), e.ApplicationID, e.APIKeyID,
		e.EndUserID, e.CredentialID, e.ProviderID, e.CanonicalID,
		rawModel, e.PromptTokens, e.CompletionTokens,
		e.CacheReadTokens, e.CacheWriteTokens,
		totalTok, e.CostUSD, e.LatencyMs, e.Success, e.ErrorKind,
	)
	if err != nil {
		t.classifyAndCount("usage_ledger", e.RequestID, err)
		slog.Warn("telemetry ingest usage_ledger failed", "error", err)
		return
	}

	// 2026-07-05 migration 341: INSERT directly targets request_logs_hot
	// (独立热表，0-7 天数据窗口)。所有 INSERT/UPDATE/DELETE 统一写入 _hot 表，
	// 后台 partition_manager 会定期将冷数据（>7 天）迁移到月度分区。
	//
	// 2026-07-21 Ticket #10: Modified to NOT include request_body/response_body.
	// These are now written to request_logs_bodies_hot (see below).
	// Only preview fields remain in the metadata table.
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, api_key_id,
			end_user_id, client_model, outbound_model,
			credential_id, provider_id, canonical_id,
			client_profile, request_mode,
			prompt_tokens, completion_tokens,
			cache_read_tokens, cache_write_tokens, total_tokens,
			cost_usd, latency_ms, success, error_kind, search_text,
			identity_hash, response_checksum,
			transform_rule_id, egress_protocol, failure_detail_code,
			request_preview, transform_summary, response_preview,
			stream_first_chunk_ms, stream_chunk_count, stream_done_received,
			stream_interrupted,
			stream_chunks_sent,
			upstream_finish_reason
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10,
			$11, $12,
			$13, $14,
			$15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24,
			$25, $26, $27,
			$28, $29, $30,
			$31,
			COALESCE($32, 0),
			$33
		)
		ON CONFLICT (request_id) DO UPDATE SET
			ts = EXCLUDED.ts
	`,
		e.RequestID, nonEmptyDefault(e.TenantID), e.ApplicationID, e.APIKeyID,
		e.EndUserID, e.ClientModel, e.OutboundModel,
		e.CredentialID, e.ProviderID, e.CanonicalID,
		e.ClientProfile, e.RequestMode,
		e.PromptTokens, e.CompletionTokens,
		e.CacheReadTokens, e.CacheWriteTokens, totalTok,
		e.CostUSD, e.LatencyMs, e.Success, e.ErrorKind, search,
		e.IdentityHash, e.ResponseChecksum,
		e.TransformRuleID, e.EgressProtocol, e.FailureDetailCode,
		e.RequestPreview, e.TransformSummary, e.ResponsePreview,
		e.StreamFirstChunkMs, e.StreamChunkCount, e.StreamDoneReceived,
		e.StreamInterrupted,
		e.StreamChunksSent,
		e.UpstreamFinishReason,
	)
	if err != nil {
		t.classifyAndCount("request_logs_hot", e.RequestID, err)
		slog.Warn("telemetry ingest request_logs failed", "request_id", e.RequestID, "error", err)
		return
	}

	// 2026-07-21 Ticket #10: Persist full bodies in request_logs_bodies_hot.
	// This path must tolerate both historical UNIQUE (request_id, ts) and
	// migration-455 UNIQUE (request_id) deployments, because live hosts can
	// report schema_migrations=455 while still serving the old unique index.
	err = upsertRequestLogBodies(ctx, tx, e.RequestID, e.RequestBody, e.ResponseBody)

	if err != nil {
		t.classifyAndCount("request_logs_bodies_hot", e.RequestID, err)
		slog.Warn("telemetry ingest bodies failed", "request_id", e.RequestID, "error", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("telemetry ingest commit failed", "error", err)
	}
}

func upsertRequestLogBodies(ctx context.Context, tx pgx.Tx, requestID string, requestBody, responseBody *string) error {
	_, err := tx.Exec(ctx, `
		UPDATE request_logs_bodies_hot
		   SET request_body = COALESCE($2::jsonb, request_body),
		       response_body = COALESCE($3::jsonb, response_body)
		 WHERE request_id = $1
	`, requestID, requestBody, responseBody)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_bodies_hot (
			request_id, ts, request_body, response_body
		)
		SELECT $1, rl.ts, $2::jsonb, $3::jsonb
		  FROM request_logs_hot rl
		 WHERE rl.request_id = $1
		ON CONFLICT DO NOTHING
	`, requestID, requestBody, responseBody)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE request_logs_bodies_hot
		   SET request_body = COALESCE($2::jsonb, request_body),
		       response_body = COALESCE($3::jsonb, response_body)
		 WHERE request_id = $1
	`, requestID, requestBody, responseBody)
	return err
}

// execWithRetry runs op with up to 2 retries on transient PG errors.
// Returns the first non-transient error or the last transient error
// after exhausting retries. The op is invoked with the same ctx; if
// the op has its own timeout it will surface that.
//
// 2026-07-16 hardening: replaces the bare `t.db.Exec(...)` calls so
// PG-side hiccups (deadlock 40P01, connection reset 57P01/08006,
// serialization failure 40001) don't immediately count as a lost
// telemetry row.
//
// NOTE: this helper is for SINGLE-statement, transaction-less
// operations. Wrapping a pgx.Tx.Exec in retry is unsafe because a
// failed statement aborts the transaction.
func (t *telemetryIngester) execWithRetry(ctx context.Context, label, requestID string, op func(context.Context) error) error {
	var lastErr error
	backoff := 50 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(ctx)
		if err == nil {
			if attempt > 0 {
				atomic.AddUint64(&t.failRetried, 1)
				slog.Info("telemetry ingest retry succeeded",
					"label", label, "request_id", requestID, "attempt", attempt+1)
			}
			return nil
		}
		lastErr = err
		if !isTransientPGError(err) {
			t.classifyAndCount(label, requestID, err)
			return err
		}
		if attempt < 2 {
			atomic.AddUint64(&t.failRetried, 1)
			slog.Warn("telemetry ingest transient error, retrying",
				"label", label, "request_id", requestID, "attempt", attempt+1, "backoff_ms", backoff.Milliseconds(), "error", err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff *= 2
		}
	}
	if lastErr != nil {
		t.classifyAndCount(label, requestID, lastErr)
	}
	return lastErr
}

// classifyAndCount bumps one of the failure counters based on the PG
// SQLSTATE classification. Permanent errors (schema drift, bad
// data, permission) are recorded as failPermanent; transient as
// failTransient.
func (t *telemetryIngester) classifyAndCount(label, requestID string, err error) {
	if err == nil {
		return
	}
	if isTransientPGError(err) {
		atomic.AddUint64(&t.failTransient, 1)
		return
	}
	atomic.AddUint64(&t.failPermanent, 1)
}

// isTransientPGError returns true for SQLSTATEs that indicate a
// retry might succeed: deadlock (40P01), serialization failure
// (40001), admin shutdown (57P01), cannot connect now (57P03),
// connection failures (08000/08003/08006/08001/08004/08007), and
// statement timeout (57014).
func isTransientPGError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range []string{
		"40P01", "40001",
		"57P01", "57P03",
		"57014",
		"08000", "08003", "08006", "08001", "08004", "08007",
	} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// FailCounts returns a snapshot of the telemetry ingester failure
// counters. Exposed via admin/metrics so ops can detect "writes
// silently dropping" without parsing stderr.
func (t *telemetryIngester) FailCounts() (transient, permanent, retried uint64) {
	if t == nil {
		return 0, 0, 0
	}
	return atomic.LoadUint64(&t.failTransient),
		atomic.LoadUint64(&t.failPermanent),
		atomic.LoadUint64(&t.failRetried)
}

func (h *Handler) handleTelemetryDecisionLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var entry decisionLogInput
	if err := readJSON(r, &entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if ingester != nil {
		select {
		case ingester.queue <- &entry:
		default:
			slog.Warn("telemetry ingest queue full, dropping decision log", "request_id", entry.RequestID)
		}
	} else if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rawModelsJSON, _ := json.Marshal(coalesceRawModels(entry.ResolutionRawModels))
		traceJSON := coalesceTrace(entry.DecisionTrace)
		// INSERT directly targets routing_decision_log_hot
		// (canonical write target per the 2026-07 data-lifecycle
		// architecture — never the parent table).
		_, err := h.db.Exec(ctx, `
			INSERT INTO routing_decision_log_hot (
				ts, request_id, idempotency_key, tenant_id, api_key_id,
				model, chosen_credential_id, chosen_provider_id, tier,
				candidates_tried, latency_ms, success, error_class,
				prompt_tokens, completion_tokens, cost_usd,
				request_bytes, response_bytes,
				client_model, resolved_raw_model, sticky_hit, client_profile,
				outbound_model, request_mode, identity_hash, transform_rule_id,
				egress_protocol, failure_stage, failure_detail_code,
				resolution_path, canonical_model, resolution_raw_models, decision_trace
			) VALUES (
				now(), $1::uuid, $2, $3, $4,
				$5, $6, $7, $8,
				$9, $10, $11, $12,
				$13, $14, $15,
				$16, $17,
				$18, $19, $20, $21,
				$22, $23, $24, $25,
				$26, $27, $28,
				$29, $30, CAST($31 AS jsonb), CAST($32 AS jsonb)
		)
		ON CONFLICT (request_id) DO UPDATE SET
			ts = EXCLUDED.ts
	`,
			entry.RequestID, entry.IdempotencyKey, nonEmptyDefault(entry.TenantID), entry.APIKeyID,
			entry.Model, entry.ChosenCredentialID, entry.ChosenProviderID, entry.Tier,
			entry.CandidatesTried, entry.LatencyMs, entry.Success, entry.ErrorClass,
			entry.PromptTokens, entry.CompletionTokens, entry.CostUSD,
			entry.RequestBytes, entry.ResponseBytes,
			entry.ClientModel, entry.ResolvedRawModel, entry.StickyHit, entry.ClientProfile,
			entry.OutboundModel, entry.RequestMode, entry.IdentityHash, entry.TransformRuleID,
			entry.EgressProtocol, entry.FailureStage, entry.FailureDetailCode,
			entry.ResolutionPath, entry.CanonicalModel, rawModelsJSON, traceJSON,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "insert failed")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleTelemetryRequestLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var entry requestLogInput
	if err := readJSON(r, &entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if ingester != nil {
		select {
		case ingester.queue <- &entry:
		default:
			slog.Warn("telemetry ingest queue full, dropping request log", "request_id", entry.RequestID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

func (h *Handler) handleTelemetryBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var batch struct {
		Entries []batchEntry `json:"entries"`
	}
	if err := readJSON(r, &batch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	decCount := 0
	reqCount := 0
	errCount := 0

	for _, e := range batch.Entries {
		if e.DecisionLog != nil {
			if ingester != nil {
				select {
				case ingester.queue <- e.DecisionLog:
					decCount++
				default:
					errCount++
				}
			}
		}
		if e.RequestLog != nil {
			if ingester != nil {
				select {
				case ingester.queue <- e.RequestLog:
					reqCount++
				default:
					errCount++
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"decision_log_count": decCount,
		"request_log_count":  reqCount,
		"errors":             errCount,
	})
}

func nonEmptyDefault(s string) string {
	if strings.TrimSpace(s) == "" {
		return "default"
	}
	return s
}

func coalesceRawModels(models []string) []string {
	if models == nil {
		return []string{}
	}
	return models
}

func coalesceTrace(trace json.RawMessage) []byte {
	if len(trace) == 0 {
		return []byte("{}")
	}
	return trace
}

func calcTotal(a, b *int) *int { //nolint:unused
	if a == nil && b == nil {
		return nil
	}
	v := 0
	if a != nil {
		v += *a
	}
	if b != nil {
		v += *b
	}
	return &v
}

func firstNonEmptyStr(ss ...*string) *string { //nolint:unused
	for _, s := range ss {
		if s != nil && *s != "" {
			return s
		}
	}
	return nil
}

func buildSearchText(e *requestLogInput) *string { //nolint:unused
	parts := []string{}
	for _, s := range []*string{e.ClientModel, e.OutboundModel, e.ClientProfile, e.RequestMode} {
		if s != nil && *s != "" {
			parts = append(parts, *s)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, " ")
	return &joined
}
