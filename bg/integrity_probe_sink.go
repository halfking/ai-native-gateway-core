package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const integrityProbeWriteTimeout = 5 * time.Second

// IntegrityProbeResultSink persists the outcome of a durable integrity probe.
// Implementations must be nil-safe at the worker boundary: a reporting failure
// must never prevent the queue lease from being completed.
type IntegrityProbeResultSink interface {
	Record(ctx context.Context, task ProbeQueueTask, target *ProbeTarget, result *ProbeResult, targetErr error) error
}

// PostgresIntegrityProbeResultSink writes probe outcomes to
// model_integrity_events and resolves the event class that triggered the
// verification when the provider answers successfully.
type PostgresIntegrityProbeResultSink struct {
	db *pgxpool.Pool
}

func NewPostgresIntegrityProbeResultSink(db *pgxpool.Pool) *PostgresIntegrityProbeResultSink {
	return &PostgresIntegrityProbeResultSink{db: db}
}

func (s *PostgresIntegrityProbeResultSink) Record(parent context.Context, task ProbeQueueTask, target *ProbeTarget, result *ProbeResult, targetErr error) error {
	if s == nil || s.db == nil || task.Command != "integrity_verify" {
		return nil
	}
	if result == nil {
		status := ProbeStatusFailed
		message := "integrity probe did not produce a result"
		if targetErr != nil {
			message = targetErr.Error()
		}
		result = &ProbeResult{Status: status, ErrCode: "load_target", ErrMsg: message}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), integrityProbeWriteTimeout)
	defer cancel()

	anomaly := normalizeIntegrityAnomaly(task.ReasonCode)
	severity := "high"
	if result.Status == ProbeStatusSuccess {
		severity = "low"
	}
	credentialID := task.CredentialID
	providerID := task.ProviderID
	rawModel := task.RawModel
	outboundModel := task.Outbound
	tenantID := task.TenantID
	if target != nil {
		credentialID = int64(target.CredentialID)
		providerID = int64(target.ProviderID)
		rawModel = target.RawModel
		outboundModel = target.OutboundModel
	}

	status := string(result.Status)
	if status == "" {
		status = "failed"
	}
	detail := result.ErrMsg
	if detail == "" && targetErr != nil {
		detail = targetErr.Error()
	}
	ctxPayload := map[string]any{
		"probe":         true,
		"probe_command": task.Command,
		"probe_status":  status,
		"http_status":   result.HTTPStatus,
		"err_code":      result.ErrCode,
		"latency_ms":    result.LatencyMs,
		"attempt":       task.Attempt,
		"queue_id":      task.ID,
	}
	if detail != "" {
		ctxPayload["error_detail"] = truncateProbeText(detail, 512)
	}
	if preview := result.ResponseBody; preview != "" {
		ctxPayload["response_preview"] = truncateProbeText(preview, 512)
	} else if result.RespPreview != "" {
		ctxPayload["response_preview"] = truncateProbeText(result.RespPreview, 512)
	}
	contextJSON, err := json.Marshal(ctxPayload)
	if err != nil {
		return fmt.Errorf("marshal integrity probe context: %w", err)
	}

	// sample column holds PII-safe metadata only (request URL, err_code).
	// Response bodies belong in the JSON context blob above. We do not
	// store the model's model_mismatch returned value (which can include
	// the upstream's model name) to avoid duplicating it with the
	// outbound_model column. probeSample is the in-helper that prefers
	// the upstream URL so operators can pivot from the integrity event
	// back to the request_logs row.
	sample := probeSample(result)
	if len(sample) > 256 {
		sample = sample[:256]
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin integrity probe result: %w", err)
	}
	defer tx.Rollback(ctx)

	// expected_value carries the source anomaly (why the planner
	// enqueued the probe). actual_value carries the probe outcome so
	// operators can see "expected vs actual" in the admin UI.
	_, err = tx.Exec(ctx, `
		INSERT INTO model_integrity_events (
			ts, tenant_id, provider_id, credential_id,
			outbound_model, raw_model_name, anomaly_type, severity,
			expected_value, actual_value, sample, context
		) VALUES (now(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		nilIfEmpty(tenantID), nullableInt64(providerID), nullableInt64(credentialID),
		nilIfEmpty(outboundModel), nilIfEmpty(rawModel), anomaly, severity,
		anomaly, status, nilIfEmpty(sample), contextJSON)
	if err != nil {
		return fmt.Errorf("insert integrity probe result: %w", err)
	}

	if result.Status == ProbeStatusSuccess {
		// tenant_id guard prevents an integrity event in another tenant
		// that happens to share credential_id/raw_model_name from being
		// silently resolved.
		_, err = tx.Exec(ctx, `
			UPDATE model_integrity_events
			SET resolved = true, resolved_at = now(),
				resolution_notes = $5
			WHERE tenant_id IS NOT DISTINCT FROM $1
			  AND credential_id = $2
			  AND raw_model_name = $3
			  AND anomaly_type = $4
			  AND resolved = false`, tenantID, credentialID, rawModel, anomaly,
			fmt.Sprintf("integrity probe succeeded: %s", status))
		if err != nil {
			return fmt.Errorf("resolve integrity events: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit integrity probe result: %w", err)
	}
	return nil
}

func normalizeIntegrityAnomaly(value string) string {
	switch strings.TrimSpace(value) {
	case "model_mismatch", "finish_refusal", "finish_truncation", "token_arith_fail", "empty_response", "repeated_content", "fingerprint_drift":
		return strings.TrimSpace(value)
	default:
		return "model_mismatch"
	}
}

func probeSample(result *ProbeResult) string {
	if result == nil {
		return ""
	}
	// The sample column is PII-safe metadata only. Prefer the request URL
	// so operators can pivot from a model_integrity_events row back to
	// the originating request_logs row via task ID / RequestURL. Fall back
	// to the structured error code; never store the model output here.
	if result.RequestURL != "" {
		return truncateProbeText(result.RequestURL, 256)
	}
	if result.ErrCode != "" {
		return result.ErrCode
	}
	return ""
}

func truncateProbeText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
