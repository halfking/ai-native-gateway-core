package streaming

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

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
	db FormatAnomalyExec
}

// AnomalyType classifies different kinds of format issues.
type AnomalyType string

const (
	AnomalyMissingUsage        AnomalyType = "missing_usage_block"
	AnomalyZeroCompletion      AnomalyType = "zero_completion_tokens"
	AnomalyExtractionFailed    AnomalyType = "extraction_failed"
	AnomalyUnexpectedStructure AnomalyType = "unexpected_structure"
	AnomalyNullUsageValues     AnomalyType = "null_usage_values"
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
	return &FormatAnomalyRecorder{db: pool}
}

// RecordAnomaly records a format anomaly to the database.
func (r *FormatAnomalyRecorder) RecordAnomaly(ctx context.Context, record AnomalyRecord) error {
	if r == nil || r.db == nil {
		return nil
	}

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

	_, err = r.db.Exec(ctx, query,
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
