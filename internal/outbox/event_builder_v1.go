// event_builder_v1.go builds request.completed.v1 events whose payload
// satisfies gateway-event-schema-v1.json RequestCompletedPayload (GW-1.2).
//
// The v1 payload contract differs from the legacy omni-ref2 payload in three
// ways:
//   - tokens are flat integers (input_tokens / output_tokens), not the nested
//     token_usage object;
//   - cost_usd is a decimal string (avoids float precision issues);
//   - status is the enum success|error (legacy used succeeded|failed|timeout).
//
// For backward compatibility every legacy field (session_id, turn_no,
// correlation_id, idempotency_key, token_usage, body_refs) is still emitted
// alongside the v1 fields, and the legacy status value is preserved under
// legacy_status. The only semantic rename is payload.status itself, which the
// v1 schema constrains to success|error.
package outbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// BuildSessionOpenedEventV1 constructs the durable session.opened.v1 event.
// The event ID is derived only from tenantID and sessionID so repeated request
// log writes for the same session are idempotent.
func BuildSessionOpenedEventV1(tenantID, sessionID, userID string) (EventEnvelope, error) {
	if tenantID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildSessionOpenedEventV1: tenant_id is required")
	}
	if sessionID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildSessionOpenedEventV1: session_id is required")
	}
	if userID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildSessionOpenedEventV1: user_id is required")
	}

	hashInput := tenantID + "\x00" + sessionID
	digest := sha256.Sum256([]byte(hashInput))
	eventID := "evt-session-opened-" + hex.EncodeToString(digest[:8])
	now := time.Now().UTC()

	return EventEnvelope{
		EventID:          eventID,
		EventType:        "session.opened.v1",
		SchemaVersion:    1,
		TenantID:         tenantID,
		AggregateID:      sessionID,
		AggregateVersion: 1,
		OccurredAt:       now,
		Payload: map[string]any{
			"session_id":    sessionID,
			"user_id":       userID,
			"source_system": "gateway",
		},
		SessionID:     sessionID,
		CorrelationID: sessionID,
		SourceSystem:  "gateway",
	}, nil
}

// BuildRequestCompletedEventV3 constructs a request.completed.v1 event whose
// wire rendering validates against gateway-event-schema-v1.json.
//
// It supersedes BuildRequestCompletedEventV2 for the write path: same
// parameters plus costUSD (nullable; rendered as a decimal string cost_usd
// and required by the v1 payload, so nil renders "0").
//
// status semantics: success=false renders payload.status="error" plus
// payload.error_code (timeout when the internal status says timeout, the
// internal status string otherwise) — the v1 enum has no "timeout" member,
// the distinction moves into error_code.
func BuildRequestCompletedEventV3(
	tenantID, sessionID string,
	turnNo int,
	requestID, correlationID, idempotencyKey string,
	provider, model, status string,
	promptTokens, completionTokens, latencyMs int,
	costUSD *float64,
	success bool,
) (EventEnvelope, error) {
	if requestID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildRequestCompletedEventV3: request_id is required")
	}
	if correlationID == "" {
		correlationID = requestID
	}
	if idempotencyKey == "" {
		idempotencyKey = requestID
	}
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}

	legacyStatus := "succeeded"
	if !success {
		if status == "timeout" || status == "upstream_timeout" {
			legacyStatus = "timeout"
		} else {
			legacyStatus = "failed"
		}
	}

	now := time.Now().UTC()
	hashInput := tenantID + "\x00request.completed.v1\x00" + requestID
	digest := sha256.Sum256([]byte(hashInput))
	eventID := "evt-request-completed-" + hex.EncodeToString(digest[:16])

	payload := map[string]any{
		// V1 required fields (gateway-event-schema-v1.json RequestCompletedPayload)
		"model":         model,
		"provider":      provider,
		"input_tokens":  promptTokens,
		"output_tokens": completionTokens,
		"cost_usd":      FormatCostUSD(costUSD),
		"latency_ms":    latencyMs,
		"status":        "success",
		// Legacy omni-ref2 fields, retained for existing consumers
		"session_id":      sessionID,
		"turn_no":         turnNo,
		"request_id":      requestID,
		"correlation_id":  correlationID,
		"idempotency_key": idempotencyKey,
		"token_usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
		"body_refs": map[string]string{
			"prompt_ref":   fmt.Sprintf("internal://body/%s/prompt", requestID),
			"response_ref": fmt.Sprintf("internal://body/%s/response", requestID),
		},
		"legacy_status": legacyStatus,
	}

	if !success {
		payload["status"] = "error"
		errorCode := status
		if errorCode == "" {
			errorCode = legacyStatus
		}
		payload["error_code"] = errorCode
	}

	return EventEnvelope{
		EventID:          eventID,
		EventType:        "request.completed.v1",
		SchemaVersion:    1, // storage representation; wire renders "1.0"
		TenantID:         tenantID,
		AggregateID:      sessionID,
		AggregateVersion: turnNo + 1,
		OccurredAt:       now,
		Payload:          payload,
		SessionID:        sessionID,
		RequestID:        requestID,
		CorrelationID:    correlationID,
		SourceSystem:     "gateway",
	}, nil
}

// FormatCostUSD renders cost as a decimal string matching the v1 schema
// pattern ^[0-9]+(\.[0-9]+)?$. nil renders "0" (cost_usd is required);
// negatives are clamped to "0" because refunds never appear on the event
// contract. Scientific notation is avoided by the 'f' format.
func FormatCostUSD(costUSD *float64) string {
	if costUSD == nil || *costUSD < 0 || *costUSD == 0 {
		return "0"
	}
	return strconv.FormatFloat(*costUSD, 'f', -1, 64)
}
