package outbox

import (
	"testing"
	"time"
)

func TestBuildRequestCompletedEvent(t *testing.T) {
	envelope, err := BuildRequestCompletedEvent(
		"tenant-123",
		"session-abc",
		1,
		"request-001",
		"anthropic",
		"claude-3-5-sonnet-20241022",
		"succeeded",
		150,
		80,
		1250,
		true,
	)

	if err != nil {
		t.Fatalf("BuildRequestCompletedEvent failed: %v", err)
	}

	// Validate envelope structure
	if envelope.EventType != "request.completed.v1" {
		t.Errorf("event_type = %q, want %q", envelope.EventType, "request.completed.v1")
	}
	if envelope.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", envelope.SchemaVersion)
	}
	if envelope.TenantID != "tenant-123" {
		t.Errorf("tenant_id = %q, want %q", envelope.TenantID, "tenant-123")
	}
	if envelope.AggregateID != "session-abc" {
		t.Errorf("aggregate_id = %q, want %q", envelope.AggregateID, "session-abc")
	}
	if envelope.AggregateVersion != 1 {
		t.Errorf("aggregate_version = %d, want 1", envelope.AggregateVersion)
	}

	// Validate occurred_at is recent
	if time.Since(envelope.OccurredAt) > 5*time.Second {
		t.Errorf("occurred_at too old: %v", envelope.OccurredAt)
	}

	// Validate payload structure
	payload := envelope.Payload

	// Validate required 11 fields
	requiredFields := []string{
		"session_id", "turn_no", "request_id", "correlation_id", "idempotency_key",
		"provider", "model", "status", "token_usage", "latency_ms", "body_refs",
	}
	for _, field := range requiredFields {
		if _, exists := payload[field]; !exists {
			t.Errorf("payload missing required field: %s", field)
		}
	}

	// Validate specific payload values
	if payload["session_id"] != "session-abc" {
		t.Errorf("payload.session_id = %v, want %q", payload["session_id"], "session-abc")
	}
	if payload["turn_no"] != 1 {
		t.Errorf("payload.turn_no = %v, want 1", payload["turn_no"])
	}
	if payload["status"] != "succeeded" {
		t.Errorf("payload.status = %v, want %q", payload["status"], "succeeded")
	}

	// Validate token_usage structure
	tokenUsage, ok := payload["token_usage"].(map[string]int)
	if !ok {
		t.Fatalf("token_usage is not map[string]int: %T", payload["token_usage"])
	}
	if tokenUsage["prompt_tokens"] != 150 {
		t.Errorf("token_usage.prompt_tokens = %d, want 150", tokenUsage["prompt_tokens"])
	}
	if tokenUsage["completion_tokens"] != 80 {
		t.Errorf("token_usage.completion_tokens = %d, want 80", tokenUsage["completion_tokens"])
	}
	if tokenUsage["total_tokens"] != 230 {
		t.Errorf("token_usage.total_tokens = %d, want 230", tokenUsage["total_tokens"])
	}

	// Validate body_refs structure
	bodyRefs, ok := payload["body_refs"].(map[string]string)
	if !ok {
		t.Fatalf("body_refs is not map[string]string: %T", payload["body_refs"])
	}
	expectedPromptRef := "internal://body/request-001/prompt"
	if bodyRefs["prompt_ref"] != expectedPromptRef {
		t.Errorf("body_refs.prompt_ref = %q, want %q", bodyRefs["prompt_ref"], expectedPromptRef)
	}
}

func TestBuildRequestCompletedEvent_StatusMapping(t *testing.T) {
	tests := []struct {
		name           string
		requestStatus  string
		successFlag    bool
		expectedStatus string
	}{
		{"succeeded", "succeeded", true, "succeeded"},
		{"failed", "failed", false, "failed"},
		{"timeout", "timeout", false, "timeout"},
		{"upstream_timeout", "upstream_timeout", false, "timeout"},
		{"generic_failure", "error", false, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope, err := BuildRequestCompletedEvent(
				"tenant-1", "session-1", 1, "request-1",
				"anthropic", "claude-test", tt.requestStatus,
				100, 50, 1000, tt.successFlag,
			)
			if err != nil {
				t.Fatalf("BuildRequestCompletedEvent failed: %v", err)
			}

			payload := envelope.Payload
			if payload["status"] != tt.expectedStatus {
				t.Errorf("status = %v, want %q", payload["status"], tt.expectedStatus)
			}
		})
	}
}

func TestBuildRequestCompletedEvent_EventIDUniqueness(t *testing.T) {
	envelope1, _ := BuildRequestCompletedEvent(
		"tenant-1", "session-1", 1, "request-001",
		"anthropic", "claude", "succeeded", 100, 50, 1000, true,
	)
	time.Sleep(10 * time.Millisecond)
	envelope2, _ := BuildRequestCompletedEvent(
		"tenant-1", "session-1", 2, "request-002",
		"anthropic", "claude", "succeeded", 100, 50, 1000, true,
	)

	if envelope1.EventID == envelope2.EventID {
		t.Errorf("event_id not unique: both are %q", envelope1.EventID)
	}

	// Validate event_id format: evt-{timestamp}-{suffix}
	if len(envelope1.EventID) < 20 {
		t.Errorf("event_id too short: %q", envelope1.EventID)
	}
	if envelope1.EventID[:4] != "evt-" {
		t.Errorf("event_id prefix = %q, want %q", envelope1.EventID[:4], "evt-")
	}
}
