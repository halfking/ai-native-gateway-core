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
}

func NewClient() *Client {
	c := &Client{
		queue: make(chan any, 4096),
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
			gw_session_id, gw_task_id
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
			$41, $42, $43
		)
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
		       error_kind = COALESCE($35, rl.error_kind),
		       latency_ms = COALESCE($36, rl.latency_ms),
		       identity_hash = COALESCE($37, rl.identity_hash),
		       search_text = COALESCE($38, rl.search_text),
		       gw_session_id = COALESCE($39, rl.gw_session_id),
		       gw_task_id = COALESCE($40, rl.gw_task_id)
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
		entry.GwSessionID, entry.GwTaskID,
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
