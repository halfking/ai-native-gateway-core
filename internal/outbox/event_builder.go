package outbox

import (
	"fmt"
	"time"
)

// BuildRequestCompletedEvent constructs a request.completed.v1 event envelope from
// business request log data. Returns an EventEnvelope ready for OutboxWriter.Write().
//
// Parameters:
//   - tenantID: extracted from request context (required for tenant isolation)
//   - sessionID: gw_session_id from request_logs (aggregate root)
//   - turnNo: turn number within the session (for aggregate_version)
//   - requestID: unique request identifier (idempotency + correlation)
//   - provider: canonical provider name (e.g. "anthropic", "openai")
//   - model: outbound model name (e.g. "claude-3-5-sonnet-20241022")
//   - status: request outcome ("succeeded" | "failed" | "timeout")
//   - promptTokens, completionTokens: token counts for billing
//   - latencyMs: total request latency in milliseconds
//   - success: boolean request outcome (determines status mapping)
//
// Returns EventEnvelope for OutboxWriter.Write(), or error if construction fails.
func BuildRequestCompletedEvent(
	tenantID, sessionID string,
	turnNo int,
	requestID, provider, model, status string,
	promptTokens, completionTokens, latencyMs int,
	success bool,
) (EventEnvelope, error) {
	if requestID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildRequestCompletedEvent: request_id is required")
	}
	// Map internal success flag to contract status enum
	eventStatus := "succeeded"
	if !success {
		if status == "timeout" || status == "upstream_timeout" {
			eventStatus = "timeout"
		} else {
			eventStatus = "failed"
		}
	}

	// EventID format: evt-{timestamp}-{requestID-suffix} for uniqueness + time-ordering
	now := time.Now().UTC()
	eventID := fmt.Sprintf("evt-%s-%s", now.Format("20060102150405"), requestID[len(requestID)-min(8, len(requestID)):])

	// Payload matches docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §3
	payload := map[string]any{
		"session_id":      sessionID,
		"turn_no":         turnNo,
		"request_id":      requestID,
		"correlation_id":  requestID, // For Phase 1, correlation_id = request_id
		"idempotency_key": requestID, // For Phase 1, idempotency_key = request_id
		"provider":        provider,
		"model":           model,
		"status":          eventStatus,
		"token_usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
		"latency_ms": latencyMs,
		"body_refs": map[string]string{
			"prompt_ref":   fmt.Sprintf("internal://body/%s/prompt", requestID),
			"response_ref": fmt.Sprintf("internal://body/%s/response", requestID),
		},
	}

	envelope := EventEnvelope{
		EventID:          eventID,
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         tenantID,
		AggregateID:      sessionID,
		AggregateVersion: turnNo,
		OccurredAt:       now,
		Payload:          payload,
	}

	return envelope, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BuildRequestCompletedEventV2 constructs a request.completed.v1 event with separated IDs.
// This version allows correlation_id and idempotency_key to differ from request_id.
//
// Parameters:
//   - correlationID: cross-service tracing ID (typically from client X-Request-Id header)
//     If empty, falls back to requestID
//   - idempotencyKey: idempotency key for deduplication (typically same as requestID)
//
// All other parameters match BuildRequestCompletedEvent.
func BuildRequestCompletedEventV2(
	tenantID, sessionID string,
	turnNo int,
	requestID, correlationID, idempotencyKey string,
	provider, model, status string,
	promptTokens, completionTokens, latencyMs int,
	success bool,
) (EventEnvelope, error) {
	if requestID == "" {
		return EventEnvelope{}, fmt.Errorf("outbox.BuildRequestCompletedEventV2: request_id is required")
	}
	// Fallback: if correlationID is empty, use requestID
	if correlationID == "" {
		correlationID = requestID
	}

	// Map internal success flag to contract status enum
	eventStatus := "succeeded"
	if !success {
		if status == "timeout" || status == "upstream_timeout" {
			eventStatus = "timeout"
		} else {
			eventStatus = "failed"
		}
	}

	// EventID format: evt-{timestamp}-{requestID-suffix} for uniqueness + time-ordering
	now := time.Now().UTC()
	eventID := fmt.Sprintf("evt-%s-%s", now.Format("20060102150405"), requestID[len(requestID)-min(8, len(requestID)):])

	// Payload matches docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §3
	// Phase 2 Enhancement: correlation_id and idempotency_key are now separate
	payload := map[string]any{
		"session_id":      sessionID,
		"turn_no":         turnNo,
		"request_id":      requestID,
		"correlation_id":  correlationID,
		"idempotency_key": idempotencyKey,
		"provider":        provider,
		"model":           model,
		"status":          eventStatus,
		"token_usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
		"latency_ms": latencyMs,
		"body_refs": map[string]string{
			"prompt_ref":   fmt.Sprintf("internal://body/%s/prompt", requestID),
			"response_ref": fmt.Sprintf("internal://body/%s/response", requestID),
		},
	}

	envelope := EventEnvelope{
		EventID:          eventID,
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         tenantID,
		AggregateID:      sessionID,
		AggregateVersion: turnNo,
		OccurredAt:       now,
		Payload:          payload,
	}

	return envelope, nil
}
