package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FormatAnomalyExec is the minimal SQL exec surface required by the recorder.
type FormatAnomalyExec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// FormatAnomalyRecorder tracks response format anomalies to help detect
// provider API changes and improve token estimation logic.
type FormatAnomalyRecorder struct {
	db   FormatAnomalyExec
	pool *pgxpool.Pool
}

// AnomalyType classifies different kinds of format issues.
type AnomalyType string

const (
	AnomalyMissingUsage        AnomalyType = "missing_usage_block"
	AnomalyZeroCompletion      AnomalyType = "zero_completion_tokens"
	AnomalyExtractionFailed    AnomalyType = "extraction_failed"
	AnomalyUnexpectedStructure AnomalyType = "unexpected_structure"
	AnomalyNullUsageValues     AnomalyType = "null_usage_values"
	AnomalyPersistenceFailed   AnomalyType = "persistence_failed"
	AnomalyJSONMarshalFailed   AnomalyType = "json_marshal_failed"
	AnomalyLogHarvested        AnomalyType = "log_harvested"
)

// Severity levels for anomalies.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// AnomalyRecord represents a format anomaly to be recorded.
type AnomalyRecord struct {
	RequestID      string
	ProviderID     *int
	ProviderCode   *string
	ClientModel    *string
	OutboundModel  *string
	AnomalyType    AnomalyType
	Severity       Severity
	UsageSource    *string
	ExpectedTokens *int
	ActualTokens   *int
	ContentSize    *int
	Structure      map[string]any
	ResponseSample *string
	TenantID       *string
}

// NewFormatAnomalyRecorder creates a recorder backed by a generic SQL exec surface.
func NewFormatAnomalyRecorder(db FormatAnomalyExec) *FormatAnomalyRecorder {
	return &FormatAnomalyRecorder{db: db}
}

// NewFormatAnomalyRecorderFromPool creates a recorder backed by pgxpool.
func NewFormatAnomalyRecorderFromPool(pool *pgxpool.Pool) *FormatAnomalyRecorder {
	if pool == nil {
		return &FormatAnomalyRecorder{}
	}
	return &FormatAnomalyRecorder{db: pool, pool: pool}
}

// RecordAnomaly records a format anomaly to the database.
func (r *FormatAnomalyRecorder) RecordAnomaly(ctx context.Context, record AnomalyRecord) error {
	if r == nil || r.db == nil {
		return nil
	}

	recordFn := func(exec FormatAnomalyExec) error {
		structureJSON, err := json.Marshal(record.Structure)
		if err != nil {
			structureJSON = []byte(`{"marshal_error":true}`)
		}

		query := `
			INSERT INTO response_format_anomalies (
				request_id, provider_id, provider_code, client_model, outbound_model,
				anomaly_type, severity, usage_source, expected_tokens, actual_tokens,
				content_size_bytes, response_structure, response_sample, tenant_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`

		_, err = exec.Exec(ctx, query,
			record.RequestID,
			record.ProviderID,
			record.ProviderCode,
			record.ClientModel,
			record.OutboundModel,
			string(record.AnomalyType),
			string(record.Severity),
			record.UsageSource,
			record.ExpectedTokens,
			record.ActualTokens,
			record.ContentSize,
			structureJSON,
			record.ResponseSample,
			record.TenantID,
		)
		if err != nil {
			slog.Warn("failed to record format anomaly",
				"request_id", record.RequestID,
				"anomaly_type", record.AnomalyType,
				"error", err)
		}
		return err
	}
	if r.pool == nil {
		return recordFn(r.db)
	}
	return withAnomalyWriteTx(ctx, r.pool, recordFn)
}

func withAnomalyWriteTx(ctx context.Context, pool *pgxpool.Pool, fn func(FormatAnomalyExec) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin anomaly write tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		return fmt.Errorf("set anomaly RLS bypass: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BackfillActualTokens (CO-2, 2026-08-15) fills the actual completion-token
// count onto the anomaly rows recorded for a request whose estimated
// request_logs row has just been corrected with real usage. Only rows still
// lacking an actual_tokens value are touched, so repeated calls are
// idempotent. Errors are returned to the caller, which treats the backfill
// as best-effort (warn-only, never on the request hot path).
func (r *FormatAnomalyRecorder) BackfillActualTokens(ctx context.Context, requestID string, actualTokens int) error {
	if r == nil || r.db == nil || requestID == "" {
		return nil
	}
	query := `
		UPDATE response_format_anomalies
		   SET actual_tokens = $2,
		       usage_source = 'corrected'
		 WHERE request_id = $1
		   AND actual_tokens IS NULL
	`
	execFn := func(exec FormatAnomalyExec) error {
		_, err := exec.Exec(ctx, query, requestID, actualTokens)
		if err != nil {
			slog.Warn("failed to backfill format anomaly actual tokens",
				"request_id", requestID,
				"error", err)
		}
		return err
	}
	if r.pool == nil {
		return execFn(r.db)
	}
	return withAnomalyWriteTx(ctx, r.pool, execFn)
}

// RecordDataAnomaly implements the DataAnomalyRecorder interface for data-level
// anomalies (persistence failures, JSON marshal failures, log harvest issues).
func (r *FormatAnomalyRecorder) RecordDataAnomaly(ctx context.Context, anomalyType, severity, requestID, message string, metadata map[string]any) error {
	if r == nil || r.db == nil {
		return nil
	}
	structureJSON, err := json.Marshal(metadata)
	if err != nil {
		slog.Warn("failed to marshal data anomaly metadata",
			"request_id", requestID, "anomaly_type", anomalyType, "error", err)
		structureJSON = []byte(`{}`)
	}
	sev := Severity(severity)
	query := `
		INSERT INTO response_format_anomalies (
			request_id, anomaly_type, severity, response_structure, response_sample, detected_at
		) VALUES ($1, $2, $3, $4, $5, NOW())
	`
	if r.pool == nil {
		_, err = r.db.Exec(ctx, query,
			requestID,
			anomalyType,
			sev,
			structureJSON,
			message,
		)
	} else {
		err = withAnomalyWriteTx(ctx, r.pool, func(exec FormatAnomalyExec) error {
			_, err := exec.Exec(ctx, query,
				requestID,
				anomalyType,
				sev,
				structureJSON,
				message,
			)
			return err
		})
	}
	if err != nil {
		slog.Warn("failed to record data anomaly",
			"request_id", requestID,
			"anomaly_type", anomalyType,
			"error", err)
		return err
	}
	return nil
}

// AnalyzeResponseStructure creates a simplified structure map for analysis.
func AnalyzeResponseStructure(responseBody []byte) map[string]any {
	if len(responseBody) == 0 {
		return map[string]any{"empty": true}
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &obj); err != nil {
		return map[string]any{
			"parse_error": true,
			"error":       err.Error(),
		}
	}

	structure := make(map[string]any)
	if choicesRaw, ok := obj["choices"]; ok {
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(choicesRaw, &choices); err == nil {
			structure["has_choices"] = true
			structure["choices_count"] = len(choices)
			if len(choices) > 0 {
				firstChoice := choices[0]
				choiceFields := make([]string, 0, len(firstChoice))
				for k := range firstChoice {
					choiceFields = append(choiceFields, k)
				}
				structure["choice_fields"] = choiceFields
				if msgRaw, ok := firstChoice["message"]; ok {
					var msg map[string]json.RawMessage
					if err := json.Unmarshal(msgRaw, &msg); err == nil {
						msgFields := make([]string, 0, len(msg))
						for k := range msg {
							msgFields = append(msgFields, k)
						}
						structure["message_fields"] = msgFields
						if contentRaw, ok := msg["content"]; ok {
							var contentStr string
							if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
								structure["content_type"] = "string"
								structure["content_length"] = len(contentStr)
							} else {
								structure["content_type"] = "complex"
							}
						}
					}
				}
				if _, ok := firstChoice["finish_reason"]; ok {
					structure["has_finish_reason"] = true
				}
			}
		}
	}

	if usageRaw, ok := obj["usage"]; ok {
		var usage map[string]json.RawMessage
		if err := json.Unmarshal(usageRaw, &usage); err == nil {
			structure["has_usage"] = true
			usageFields := make([]string, 0, len(usage))
			for k := range usage {
				usageFields = append(usageFields, k)
			}
			structure["usage_fields"] = usageFields
		} else {
			structure["has_usage"] = "null_or_invalid"
		}
	} else {
		structure["has_usage"] = false
	}

	return structure
}

// ShouldRecordAnomaly determines if an anomaly should be recorded based on
// context. This prevents flooding the table with known/expected cases.
func ShouldRecordAnomaly(anomalyType AnomalyType, providerCode string) bool {
	if anomalyType == AnomalyZeroCompletion {
		knownQuirkyProviders := map[string]bool{
			"minimax": true,
		}
		if knownQuirkyProviders[providerCode] {
			return time.Now().UnixNano()%100 < 1
		}
		return time.Now().UnixNano()%100 < 10
	}
	if anomalyType == AnomalyExtractionFailed || anomalyType == AnomalyUnexpectedStructure {
		return true
	}
	if anomalyType == AnomalyMissingUsage {
		return time.Now().UnixNano()%100 < 5
	}
	return true
}

// TruncateForSample truncates a string to max length for sample storage.
func TruncateForSample(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}
