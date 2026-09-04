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
//   - The writer takes the small database interface it needs so it can run
//     independently of any telemetry/client wiring and remain easy to test.
package executors

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// candidateFailureLog is the row shape for candidate_failure_logs. Mirrors
// the migration 037 + 300 + 358 schema; keep in sync.
type candidateFailureLog struct {
	RequestID               string
	TenantID                string
	SessionID               string
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
type candidateFailureDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

const candidateFailureInsertSQL = `
		INSERT INTO candidate_failure_logs_hot (
			request_id, tenant_id, session_id, credential_id, provider_id, raw_model_name,
			attempt_index, error_kind, error_message,
			upstream_status_code, upstream_response_body, upstream_response_preview,
			latency_ms, per_attempt_latency_ms, retryable, context
		) VALUES (
			$1, $2, NULLIF($3, ''), $4, $5, $6,
			$7, $8, $9,
			$10, NULLIF($11, ''), NULLIF($12, ''),
			$13, $14, $15, $16::text::jsonb
		)
	`

type CandidateFailureWriter struct {
	pool candidateFailureDB
}

// NewCandidateFailureWriter wires the writer to the gateway's main DB pool.
// Pass nil to disable the feature (the executor's LogFailure call becomes a
// no-op, preserving behaviour for tests that don't have a DB).
func NewCandidateFailureWriter(pool candidateFailureDB) *CandidateFailureWriter {
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
	requestID, tenantID, sessionID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) {
	// explicitKind "" 让 buildRow 走自动分类（upstream.Error 类型优先，消息兜底）。
	w.logFailure(requestID, tenantID, sessionID, credentialID, providerID, rawModelName,
		attemptIndex, execErr, "", latencyMs, perAttemptLatencyMs, extraContext)
}

// LogFailureWithKind is LogFailure with a caller-preclassified errorsx kind.
// Used by the mid-stream interruption path where the executor already
// classified the precise kind (streamInterruptedError carries no
// *upstream.Error, so the message-based fallback in buildRow would flatten
// e.g. KindNetwork to KindTransient).
func (w *CandidateFailureWriter) LogFailureWithKind(
	requestID, tenantID, sessionID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	explicitKind errorsx.ErrorKind,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) {
	w.logFailure(requestID, tenantID, sessionID, credentialID, providerID, rawModelName,
		attemptIndex, execErr, explicitKind, latencyMs, perAttemptLatencyMs, extraContext)
}

func (w *CandidateFailureWriter) logFailure(
	requestID, tenantID, sessionID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	explicitKind errorsx.ErrorKind,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) {
	if w == nil || w.pool == nil || execErr == nil {
		return
	}

	row := w.buildRow(requestID, tenantID, sessionID, credentialID, providerID, rawModelName, attemptIndex, execErr, explicitKind, latencyMs, perAttemptLatencyMs, extraContext)

	// Independent context: never block the request hot path on a slow DB.
	// 3s matches the other telemetry writers in this codebase.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := w.pool.Exec(ctx, candidateFailureInsertSQL,
		row.RequestID, row.TenantID, row.SessionID, row.CredentialID, row.ProviderID, row.RawModelName,
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

	// 供应商错误唯一事实源（V371）：同一行数据投影写入 supplier_errors_hot。
	// 共用同一 3s 独立超时上下文；读端（趋势 API、凭据详情、供应商统计）
	// 统一走 supplier_errors_unified / supplier_error_stats。
	w.persistSupplierError(ctx, row)
}

// buildRow extracts fields from the error chain. Walks errors.Unwrap to
// pull the typed *upstream.Error when present (Phase 1 added Body and
// StatusCode fields to that struct).
func (w *CandidateFailureWriter) buildRow(
	requestID, tenantID, sessionID string,
	credentialID, providerID int,
	rawModelName string,
	attemptIndex int,
	execErr error,
	explicitKind errorsx.ErrorKind,
	latencyMs *int,
	perAttemptLatencyMs *int,
	extraContext map[string]any,
) candidateFailureLog {
	row := candidateFailureLog{
		RequestID:    requestID,
		TenantID:     tenantID,
		SessionID:    sessionID,
		CredentialID: credentialID,
		ProviderID:   providerID,
		RawModelName: rawModelName,
		AttemptIndex: attemptIndex,
		// 2026-07-20: defensive nil-check on execErr.Error(). Callers
		// pass typed-nil *upstream.Error via the error interface in
		// some stream-interrupt paths. (*upstream.Error).Error() is
		// also nil-receiver safe now, but this guard means the row
		// renders cleanly even if a future refactor introduces a
		// different Error type without that protection.
		ErrorMessage:        string(errorsx.SanitizeErrorText([]byte(safeErrorMessage(execErr)), 320)),
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

	kind := errorsx.ErrorKind("")
	if ue != nil {
		kind = ue.Kind
		row.ErrorKind = string(kind)
		if ue.StatusCode > 0 {
			sc := ue.StatusCode
			row.UpstreamStatusCode = &sc
		}
		if len(ue.Body) > 0 {
			// Sanitize the raw upstream body BEFORE truncation so that any
			// credentials echoed by the vendor (Bearer tokens, sk-* API keys,
			// api_key= query parameters) are redacted before they land in
			// candidate_failure_logs_hot and the admin credential-detail UI.
			body := string(errorsx.SanitizeErrorText(ue.Body, 1024))
			row.UpstreamResponseBody = body

			preview := truncateUTF8(body, 320)
			if len(preview) < len(body) {
				preview += "..."
			}
			row.UpstreamResponsePreview = preview
		}
	} else {
		// Fallback: classify from the message.
		kind = errorsx.ClassifyError(execErr, nil)
		row.ErrorKind = string(kind)
	}

	// Caller-preclassified kind wins: the executor's stream-interruption
	// path already resolved the precise kind (e.g. KindNetwork for an
	// "other side closed" read failure) and the message-based fallback
	// cannot recover it from "stream_interrupted: <reason>".
	if explicitKind != "" {
		kind = explicitKind
		row.ErrorKind = string(kind)
	}
	projection := errorsx.ProjectRecovery(kind)
	retryable := projection.GenericRetryable
	row.Retryable = &retryable
	row.Context = recoveryContext(extraContext, projection)
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

// safeErrorMessage renders an error's message without panicking on a
// typed-nil interface (Go's classic "var x *T = nil; var e error = x"
// gotcha). The upstream package's (*Error).Error() is also nil-receiver
// safe as of 2026-07-20, but this helper makes the build_row path
// robust to ANY error type a future caller might pass — a panic here
// would block the safety-net audit emit and turn a recoverable
// upstream error into a 500 for the client.
func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	defer func() {
		// Last-resort guard: any panic during err.Error() becomes an
		// empty string rather than crashing the request handler.
		_ = recover()
	}()
	return err.Error()
}

// recoveryContext preserves caller fields and adds the public recovery
// projection to the existing JSONB context column.
func recoveryContext(extra map[string]any, projection errorsx.RecoveryProjection) map[string]any {
	ctx := make(map[string]any, len(extra)+5)
	for key, value := range extra {
		ctx[key] = value
	}
	ctx["generic_retryable"] = projection.GenericRetryable
	ctx["candidate_failover"] = projection.CandidateFailover
	ctx["transparent_resume"] = projection.TransparentResume
	ctx["effective_action"] = projection.EffectiveAction
	ctx["reason"] = projection.Reason
	return ctx
}

// marshalContext renders a map as compact JSON string, returning nil when the
// input is empty so the column is NULL (not an empty object).
// Returns string instead of []byte to match the $N::text::jsonb cast pattern.
func marshalContext(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}
