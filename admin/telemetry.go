package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type decisionLogInput struct {
	RequestID          string   `json:"request_id"`
	IdempotencyKey     *string  `json:"idempotency_key,omitempty"`
	TenantID           string   `json:"tenant_id"`
	APIKeyID           *int     `json:"api_key_id,omitempty"`
	Model              string   `json:"model"`
	ChosenCredentialID *int     `json:"chosen_credential_id,omitempty"`
	ChosenProviderID   *int     `json:"chosen_provider_id,omitempty"`
	Tier               *int     `json:"tier,omitempty"`
	CandidatesTried    int      `json:"candidates_tried"`
	LatencyMs          int      `json:"latency_ms"`
	Success            bool     `json:"success"`
	ErrorClass         *string  `json:"error_class,omitempty"`
	PromptTokens       *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens   *int     `json:"completion_tokens,omitempty"`
	CostUSD            *float64 `json:"cost_usd,omitempty"`
	RequestBytes       *int     `json:"request_bytes,omitempty"`
	ResponseBytes      *int     `json:"response_bytes,omitempty"`
	ClientModel        *string  `json:"client_model,omitempty"`
	ResolvedRawModel   *string  `json:"resolved_raw_model,omitempty"`
	OutboundModel      *string  `json:"outbound_model,omitempty"`
	StickyHit          *bool    `json:"sticky_hit,omitempty"`
	ClientProfile      *string  `json:"client_profile,omitempty"`
	RequestMode        *string  `json:"request_mode,omitempty"`
	IdentityHash       *string  `json:"identity_hash,omitempty"`
	TransformRuleID    *string  `json:"transform_rule_id,omitempty"`
	EgressProtocol     *string  `json:"egress_protocol,omitempty"`
	FailureStage       *string  `json:"failure_stage,omitempty"`
	FailureDetailCode  *string  `json:"failure_detail_code,omitempty"`
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
	StreamDoneReceived *bool    `json:"stream_done_received,omitempty"`
	StreamInterrupted  *bool    `json:"stream_interrupted,omitempty"`
	ResponseChecksum   *string  `json:"response_checksum,omitempty"`
	FailureDetailCode  *string  `json:"failure_detail_code,omitempty"`
	TransformRuleID    *string  `json:"transform_rule_id,omitempty"`
	EgressProtocol     *string  `json:"egress_protocol,omitempty"`
	RequestPreview     *string  `json:"request_preview,omitempty"`
	TransformSummary   *string  `json:"transform_summary,omitempty"`
	ResponsePreview    *string  `json:"response_preview,omitempty"`
	RequestBody        *string  `json:"request_body,omitempty"`
	ResponseBody       *string  `json:"response_body,omitempty"`
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
}

var ingester *telemetryIngester

func startIngester(db *pgxpool.Pool) {
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

func stopIngester() {
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
	_, err := t.db.Exec(ctx, `
		INSERT INTO routing_decision_log (
			ts, request_id, idempotency_key, tenant_id, api_key_id,
			model, chosen_credential_id, chosen_provider_id, tier,
			candidates_tried, latency_ms, success, error_class,
			prompt_tokens, completion_tokens, cost_usd,
			request_bytes, response_bytes,
			client_model, resolved_raw_model, sticky_hit, client_profile,
			outbound_model, request_mode, identity_hash, transform_rule_id,
			egress_protocol, failure_stage, failure_detail_code
		) VALUES (
			now(), $1::uuid, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15,
			$16, $17,
			$18, $19, $20, $21,
			$22, $23, $24, $25,
			$26, $27, $28
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
	)
	if err != nil {
		slog.Warn("telemetry ingest decision log failed", "error", err)
	}
}

func (t *telemetryIngester) persistRequestLog(ctx context.Context, e *requestLogInput) {
	totalTok := calcTotal(e.PromptTokens, e.CompletionTokens)
	rawModel := firstNonEmptyStr(e.OutboundModel, e.ClientModel)
	search := buildSearchText(e)

	tx, err := t.db.Begin(ctx)
	if err != nil {
		slog.Warn("telemetry ingest begin failed", "error", err)
		return
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
		e.RequestID, nonEmptyDefault(e.TenantID), e.ApplicationID, e.APIKeyID,
		e.EndUserID, e.CredentialID, e.ProviderID, e.CanonicalID,
		rawModel, e.PromptTokens, e.CompletionTokens,
		e.CacheReadTokens, e.CacheWriteTokens,
		totalTok, e.CostUSD, e.LatencyMs, e.Success, e.ErrorKind,
	)
	if err != nil {
		slog.Warn("telemetry ingest usage_ledger failed", "error", err)
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs (
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
			request_body, response_body,
			stream_first_chunk_ms, stream_chunk_count, stream_done_received,
			stream_interrupted
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
			CAST($31 AS jsonb), CAST($32 AS jsonb),
			$33, $34, $35,
			$36
		)
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
		e.RequestBody, e.ResponseBody,
		e.StreamFirstChunkMs, e.StreamChunkCount, e.StreamDoneReceived,
		e.StreamInterrupted,
	)
	if err != nil {
		slog.Warn("telemetry ingest request_logs failed", "error", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("telemetry ingest commit failed", "error", err)
	}
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
		_, err := h.db.Exec(ctx, `
			INSERT INTO routing_decision_log (
				ts, request_id, idempotency_key, tenant_id, api_key_id,
				model, chosen_credential_id, chosen_provider_id, tier,
				candidates_tried, latency_ms, success, error_class,
				prompt_tokens, completion_tokens, cost_usd,
				request_bytes, response_bytes,
				client_model, resolved_raw_model, sticky_hit, client_profile,
				outbound_model, request_mode, identity_hash, transform_rule_id,
				egress_protocol, failure_stage, failure_detail_code
			) VALUES (
				now(), $1::uuid, $2, $3, $4,
				$5, $6, $7, $8,
				$9, $10, $11, $12,
				$13, $14, $15,
				$16, $17,
				$18, $19, $20, $21,
				$22, $23, $24, $25,
				$26, $27, $28
			)
		`,
			entry.RequestID, entry.IdempotencyKey, nonEmptyDefault(entry.TenantID), entry.APIKeyID,
			entry.Model, entry.ChosenCredentialID, entry.ChosenProviderID, entry.Tier,
			entry.CandidatesTried, entry.LatencyMs, entry.Success, entry.ErrorClass,
			entry.PromptTokens, entry.CompletionTokens, entry.CostUSD,
			entry.RequestBytes, entry.ResponseBytes,
			entry.ClientModel, entry.ResolvedRawModel, entry.StickyHit, entry.ClientProfile,
			entry.OutboundModel, entry.RequestMode, entry.IdentityHash, entry.TransformRuleID,
			entry.EgressProtocol, entry.FailureStage, entry.FailureDetailCode,
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
		"status":              "ok",
		"decision_log_count":  decCount,
		"request_log_count":   reqCount,
		"errors":              errCount,
	})
}

func nonEmptyDefault(s string) string {
	if strings.TrimSpace(s) == "" {
		return "default"
	}
	return s
}

func calcTotal(a, b *int) *int {
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

func firstNonEmptyStr(ss ...*string) *string {
	for _, s := range ss {
		if s != nil && *s != "" {
			return s
		}
	}
	return nil
}

func buildSearchText(e *requestLogInput) *string {
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
