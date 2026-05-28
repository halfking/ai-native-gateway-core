package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errNoTelemetryDB = errors.New("telemetry database not configured")

type Client struct {
	endpoint   string
	adminKey   string
	httpClient *http.Client
	dbPool     *pgxpool.Pool

	queue chan any
	done  chan struct{}
	wg    sync.WaitGroup
}

type DecisionLogEntry struct {
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

type RequestLogEntry struct {
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

func NewClient(pythonEndpoint, adminAPIKey string) *Client {
	c := &Client{
		endpoint:   pythonEndpoint,
		adminKey:   adminAPIKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		queue:      make(chan any, 4096),
		done:       make(chan struct{}),
	}
	c.wg.Add(1)
	go c.worker()
	return c
}

func (c *Client) Enabled() bool {
	return c.dbPool != nil || (c.endpoint != "" && c.adminKey != "")
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
		slog.Warn("telemetry queue full, dropping decision log", "request_id", entry.RequestID)
	}
}

func (c *Client) EmitRequestLog(entry *RequestLogEntry) {
	if !c.Enabled() {
		return
	}
	select {
	case c.queue <- entry:
	default:
		slog.Warn("telemetry queue full, dropping request log", "request_id", entry.RequestID)
	}
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
	for _, item := range batch {
		switch v := item.(type) {
		case *DecisionLogEntry:
			if err := c.insertDecisionLog(v); err != nil {
				slog.Debug("telemetry decision db insert failed, falling back to RPC", "request_id", v.RequestID, "error", err)
				c.send("/api/telemetry/decision-log", v)
			}
		case *RequestLogEntry:
			if err := c.insertRequestLog(v); err != nil {
				slog.Debug("telemetry request db insert failed, falling back to RPC", "request_id", v.RequestID, "error", err)
				c.send("/api/telemetry/request-log", v)
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
	_, err := c.dbPool.Exec(ctx, `
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
			now(), $1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15,
			$16, $17,
			$18, $19, $20, $21,
			$22, $23, $24, $25,
			$26, $27, $28
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
	)
	return err
}

func (c *Client) insertRequestLog(entry *RequestLogEntry) error {
	if c.dbPool == nil {
		return errNoTelemetryDB
	}
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
		entry.LatencyMs,
		entry.Success,
		entry.ErrorKind,
		searchText(entry),
		entry.IdentityHash,
		entry.ResponseChecksum,
		entry.TransformRuleID,
		entry.EgressProtocol,
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
	)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) send(path string, payload any) {
	if c.endpoint == "" || c.adminKey == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("telemetry marshal error", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		slog.Warn("telemetry request error", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.adminKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Debug("telemetry send failed", "path", path, "error", err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Debug("telemetry send non-2xx", "path", path, "status", resp.StatusCode)
	}
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
	parts := make([]string, 0, 4)
	for _, value := range []*string{entry.ClientModel, entry.OutboundModel, entry.ClientProfile, entry.RequestMode} {
		if value != nil && *value != "" {
			parts = append(parts, *value)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, " ")
	return &joined
}
