// Package routing — candidate_failure_logger.go
//
// 2026-06-23 Phase 2 (P1) of the minimax-m3 transient-error fix:
// persistent log of per-credential / per-model upstream failures.
//
// The Phase 1 fix added upstream response body to request_logs.response_preview
// for transient errors, but operators still couldn't see WHICH credentials were
// failing in a request sequence. This file introduces a dedicated log table
// (candidate_failure_logs) and the writer that the executor calls once per
// failed candidate attempt.
//
// Design notes:
//   - One row per (request_id, credential_id, raw_model_name, attempt_index).
//     attempt_index mirrors the routing layer's attempt counter so retries
//     are visible.
//   - Body is capped at 1KB before INSERT to bound storage. The first 320
//     chars go to upstream_response_preview for fast UI rendering; the full
//     1KB body is in upstream_response_body for forensic use.
//   - Writes are best-effort and use a 3s timeout independent of the
//     request's own context (Background) so a slow request_log write does
//     not delay the user-visible response.
//   - The writer takes a *pgxpool.Pool so it can run independently of any
//     telemetry/client wiring. The pool is the same one the gateway uses
//     for everything else (request_logs, credentials, etc.).
package routing

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// candidateFailureLog is the row shape for candidate_failure_logs. Mirrors
// the migration 037 + 300 schema; keep in sync.
type candidateFailureLog struct {
	RequestID               string
	TenantID                string
	CredentialID            int
	ProviderID              int
	RawModelName            string
	AttemptIndex            int
	ErrorKind               string
	ErrorMessage            string
	UpstreamStatusCode      *int
	UpstreamResponseBody    string
	UpstreamResponsePreview string
	LatencyMs               *int
	PerAttemptLatencyMs     *int
	Retryable               *bool
	Context                 map[string]any
}

// CandidateFailureWriter persists per-credential failure rows so operators
// can see "credential X failed N times in the last hour with status code 502".
// nil-safe: LogFailure is a no-op when writer is nil.
type CandidateFailureWriter struct {
	pool *pgxpool.Pool
}

// NewCandidateFailureWriter wires the writer to the gateway's main DB pool.
// Pass nil to disable the feature (the executor's LogFailure call becomes a
// no-op, preserving behaviour for tests that don't have a DB).
func NewCandidateFailureWriter(pool *pgxpool.Pool) *CandidateFailureWriter {
	return &CandidateFailureWriter{pool: pool}
}

// LogFailure records one candidate-level failure. It is intentionally
// tolerant: any error from the INSERT is logged at warn level but does not
// propagate to the caller, because the user-visible request has already
// been served (or routed elsewhere) by the time we get here.
//
// Captures:
//   - The typed *upstream.Error fields (Kind, StatusCode, Body) when
//     available so the row reflects what the vendor actually returned.
//   - A 320-char preview of the body (UI-friendly) plus the first 1KB
//     body (forensic).
//   - A small JSON `context` blob with model, request_id, attempt_index
//     and any caller-supplied extras — the column is JSONB so callers
//     can attach custom fields without a schema change.
func (w *CandidateFailureWriter) LogFailure(
	requestID, tenantID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) {
	if w == nil || w.pool == nil || execErr == nil {
		return
	}

	row := w.buildRow(requestID, tenantID, credentialID, providerID, rawModelName, attemptIndex, execErr, latencyMs, perAttemptLatencyMs, extraContext)

	// Independent context: never block the request hot path on a slow DB.
	// 3s matches the other telemetry writers in this codebase.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	//nolint:errcheck // best-effort INSERT; log + ignore.
	_, err := w.pool.Exec(ctx, `
		INSERT INTO candidate_failure_logs (
			request_id, tenant_id, credential_id, provider_id, raw_model_name,
			attempt_index, error_kind, error_message,
			upstream_status_code, upstream_response_body, upstream_response_preview,
			latency_ms, per_attempt_latency_ms, retryable, context
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, NULLIF($10, ''), NULLIF($11, ''),
			$12, $13, $14, $15
		)
	`,
		row.RequestID, row.TenantID, row.CredentialID, row.ProviderID, row.RawModelName,
		row.AttemptIndex, row.ErrorKind, row.ErrorMessage,
		row.UpstreamStatusCode, row.UpstreamResponseBody, row.UpstreamResponsePreview,
		row.LatencyMs, row.PerAttemptLatencyMs, row.Retryable, marshalContext(row.Context),
	)
	if err != nil {
		slog.Warn("candidate_failure_logger: insert failed",
			"error", err,
			"request_id", requestID,
			"credential_id", credentialID,
			"raw_model", rawModelName,
		)
	}
}

// buildRow extracts fields from the error chain. Walks errors.Unwrap to
// pull the typed *upstream.Error when present (Phase 1 added Body and
// StatusCode fields to that struct).
func (w *CandidateFailureWriter) buildRow(
	requestID, tenantID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) candidateFailureLog {
	row := candidateFailureLog{
		RequestID:           requestID,
		TenantID:            tenantID,
		CredentialID:        credentialID,
		ProviderID:          providerID,
		RawModelName:        rawModelName,
		AttemptIndex:        attemptIndex,
		ErrorMessage:        execErr.Error(),
		LatencyMs:           latencyMs,
		PerAttemptLatencyMs: perAttemptLatencyMs,
	}

	// Walk the chain to find the typed upstream error and the
	// errorsx.ErrorKind encoded in the message.
	var ue *upstreampkg.Error
	for cur := execErr; cur != nil; cur = unwrapErr(cur) {
		if typed, ok := cur.(*upstreampkg.Error); ok {
			ue = typed
			break
		}
	}

	if ue != nil {
		row.ErrorKind = string(ue.Kind)
		if ue.StatusCode > 0 {
			sc := ue.StatusCode
			row.UpstreamStatusCode = &sc
		}
		if len(ue.Body) > 0 {
			body := string(ue.Body)
			if len(body) > 1024 {
				body = body[:1024]
			}
			row.UpstreamResponseBody = body

			preview := body
			if len(preview) > 320 {
				preview = preview[:320] + "..."
			}
			row.UpstreamResponsePreview = preview
		}
		retryable := errorsx.IsRetryable(ue.Kind)
		row.Retryable = &retryable
	} else {
		// Fallback: classify from the message.
		kind := errorsx.ClassifyError(execErr, nil)
		row.ErrorKind = string(kind)
		retryable := errorsx.IsRetryable(kind)
		row.Retryable = &retryable
	}

	if extraContext != nil {
		row.Context = extraContext
	}
	return row
}

// unwrapErr is a tiny helper that calls the standard errors.Unwrap via the
// error's Unwrap() method, falling back to nil if the error does not
// implement Unwrap. We don't import "errors" at file scope to keep this
// helper trivial to inline-test.
func unwrapErr(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}

// marshalContext renders a map as compact JSON, returning nil when the
// input is empty so the column is NULL (not an empty object).
func marshalContext(m map[string]any) []byte {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
