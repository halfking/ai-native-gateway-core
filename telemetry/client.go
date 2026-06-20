package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errNoTelemetryDB = errors.New("telemetry database not configured")

type Client struct {
	dbPool *pgxpool.Pool

	queue chan any
	done  chan struct{}
	wg    sync.WaitGroup
}

type DecisionLogEntry struct {
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

// RequestLogOp distinguishes insert-at-start from update-on-complete.
type RequestLogOp string

const (
	RequestLogInsert RequestLogOp = "insert"
	RequestLogUpdate RequestLogOp = "update"
)

// Request log lifecycle status stored in request_logs.request_status.
const (
	RequestStatusInProgress = "in_progress"
	RequestStatusSuccess    = "success"
	RequestStatusFailure    = "failure"
)

type RequestLogEntry struct {
	Op RequestLogOp `json:"op,omitempty"`
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
	CostDisplay        *float64 `json:"cost_display,omitempty"`
	CostCurrency       *string  `json:"cost_currency,omitempty"`
	LatencyMs          *int     `json:"latency_ms,omitempty"`
	Success            bool     `json:"success"`
	RequestStatus      *string  `json:"request_status,omitempty"`
	ErrorKind          *string  `json:"error_kind,omitempty"`
	// UsageSource indicates where the token counts came from:
	//   "llm"       — extracted from upstream response.usage block
	//   "estimated" — computed locally from request/response text (fallback)
	//   ""          — not available (request failed before parsing)
	UsageSource        *string  `json:"usage_source,omitempty"`
	IdentityHash       *string  `json:"identity_hash,omitempty"`
	StreamFirstChunkMs *int     `json:"stream_first_chunk_ms,omitempty"`
	StreamChunkCount   *int     `json:"stream_chunk_count,omitempty"`
	StreamDoneReceived *bool    `json:"stream_done_received,omitempty"`
	StreamInterrupted  *bool    `json:"stream_interrupted,omitempty"`
	ResponseChecksum   *string  `json:"response_checksum,omitempty"`
	FailureDetailCode  *string  `json:"failure_detail_code,omitempty"`
	FailureStage       *string  `json:"failure_stage,omitempty"`
	TransformRuleID    *string  `json:"transform_rule_id,omitempty"`
	EgressProtocol     *string  `json:"egress_protocol,omitempty"`
	RequestPreview     *string  `json:"request_preview,omitempty"`
	TransformSummary   *string  `json:"transform_summary,omitempty"`
	ResponsePreview    *string  `json:"response_preview,omitempty"`
	RequestBody        *string  `json:"request_body,omitempty"`
	ResponseBody       *string  `json:"response_body,omitempty"`
	GwSessionID        *string  `json:"gw_session_id,omitempty"`
	GwTaskID           *string  `json:"gw_task_id,omitempty"`
	APIKeyPrefix       *string  `json:"api_key_prefix,omitempty"`
	APIKeyOwnerUser    *string  `json:"api_key_owner_user,omitempty"`
	ApplicationCode    *string  `json:"application_code,omitempty"`
	// v2.0 auto-route observability (requires 2026-06-15-auto-route-mode.sql)
	IsAutoRequest      *bool    `json:"is_auto_request,omitempty"`
	TaskType           *string  `json:"task_type,omitempty"`
	AutoProfile        *string  `json:"auto_profile,omitempty"`
	AutoDecision       *string  `json:"auto_decision,omitempty"`
	AutoConfidence     *float64 `json:"auto_confidence,omitempty"`
	WorkType           *string  `json:"work_type,omitempty"`

	// P7.2: promoted from auto_decision JSONB to dedicated columns
	// for indexable queries (see ensureRequestLogAutoDecisionColumns).
	TaskTypeChosen *string  `json:"task_type_chosen,omitempty"`
	ConfidenceNum  *float64 `json:"confidence_num,omitempty"`
	ModelChosen    *string  `json:"model_chosen,omitempty"`
	StrategyUsed   *string  `json:"strategy_used,omitempty"`
	CreditsCharged *int64   `json:"credits_charged,omitempty"`

	// Round 47 (2026-06-18) compression v7 T2: parent-child chain tracking.
	// Mirrors the 4 columns added by db/migrations/013_compression_columns.sql
	// (parent_request_id, compression_reason, compression_strategy,
	// compression_meta). Populated by compressor/ when mode=1 (auto_threshold)
	// or mode=2 (on_4xx) fires. Single-level chain: a child row has at most
	// 1 parent (its own request_id pre-compression); no grandparent.
	// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.1.
	ParentRequestID     *string         `json:"parent_request_id,omitempty"`
	CompressionReason   *string         `json:"compression_reason,omitempty"`
	CompressionStrategy *string         `json:"compression_strategy,omitempty"`
	CompressionMeta     json.RawMessage `json:"compression_meta,omitempty"`

	// v3 (2026-06-19) session-level outbound body T23.
	// Mirrors 4 columns added by db/migrations/016_outbound_body.sql.
	// Populated by compressor.SessionCompressor when the session cache
	// rewrites the body (delta-append + optional sliding-window summary).
	// NULL means no session compressor was active for this request.
	OutboundBody      json.RawMessage `json:"outbound_body,omitempty"`
	OutboundMsgCount  *int            `json:"outbound_msg_count,omitempty"`
	OutboundTokenEst  *int            `json:"outbound_token_est,omitempty"`
	OutboundMsgHashes json.RawMessage `json:"outbound_msg_hashes,omitempty"`

	// 2026-06-19: tool_call quality signals (017_quality_fix_mode.sql).
	// QualityFlags is the array of detected issues (empty_tool_name,
	// duplicate_tool_call_id, …). QualityFixActions is the JSON
	// {flag: {detected, renamed, dropped}} tally of what was actually
	// done in the response body. QualityScore is 0..1, nil when the
	// quality processor was off for this provider.
	QualityFlags     []string        `json:"quality_flags,omitempty"`
	QualityFixActions json.RawMessage `json:"quality_fix_actions,omitempty"`
	QualityScore     *float64        `json:"quality_score,omitempty"`

	// 2026-06-19 T-NEW-7: the upstream finish_reason (stop, tool_calls,
	// length, end_turn, function_call, max_tokens, …). Stored in
	// request_logs.upstream_finish_reason — the SOLE home for the
	// finish_reason.  Distinct from FailureDetailCode which is now
	// reserved for actual failure / interruption codes.  Populated for
	// BOTH success and failure rows.
	UpstreamFinishReason *string `json:"upstream_finish_reason,omitempty"`
}

func NewClient() *Client {
	return newClientWithBufSize(4096)
}

func newClientWithBufSize(bufSize int) *Client {
	c := &Client{
		queue: make(chan any, bufSize),
		done:  make(chan struct{}),
	}
	c.wg.Add(1)
	go c.worker()
	return c
}

func (c *Client) Enabled() bool {
	return c.dbPool != nil
}

func (c *Client) SetDB(pool *pgxpool.Pool) {
	c.dbPool = pool
}

func (c *Client) EmitDecisionLog(entry *DecisionLogEntry) {
	if !c.Enabled() {
		return
	}
	select {
	case c.queue <- entry:
	default:
		// Decision logs power /routing-decisions — never silently drop on backpressure.
		if err := c.insertDecisionLog(entry); err != nil {
			slog.Warn("telemetry decision sync insert failed", "request_id", entry.RequestID, "error", err)
		}
	}
}

func (c *Client) EmitRequestLog(entry *RequestLogEntry) {
	if !c.Enabled() {
		return
	}
	if entry.Op == "" {
		entry.Op = RequestLogInsert
	}
	select {
	case c.queue <- entry:
	default:
		// Request logs power /request-logs — never silently drop on backpressure.
		if err := c.persistRequestLog(entry); err != nil {
			slog.Warn("telemetry request sync persist failed", "request_id", entry.RequestID, "op", entry.Op, "error", err)
		}
	}
}

func (c *Client) EmitRequestLogInsert(entry *RequestLogEntry) {
	entry.Op = RequestLogInsert
	c.EmitRequestLog(entry)
}

func (c *Client) EmitRequestLogUpdate(entry *RequestLogEntry) {
	entry.Op = RequestLogUpdate
	c.EmitRequestLog(entry)
}

func (c *Client) Stop() {
	close(c.done)
	c.wg.Wait()
}

func (c *Client) worker() {
	defer c.wg.Done()

	batch := make([]any, 0, 50)
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case <-c.done:
			c.flush(batch)
			return
		case item := <-c.queue:
			batch = append(batch, item)
			if len(batch) >= 50 {
				c.flush(batch)
				batch = batch[:0]
				timer.Reset(200 * time.Millisecond)
			} else if len(batch) == 1 {
				timer.Reset(200 * time.Millisecond)
			}
		case <-timer.C:
			if len(batch) > 0 {
				c.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (c *Client) flush(batch []any) {
	batch = mergeRequestLogBatch(batch)
	for _, item := range batch {
		switch v := item.(type) {
		case *DecisionLogEntry:
			if err := c.insertDecisionLog(v); err != nil {
				slog.Warn("telemetry decision db insert failed", "request_id", v.RequestID, "error", err)
			}
		case *RequestLogEntry:
			if err := c.persistRequestLog(v); err != nil {
				slog.Warn("telemetry request db persist failed", "request_id", v.RequestID, "op", v.Op, "error", err)
			}
		}
	}
}

func (c *Client) insertDecisionLog(entry *DecisionLogEntry) error {
	if c.dbPool == nil {
		return errNoTelemetryDB
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rawModelsJSON, _ := json.Marshal(coalesceRawModels(entry.ResolutionRawModels))
	traceJSON := coalesceTrace(entry.DecisionTrace)
	_, err := c.dbPool.Exec(ctx, `
		INSERT INTO routing_decision_log (
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
			now(), $1, $2, $3, $4,
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
		entry.RequestID,
		entry.IdempotencyKey,
		nonEmpty(entry.TenantID, "default"),
		entry.APIKeyID,
		entry.Model,
		entry.ChosenCredentialID,
		entry.ChosenProviderID,
		entry.Tier,
		entry.CandidatesTried,
		entry.LatencyMs,
		entry.Success,
		entry.ErrorClass,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CostUSD,
		entry.RequestBytes,
		entry.ResponseBytes,
		entry.ClientModel,
		entry.ResolvedRawModel,
		entry.StickyHit,
		entry.ClientProfile,
		entry.OutboundModel,
		entry.RequestMode,
		entry.IdentityHash,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.ResolutionPath,
		entry.CanonicalModel,
		rawModelsJSON,
		traceJSON,
	)
	return err
}

func (c *Client) persistRequestLog(entry *RequestLogEntry) error {
	normalizeRequestStatus(entry)
	if entry.Op == RequestLogUpdate {
		return c.updateRequestLog(entry)
	}
	return c.insertRequestLog(entry)
}

func (c *Client) insertRequestLog(entry *RequestLogEntry) error {
	if c.dbPool == nil {
		return errNoTelemetryDB
	}
	// Defence-in-depth: scrub any invalid UTF-8 from all string-valued fields
	// before INSERT.  PostgreSQL rejects invalid bytes with SQLSTATE 22021,
	// which (because we wrap usage_ledger + request_logs + api_keys updates
	// in a single transaction) causes the entire row to be dropped — exactly
	// the symptom reported in the 2026-06-11 incident where glm-5.1 calls
	// succeeded for the client but no request_logs row was written.  Even
	// after fixing the upstream truncation sites, this layer guarantees that
	// no future regression can silently lose rows.
	sanitizeRequestLogEntry(entry)
	totalTokens := total(entry.PromptTokens, entry.CompletionTokens)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := c.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger (
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
		entry.RequestID,
		nonEmpty(entry.TenantID, "default"),
		entry.ApplicationID,
		entry.APIKeyID,
		entry.EndUserID,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		firstString(entry.OutboundModel, entry.ClientModel),
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		totalTokens,
		entry.CostUSD,
		entry.LatencyMs,
		entry.Success,
		entry.ErrorKind,
	)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs (
			request_id, ts, tenant_id, application_id, api_key_id,
			end_user_id, client_model, outbound_model,
			credential_id, provider_id, canonical_id,
			client_profile, request_mode,
			prompt_tokens, completion_tokens,
			cache_read_tokens, cache_write_tokens, total_tokens,
			cost_usd, cost_display, cost_currency,
			latency_ms, success, request_status, error_kind, search_text,
			identity_hash, response_checksum,
			transform_rule_id, egress_protocol, failure_stage, failure_detail_code,
			request_preview, transform_summary, response_preview,
			request_body, response_body,
			stream_first_chunk_ms, stream_chunk_count, stream_done_received,
			stream_interrupted,
			usage_source,
			gw_session_id, gw_task_id,
			api_key_prefix, api_key_owner_user, application_code,
			is_auto_request, task_type, auto_profile, auto_decision, auto_confidence,
			work_type, credits_charged,
			-- Round 47 compression v7 T2: parent-child chain (4 columns).
			parent_request_id, compression_reason, compression_strategy, compression_meta,
			-- v3 (2026-06-19) T23: session-level outbound body (4 columns).
			outbound_body, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
			-- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
			quality_flags, quality_fix_actions, quality_score,
			-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
			-- (db/migrations/018_upstream_finish_reason.sql). The new column is
			-- the SOLE home for the upstream finish_reason.
			upstream_finish_reason
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10,
			$11, $12,
			$13, $14,
			$15, $16, $17,
			$18, $19, $20,
			$21, $22, $23, $24, $25,
			$26, $27,
			$28, $29, $30, $31,
			$32, $33, $34,
			CAST($35 AS jsonb), CAST($36 AS jsonb),
			$37, $38, $39,
			$40,
			$41,
			$42, $43,
			$44, $45, $46,
			$47, $48, $49, CAST($50 AS jsonb), $51,
			$52, $53,
			$54, $55, $56, CAST($57 AS jsonb),
			CAST($58 AS jsonb), $59, $60, CAST($61 AS jsonb),
			CAST($62 AS text[]), CAST($63 AS jsonb), $64,
			$65
			)
			ON CONFLICT (request_id, ts) DO UPDATE SET
				ts = EXCLUDED.ts,
			tenant_id = EXCLUDED.tenant_id,
			application_id = EXCLUDED.application_id,
			api_key_id = EXCLUDED.api_key_id,
			end_user_id = EXCLUDED.end_user_id,
			client_model = EXCLUDED.client_model,
			outbound_model = EXCLUDED.outbound_model,
			credential_id = EXCLUDED.credential_id,
			provider_id = EXCLUDED.provider_id,
			canonical_id = EXCLUDED.canonical_id,
			client_profile = EXCLUDED.client_profile,
			request_mode = EXCLUDED.request_mode,
			prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens,
			total_tokens = EXCLUDED.total_tokens,
			cost_usd = EXCLUDED.cost_usd,
			cost_display = EXCLUDED.cost_display,
			cost_currency = EXCLUDED.cost_currency,
			latency_ms = EXCLUDED.latency_ms,
			success = EXCLUDED.success,
			request_status = EXCLUDED.request_status,
			error_kind = CASE
				WHEN EXCLUDED.success = TRUE THEN NULL
				ELSE EXCLUDED.error_kind
			END,
			search_text = EXCLUDED.search_text,
			identity_hash = EXCLUDED.identity_hash,
			response_checksum = EXCLUDED.response_checksum,
			transform_rule_id = EXCLUDED.transform_rule_id,
			egress_protocol = EXCLUDED.egress_protocol,
			failure_stage = EXCLUDED.failure_stage,
			failure_detail_code = EXCLUDED.failure_detail_code,
			request_preview = EXCLUDED.request_preview,
			transform_summary = EXCLUDED.transform_summary,
			response_preview = EXCLUDED.response_preview,
			request_body = EXCLUDED.request_body,
			response_body = EXCLUDED.response_body,
			stream_first_chunk_ms = EXCLUDED.stream_first_chunk_ms,
			stream_chunk_count = EXCLUDED.stream_chunk_count,
			stream_done_received = EXCLUDED.stream_done_received,
			stream_interrupted = EXCLUDED.stream_interrupted,
			usage_source = EXCLUDED.usage_source,
			gw_session_id = EXCLUDED.gw_session_id,
			gw_task_id = EXCLUDED.gw_task_id,
			api_key_prefix = EXCLUDED.api_key_prefix,
			api_key_owner_user = EXCLUDED.api_key_owner_user,
			application_code = EXCLUDED.application_code,
			is_auto_request = EXCLUDED.is_auto_request,
			task_type = EXCLUDED.task_type,
			auto_profile = EXCLUDED.auto_profile,
			auto_decision = EXCLUDED.auto_decision,
			auto_confidence = EXCLUDED.auto_confidence,
			work_type = EXCLUDED.work_type,
			credits_charged = EXCLUDED.credits_charged,
			parent_request_id = EXCLUDED.parent_request_id,
			compression_reason = EXCLUDED.compression_reason,
			compression_strategy = EXCLUDED.compression_strategy,
			compression_meta = EXCLUDED.compression_meta,
			outbound_body = EXCLUDED.outbound_body,
			outbound_msg_count = EXCLUDED.outbound_msg_count,
			outbound_token_est = EXCLUDED.outbound_token_est,
			outbound_msg_hashes = EXCLUDED.outbound_msg_hashes,
			quality_flags = EXCLUDED.quality_flags,
			quality_fix_actions = EXCLUDED.quality_fix_actions,
			quality_score = EXCLUDED.quality_score,
			upstream_finish_reason = EXCLUDED.upstream_finish_reason
	`,
		entry.RequestID,
		nonEmpty(entry.TenantID, "default"),
		entry.ApplicationID,
		entry.APIKeyID,
		entry.EndUserID,
		entry.ClientModel,
		entry.OutboundModel,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		entry.ClientProfile,
		entry.RequestMode,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		totalTokens,
		entry.CostUSD,
		entry.CostDisplay,
		entry.CostCurrency,
		entry.LatencyMs,
		entry.Success,
		entry.RequestStatus,
		entry.ErrorKind,
		searchText(entry),
		entry.IdentityHash,
		entry.ResponseChecksum,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.RequestPreview,
		entry.TransformSummary,
		entry.ResponsePreview,
		entry.RequestBody,
		entry.ResponseBody,
		entry.StreamFirstChunkMs,
		entry.StreamChunkCount,
		entry.StreamDoneReceived,
		entry.StreamInterrupted,
		nonEmptyPtr(entry.UsageSource, "llm"),
		entry.GwSessionID,
		entry.GwTaskID,
		entry.APIKeyPrefix,
		entry.APIKeyOwnerUser,
		entry.ApplicationCode,
		entry.IsAutoRequest,
		entry.TaskType,
		entry.AutoProfile,
		entry.AutoDecision,
		entry.AutoConfidence,
		entry.WorkType,
		entry.CreditsCharged,
		// Round 47 compression v7 T2: parent-child chain payload.
		entry.ParentRequestID,
		entry.CompressionReason,
		entry.CompressionStrategy,
		entry.CompressionMeta,
		// v3 (2026-06-19) T23: session-level outbound body payload.
		entry.OutboundBody,
		entry.OutboundMsgCount,
		entry.OutboundTokenEst,
		entry.OutboundMsgHashes,
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// quality_flags is bound as text[]; we cast nil to NULL so the
		// column DEFAULT '{}' kicks in. quality_fix_actions is JSONB.
		qualityFlagsArg(entry.QualityFlags),
		qualityActionsArg(entry.QualityFixActions),
		entry.QualityScore,
		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		// (db/migrations/018_upstream_finish_reason.sql). The new column is
		// the SOLE home for the upstream finish_reason.
		entry.UpstreamFinishReason,
	)
	if err != nil {
		return err
	}

	if entry.APIKeyID != nil && *entry.APIKeyID > 0 && entry.Success {
		var promptAdd, completionAdd int64
		if entry.PromptTokens != nil {
			promptAdd = int64(*entry.PromptTokens)
		}
		if entry.CompletionTokens != nil {
			completionAdd = int64(*entry.CompletionTokens)
		}
		var costAdd float64
		if entry.CostUSD != nil {
			costAdd = *entry.CostUSD
		}
		_, err = tx.Exec(ctx, `
			UPDATE api_keys SET
				total_requests = total_requests + 1,
				total_prompt_tokens = total_prompt_tokens + $2,
				total_completion_tokens = total_completion_tokens + $3,
				total_cost_usd = total_cost_usd + $4,
				last_request_at = now()
			WHERE id = $1
		`, *entry.APIKeyID, promptAdd, completionAdd, costAdd)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (c *Client) updateRequestLog(entry *RequestLogEntry) error {
	if c.dbPool == nil {
		return errNoTelemetryDB
	}
	sanitizeRequestLogEntry(entry)
	totalTokens := total(entry.PromptTokens, entry.CompletionTokens)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := c.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if entry.PromptTokens != nil || entry.CompletionTokens != nil {
		_, err = tx.Exec(ctx, `
			UPDATE usage_ledger
			   SET prompt_tokens = COALESCE($2, prompt_tokens),
			       completion_tokens = COALESCE($3, completion_tokens),
			       total_tokens = COALESCE($4, total_tokens),
			       cache_read_tokens = COALESCE($5, cache_read_tokens),
			       cache_write_tokens = COALESCE($6, cache_write_tokens),
			       cost_usd = COALESCE($7, cost_usd),
			       latency_ms = COALESCE($8, latency_ms),
			       success = COALESCE($9, success),
			       error_kind = COALESCE($10, error_kind)
			 WHERE request_id = $1
		`,
			entry.RequestID,
			entry.PromptTokens,
			entry.CompletionTokens,
			totalTokens,
			entry.CacheReadTokens,
			entry.CacheWriteTokens,
			entry.CostUSD,
			entry.LatencyMs,
			boolptr(entry.Success),
			entry.ErrorKind,
		)
		if err != nil {
			return err
		}
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE usage_ledger
			   SET latency_ms = COALESCE($2, latency_ms),
			       success = COALESCE($3, success),
			       error_kind = COALESCE($4, error_kind),
			       cost_usd = COALESCE($5, cost_usd)
			 WHERE request_id = $1
		`,
			entry.RequestID,
			entry.LatencyMs,
			boolptr(entry.Success),
			entry.ErrorKind,
			entry.CostUSD,
		)
		if err != nil {
			return err
		}
	}

	tag, err := tx.Exec(ctx, `
		WITH latest AS (
			SELECT id, ts
			FROM request_logs
			WHERE request_id = $1
			ORDER BY ts DESC
			LIMIT 1
		)
		UPDATE request_logs rl
		   SET client_model = COALESCE($2, rl.client_model),
		       outbound_model = COALESCE($3, rl.outbound_model),
		       credential_id = COALESCE($4, rl.credential_id),
		       provider_id = COALESCE($5, rl.provider_id),
		       canonical_id = COALESCE($6, rl.canonical_id),
		       client_profile = COALESCE($7, rl.client_profile),
		       request_mode = COALESCE($8, rl.request_mode),
		       end_user_id = COALESCE($9, rl.end_user_id),
		       prompt_tokens = COALESCE($10, rl.prompt_tokens),
		       completion_tokens = COALESCE($11, rl.completion_tokens),
		       total_tokens = COALESCE($12, rl.total_tokens),
		       cache_read_tokens = COALESCE($13, rl.cache_read_tokens),
		       cache_write_tokens = COALESCE($14, rl.cache_write_tokens),
		       cost_usd = COALESCE($15, rl.cost_usd),
		       cost_display = COALESCE($16, rl.cost_display),
		       cost_currency = COALESCE($17, rl.cost_currency),
		       stream_first_chunk_ms = COALESCE($18, rl.stream_first_chunk_ms),
		       stream_chunk_count = COALESCE($19, rl.stream_chunk_count),
		       stream_done_received = COALESCE($20, rl.stream_done_received),
		       stream_interrupted = COALESCE($21, rl.stream_interrupted),
		       response_checksum = COALESCE($22, rl.response_checksum),
		       response_preview = COALESCE($23, rl.response_preview),
		       response_body = COALESCE(CAST($24 AS jsonb), rl.response_body),
		       failure_stage = COALESCE($25, rl.failure_stage),
		       failure_detail_code = COALESCE($26, rl.failure_detail_code),
		       transform_rule_id = COALESCE($27, rl.transform_rule_id),
		       egress_protocol = COALESCE($28, rl.egress_protocol),
		       request_preview = COALESCE($29, rl.request_preview),
		       transform_summary = COALESCE($30, rl.transform_summary),
		       request_body = COALESCE(CAST($31 AS jsonb), rl.request_body),
		       usage_source = COALESCE(NULLIF($32, ''), rl.usage_source),
		       success = COALESCE($33, rl.success),
		       request_status = COALESCE($34, rl.request_status),
		       -- 2026-06-20: clear error_kind on success to prevent
		       -- cross-request pollution (e.g. a previous failure's
		       -- error_kind leaking into a later successful UPDATE).
		       error_kind = CASE
		           WHEN COALESCE($33, rl.success) = TRUE THEN NULL
		           ELSE COALESCE($35, rl.error_kind)
		       END,
		       latency_ms = COALESCE($36, rl.latency_ms),
		       identity_hash = COALESCE($37, rl.identity_hash),
		       search_text = COALESCE($38, rl.search_text),
		       gw_session_id = COALESCE($39, rl.gw_session_id),
		       gw_task_id = COALESCE($40, rl.gw_task_id),
		       api_key_prefix = COALESCE($41, rl.api_key_prefix),
		       api_key_owner_user = COALESCE($42, rl.api_key_owner_user),
		       application_code = COALESCE($43, rl.application_code),
		       is_auto_request = COALESCE($44, rl.is_auto_request),
		       task_type = COALESCE($45, rl.task_type),
		       auto_profile = COALESCE($46, rl.auto_profile),
		       auto_decision = COALESCE(CAST($47 AS jsonb), rl.auto_decision),
		       auto_confidence = COALESCE($48, rl.auto_confidence),
		       work_type = COALESCE($49, rl.work_type),
		       credits_charged = COALESCE($50, rl.credits_charged),
		       -- Round 47 compression v7 T2: parent-child chain payload.
		       parent_request_id = COALESCE($51, rl.parent_request_id),
		       compression_reason = COALESCE($52, rl.compression_reason),
		       compression_strategy = COALESCE($53, rl.compression_strategy),
		       compression_meta = COALESCE(CAST($54 AS jsonb), rl.compression_meta),
		       -- v3 (2026-06-19) T23: session-level outbound body payload.
		       outbound_body      = COALESCE(CAST($55 AS jsonb), rl.outbound_body),
		       outbound_msg_count = COALESCE($56, rl.outbound_msg_count),
		       outbound_token_est = COALESCE($57, rl.outbound_token_est),
		       outbound_msg_hashes = COALESCE(CAST($58 AS jsonb), rl.outbound_msg_hashes),
		       -- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		       quality_flags        = COALESCE(CAST($59 AS text[]), rl.quality_flags),
		       quality_fix_actions  = COALESCE(CAST($60 AS jsonb), rl.quality_fix_actions),
		       quality_score        = COALESCE($61, rl.quality_score),
		       -- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		       -- (db/migrations/018_upstream_finish_reason.sql). The new column is
		       -- the SOLE home for the upstream finish_reason.
		       upstream_finish_reason = COALESCE($62, rl.upstream_finish_reason)
		  FROM latest
		 WHERE rl.id = latest.id
		   AND rl.ts = latest.ts
	`,
		entry.RequestID,
		entry.ClientModel,
		entry.OutboundModel,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		entry.ClientProfile,
		entry.RequestMode,
		entry.EndUserID,
		entry.PromptTokens,
		entry.CompletionTokens,
		totalTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		entry.CostUSD,
		entry.CostDisplay,
		entry.CostCurrency,
		entry.StreamFirstChunkMs,
		entry.StreamChunkCount,
		entry.StreamDoneReceived,
		entry.StreamInterrupted,
		entry.ResponseChecksum,
		entry.ResponsePreview,
		entry.ResponseBody,
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.RequestPreview,
		entry.TransformSummary,
		entry.RequestBody,
		nonEmptyPtr(entry.UsageSource, ""),
		boolptr(entry.Success),
		entry.RequestStatus,
		entry.ErrorKind,
		entry.LatencyMs,
		entry.IdentityHash,
		searchText(entry),
		entry.GwSessionID,
		entry.GwTaskID,
		entry.APIKeyPrefix,
		entry.APIKeyOwnerUser,
		entry.ApplicationCode,
		entry.IsAutoRequest,
		entry.TaskType,
		entry.AutoProfile,
		entry.AutoDecision,
		entry.AutoConfidence,
		entry.WorkType,
		entry.CreditsCharged,
		// Round 47 compression v7 T2: parent-child chain payload.
		entry.ParentRequestID,
		entry.CompressionReason,
		entry.CompressionStrategy,
		entry.CompressionMeta,
		// v3 (2026-06-19) T23: session-level outbound body payload.
		entry.OutboundBody,
		entry.OutboundMsgCount,
		entry.OutboundTokenEst,
		entry.OutboundMsgHashes,
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// quality_flags is bound as text[]; we cast nil to NULL so the
		// column DEFAULT '{}' kicks in. quality_fix_actions is JSONB.
		qualityFlagsArg(entry.QualityFlags),
		qualityActionsArg(entry.QualityFixActions),
		entry.QualityScore,
		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		// (db/migrations/018_upstream_finish_reason.sql). The new column is
		// the SOLE home for the upstream finish_reason.
		entry.UpstreamFinishReason,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// No early row — fall back to insert so the request is not lost.
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			slog.Warn("telemetry update rollback failed", "request_id", entry.RequestID, "error", rbErr)
		}
		fallback := *entry
		fallback.Op = RequestLogInsert
		return c.insertRequestLog(&fallback)
	}

	if entry.APIKeyID != nil && *entry.APIKeyID > 0 && entry.Success {
		var promptAdd, completionAdd int64
		if entry.PromptTokens != nil {
			promptAdd = int64(*entry.PromptTokens)
		}
		if entry.CompletionTokens != nil {
			completionAdd = int64(*entry.CompletionTokens)
		}
		var costAdd float64
		if entry.CostUSD != nil {
			costAdd = *entry.CostUSD
		}
		_, err = tx.Exec(ctx, `
			UPDATE api_keys SET
				total_requests = total_requests + 1,
				total_prompt_tokens = total_prompt_tokens + $2,
				total_completion_tokens = total_completion_tokens + $3,
				total_cost_usd = total_cost_usd + $4,
				last_request_at = now()
			WHERE id = $1
		`, *entry.APIKeyID, promptAdd, completionAdd, costAdd)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func intptr(v int) *int           { return &v }
func floatptr(v float64) *float64 { return &v }
func strptr(v string) *string     { return &v }
func boolptr(v bool) *bool        { return &v }

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func nonEmptyPtr(p *string, fallback string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return fallback
	}
	return *p
}

// qualityFlagsArg converts a nil/empty []string into a value suitable
// for binding to a text[] column.  The column is NOT NULL with a
// DEFAULT '{}'::text[] — but specifying a column explicitly in the
// INSERT (which the gateway must, since it has 60+ other columns)
// OVERRIDES the default and applies whatever the bind value is.
// Passing nil would then trip `null value in column "quality_flags"
// violates not-null constraint` at runtime, so we coerce empty
// slices into a non-nil `[]string{}` so the bind produces a real
// empty array.  When the slice has elements we return it as-is.
func qualityFlagsArg(flags []string) any {
	if flags == nil {
		return []string{}
	}
	return flags
}

// qualityActionsArg turns the JSONB payload into a value safe to bind
// with pgx.  The column is NOT NULL with a DEFAULT '{}'::jsonb —
// the same DEFAULT-override caveat as qualityFlagsArg applies: an
// explicit nil bind in the INSERT would trip the not-null check.
// We therefore always return a non-nil byte slice; empty/missing
// inputs become a literal "{}" which the SQL CAST($63 AS jsonb)
// turns into a JSONB empty object, identical to the column DEFAULT.
func qualityActionsArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return []byte(raw)
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

func total(prompt, completion *int) *int {
	if prompt == nil && completion == nil {
		return nil
	}
	value := 0
	if prompt != nil {
		value += *prompt
	}
	if completion != nil {
		value += *completion
	}
	return &value
}

func firstString(values ...*string) *string {
	for _, value := range values {
		if value != nil && *value != "" {
			return value
		}
	}
	return nil
}

func searchText(entry *RequestLogEntry) *string {
	parts := make([]string, 0, 6)
	for _, value := range []*string{
		entry.ClientModel, entry.OutboundModel, entry.ClientProfile, entry.RequestMode,
		entry.GwSessionID, entry.GwTaskID, entry.APIKeyPrefix, entry.APIKeyOwnerUser, entry.ApplicationCode,
	} {
		if value != nil && *value != "" {
			parts = append(parts, *value)
		}
	}
	if len(parts) == 0 {
		empty := ""
		return &empty
	}
	joined := strings.Join(parts, " ")
	return &joined
}

func normalizeRequestStatus(entry *RequestLogEntry) {
	if entry == nil {
		return
	}
	if entry.RequestStatus != nil && *entry.RequestStatus != "" {
		return
	}
	status := ResolveRequestStatus(entry.Success, entry.ErrorKind, entry.Op == RequestLogInsert)
	entry.RequestStatus = &status
}

// ResolveRequestStatus derives the three-state lifecycle label used by request_logs.
func ResolveRequestStatus(success bool, errorKind *string, isInitialInsert bool) string {
	if success {
		return RequestStatusSuccess
	}
	if errorKind != nil && strings.TrimSpace(*errorKind) != "" {
		return RequestStatusFailure
	}
	if isInitialInsert {
		return RequestStatusInProgress
	}
	return RequestStatusInProgress
}

// sanitizeUTF8 returns a copy of s with every invalid UTF-8 byte sequence
// replaced by the Unicode replacement character (U+FFFD).  The result is
// always valid UTF-8 and safe for PostgreSQL columns with encoding=UTF8.
//
// Additionally, backslashes are escaped to prevent "unsupported Unicode
// escape sequence" (SQLSTATE 22P05) errors when a string contains sequences
// that PostgreSQL interprets as Unicode escapes (e.g. \uXXXX, \UXXXXXXXX,
// or lone \ followed by non-hex chars).  See incident 2026-06-11.
//
// Use sanitizeUTF8JSON instead for strings stored in JSONB columns.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return strings.ReplaceAll(s, "\\", "\\\\")
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/10)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\uFFFD")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return strings.ReplaceAll(b.String(), "\\", "\\\\")
}

// scrubUTF8ForJSON replaces invalid UTF-8 byte sequences with U+FFFD.
// Unlike sanitizeUTF8, backslashes are preserved so JSON escape sequences stay intact.
func scrubUTF8ForJSON(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/10)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\uFFFD")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// truncateToValidJSON walks backward from the end of s, trying each closing
// brace/bracket as a candidate boundary until json.Valid succeeds.
func truncateToValidJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if json.Valid([]byte(s)) {
		return s, true
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != '}' && s[i] != ']' {
			continue
		}
		candidate := strings.TrimSpace(s[:i+1])
		if candidate != "" && json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// sanitizeUTF8JSON scrubs invalid UTF-8 and ensures the result is valid JSON
// before CAST(... AS jsonb). On unrecoverable corruption it returns "" so callers
// can store NULL and let UPDATE COALESCE keep the previous body.
func sanitizeUTF8JSON(s string) string {
	cleaned := scrubUTF8ForJSON(s)
	if repaired, ok := truncateToValidJSON(cleaned); ok {
		return repaired
	}
	return ""
}

func sanitizeStringPtr(p **string) {
	if *p == nil {
		return
	}
	clean := sanitizeUTF8(**p)
	*p = &clean
}

func sanitizeRequestLogEntry(e *RequestLogEntry) {
	sanitizeStringPtr(&e.ClientModel)
	sanitizeStringPtr(&e.OutboundModel)
	sanitizeStringPtr(&e.ClientProfile)
	sanitizeStringPtr(&e.RequestMode)
	sanitizeStringPtr(&e.ErrorKind)
	sanitizeStringPtr(&e.UsageSource)
	sanitizeStringPtr(&e.IdentityHash)
	sanitizeStringPtr(&e.ResponseChecksum)
	sanitizeStringPtr(&e.TransformRuleID)
	sanitizeStringPtr(&e.EgressProtocol)
	sanitizeStringPtr(&e.FailureDetailCode)
	sanitizeStringPtr(&e.FailureStage)
	sanitizeStringPtr(&e.RequestPreview)
	sanitizeStringPtr(&e.TransformSummary)
	sanitizeStringPtr(&e.ResponsePreview)
	sanitizeStringPtr(&e.GwSessionID)
	sanitizeStringPtr(&e.GwTaskID)
	sanitizeStringPtr(&e.APIKeyPrefix)
	sanitizeStringPtr(&e.APIKeyOwnerUser)
	sanitizeStringPtr(&e.ApplicationCode)
	e.RequestID = sanitizeUTF8(e.RequestID)
	e.TenantID = sanitizeUTF8(e.TenantID)
	if e.EndUserID != nil {
		clean := sanitizeUTF8(*e.EndUserID)
		e.EndUserID = &clean
	}
	if e.CostCurrency != nil {
		clean := sanitizeUTF8(*e.CostCurrency)
		e.CostCurrency = &clean
	}
	sanitizeJSONField(&e.RequestBody)
	sanitizeJSONField(&e.ResponseBody)
}

func sanitizeJSONField(p **string) {
	if *p == nil {
		return
	}
	v := sanitizeUTF8JSON(**p)
	if v == "" {
		*p = nil
		return
	}
	*p = &v
}

// mergeRequestLogBatch coalesces multiple updates for the same request_id so a
// burst of stream telemetry writes one DB round-trip instead of many.
func mergeRequestLogBatch(batch []any) []any {
	if len(batch) < 2 {
		return batch
	}
	merged := make([]any, 0, len(batch))
	pending := make(map[string]*RequestLogEntry)
	for _, item := range batch {
		entry, ok := item.(*RequestLogEntry)
		if !ok || entry.Op != RequestLogUpdate {
			merged = append(merged, item)
			continue
		}
		if existing, found := pending[entry.RequestID]; found {
			mergeRequestLogEntry(existing, entry)
			continue
		}
		cp := *entry
		pending[entry.RequestID] = &cp
	}
	for _, entry := range pending {
		merged = append(merged, entry)
	}
	return merged
}

func mergeRequestLogEntry(dst, src *RequestLogEntry) {
	if src == nil || dst == nil {
		return
	}
	dst.Op = RequestLogUpdate
	mergeStringPtr(&dst.ClientModel, src.ClientModel)
	mergeStringPtr(&dst.OutboundModel, src.OutboundModel)
	mergeIntPtr(&dst.CredentialID, src.CredentialID)
	mergeIntPtr(&dst.ProviderID, src.ProviderID)
	mergeIntPtr(&dst.CanonicalID, src.CanonicalID)
	mergeStringPtr(&dst.ClientProfile, src.ClientProfile)
	mergeStringPtr(&dst.RequestMode, src.RequestMode)
	mergeStringPtr(&dst.EndUserID, src.EndUserID)
	mergeIntPtr(&dst.PromptTokens, src.PromptTokens)
	mergeIntPtr(&dst.CompletionTokens, src.CompletionTokens)
	mergeIntPtr(&dst.CacheReadTokens, src.CacheReadTokens)
	mergeIntPtr(&dst.CacheWriteTokens, src.CacheWriteTokens)
	mergeFloatPtr(&dst.CostUSD, src.CostUSD)
	mergeFloatPtr(&dst.CostDisplay, src.CostDisplay)
	mergeStringPtr(&dst.CostCurrency, src.CostCurrency)
	mergeIntPtr(&dst.StreamFirstChunkMs, src.StreamFirstChunkMs)
	mergeIntPtr(&dst.StreamChunkCount, src.StreamChunkCount)
	mergeBoolPtr(&dst.StreamDoneReceived, src.StreamDoneReceived)
	mergeBoolPtr(&dst.StreamInterrupted, src.StreamInterrupted)
	mergeStringPtr(&dst.ResponseChecksum, src.ResponseChecksum)
	mergeStringPtr(&dst.ResponsePreview, src.ResponsePreview)
	mergeStringPtr(&dst.ResponseBody, src.ResponseBody)
	mergeStringPtr(&dst.FailureDetailCode, src.FailureDetailCode)
	mergeStringPtr(&dst.FailureStage, src.FailureStage)
	mergeStringPtr(&dst.TransformRuleID, src.TransformRuleID)
	mergeStringPtr(&dst.EgressProtocol, src.EgressProtocol)
	mergeStringPtr(&dst.RequestPreview, src.RequestPreview)
	mergeStringPtr(&dst.TransformSummary, src.TransformSummary)
	mergeStringPtr(&dst.RequestBody, src.RequestBody)
	mergeStringPtr(&dst.UsageSource, src.UsageSource)
	mergeStringPtr(&dst.ErrorKind, src.ErrorKind)
	mergeStringPtr(&dst.RequestStatus, src.RequestStatus)
	mergeIntPtr(&dst.LatencyMs, src.LatencyMs)
	mergeStringPtr(&dst.IdentityHash, src.IdentityHash)
	mergeStringPtr(&dst.GwSessionID, src.GwSessionID)
	mergeStringPtr(&dst.GwTaskID, src.GwTaskID)
	mergeStringPtr(&dst.APIKeyPrefix, src.APIKeyPrefix)
	mergeStringPtr(&dst.APIKeyOwnerUser, src.APIKeyOwnerUser)
	mergeStringPtr(&dst.ApplicationCode, src.ApplicationCode)
	mergeInt64Ptr(&dst.CreditsCharged, src.CreditsCharged)
	if src.Success {
		dst.Success = true
	}
	if src.RequestStatus != nil && *src.RequestStatus != "" {
		v := *src.RequestStatus
		dst.RequestStatus = &v
	} else if src.Success {
		v := RequestStatusSuccess
		dst.RequestStatus = &v
	} else if src.ErrorKind != nil && *src.ErrorKind != "" {
		v := RequestStatusFailure
		dst.RequestStatus = &v
	}
	// 2026-06-20: when a later success update arrives, the SQL CASE
	// (`WHEN EXCLUDED.success = TRUE THEN NULL`) will correctly clear
	// error_kind in the DB. We mirror that here so the merged entry
	// is internally consistent — important for log/debug paths that
	// inspect the in-memory batched entry before it is persisted.
	if dst.Success {
		dst.ErrorKind = nil
	}
}

func mergeStringPtr(dst **string, src *string) {
	if src != nil && *src != "" {
		v := *src
		*dst = &v
	}
}

func mergeIntPtr(dst **int, src *int) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeInt64Ptr(dst **int64, src *int64) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeFloatPtr(dst **float64, src *float64) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeBoolPtr(dst **bool, src *bool) {
	if src != nil {
		v := *src
		*dst = &v
	}
}
