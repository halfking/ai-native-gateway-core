package main

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestExtractRequestCompletedPayload(t *testing.T) {
	startTime := time.Now().Add(-100 * time.Millisecond)

	env := &domain.PipelineRequest{
		SessionID:  "session-test-123",
		StatusCode: 200,
		Metadata: map[string]any{
			"model":    "gpt-4",
			"turn_no":  2,
			"provider": "openai",
		},
		UpstreamResponse: []byte(`{"usage":{"prompt_tokens":50,"completion_tokens":100,"total_tokens":150}}`),
		SelectedProvider: &domain.PipelineProvider{
			Name: "openai-gpt",
		},
	}

	payload := extractRequestCompletedPayload(env, "req-test-001", startTime)

	// Verify all 11 required fields are present
	requiredFields := []string{
		"session_id", "turn_no", "request_id", "correlation_id",
		"idempotency_key", "provider", "model", "status",
		"token_usage", "latency_ms", "body_refs",
	}

	for _, field := range requiredFields {
		if _, ok := payload[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}

	// Verify field values
	if payload["session_id"] != "session-test-123" {
		t.Errorf("session_id = %v, want session-test-123", payload["session_id"])
	}
	if payload["request_id"] != "req-test-001" {
		t.Errorf("request_id = %v, want req-test-001", payload["request_id"])
	}
	if payload["turn_no"] != 2 {
		t.Errorf("turn_no = %v, want 2", payload["turn_no"])
	}
	if payload["provider"] != "openai-gpt" {
		t.Errorf("provider = %v, want openai-gpt", payload["provider"])
	}
	if payload["model"] != "gpt-4" {
		t.Errorf("model = %v, want gpt-4", payload["model"])
	}
	if payload["status"] != "succeeded" {
		t.Errorf("status = %v, want succeeded", payload["status"])
	}

	// Verify correlation_id format
	if payload["correlation_id"] != "req-test-001-corr" {
		t.Errorf("correlation_id = %v, want req-test-001-corr", payload["correlation_id"])
	}

	// Verify idempotency_key format
	if payload["idempotency_key"] != "req-test-001-idem" {
		t.Errorf("idempotency_key = %v, want req-test-001-idem", payload["idempotency_key"])
	}

	// Verify latency_ms is reasonable (>= 100ms)
	latencyMs, ok := payload["latency_ms"].(int)
	if !ok {
		t.Fatalf("latency_ms is not int: %T", payload["latency_ms"])
	}
	if latencyMs < 100 {
		t.Errorf("latency_ms = %d, want >= 100", latencyMs)
	}

	// Verify token_usage structure
	tokenUsage, ok := payload["token_usage"].(map[string]int)
	if !ok {
		t.Fatalf("token_usage is not map[string]int: %T", payload["token_usage"])
	}
	if tokenUsage["total_tokens"] != 150 {
		t.Errorf("total_tokens = %d, want 150", tokenUsage["total_tokens"])
	}

	// Verify body_refs structure
	bodyRefs, ok := payload["body_refs"].(map[string]string)
	if !ok {
		t.Fatalf("body_refs is not map[string]string: %T", payload["body_refs"])
	}
	if bodyRefs["prompt_ref"] != "internal://body/req-test-001/prompt" {
		t.Errorf("prompt_ref = %v", bodyRefs["prompt_ref"])
	}
}

func TestHttpCodeToStatus(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{200, "succeeded"},
		{201, "succeeded"},
		{299, "succeeded"},
		{400, "failed"},
		{404, "failed"},
		{408, "timeout"},
		{500, "failed"},
		{504, "timeout"},
	}

	for _, tt := range tests {
		got := httpCodeToStatus(tt.code)
		if got != tt.want {
			t.Errorf("httpCodeToStatus(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestExtractTokenUsage_FromResponse(t *testing.T) {
	env := &domain.PipelineRequest{
		UpstreamResponse: []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
	}

	usage := extractTokenUsage(env)

	if usage["prompt_tokens"] != 10 {
		t.Errorf("prompt_tokens = %d, want 10", usage["prompt_tokens"])
	}
	if usage["completion_tokens"] != 20 {
		t.Errorf("completion_tokens = %d, want 20", usage["completion_tokens"])
	}
	if usage["total_tokens"] != 30 {
		t.Errorf("total_tokens = %d, want 30", usage["total_tokens"])
	}
}

func TestExtractTokenUsage_Default(t *testing.T) {
	env := &domain.PipelineRequest{}

	usage := extractTokenUsage(env)

	if usage["total_tokens"] != 0 {
		t.Errorf("total_tokens = %d, want 0", usage["total_tokens"])
	}
}

func TestExtractProvider_Priority(t *testing.T) {
	// Priority 1: SelectedProvider
	env := &domain.PipelineRequest{
		SelectedProvider: &domain.PipelineProvider{Name: "anthropic-claude"},
		Metadata:         map[string]any{"provider": "openai"},
	}
	if got := extractProvider(env); got != "anthropic-claude" {
		t.Errorf("extractProvider with SelectedProvider = %q, want anthropic-claude", got)
	}

	// Priority 2: Metadata
	env = &domain.PipelineRequest{
		Metadata: map[string]any{"provider": "openai"},
	}
	if got := extractProvider(env); got != "openai" {
		t.Errorf("extractProvider with Metadata = %q, want openai", got)
	}

	// Default: unknown
	env = &domain.PipelineRequest{}
	if got := extractProvider(env); got != "unknown" {
		t.Errorf("extractProvider with no data = %q, want unknown", got)
	}
}

func TestExtractTurnNo_Default(t *testing.T) {
	env := &domain.PipelineRequest{}

	turnNo := extractTurnNo(env)

	if turnNo != 1 {
		t.Errorf("extractTurnNo default = %d, want 1", turnNo)
	}
}

func TestExtractRequestCompletedPayload_NoUserContent(t *testing.T) {
	env := &domain.PipelineRequest{
		SessionID:  "session-test",
		StatusCode: 200,
		Metadata: map[string]any{
			"model": "gpt-4",
		},
	}

	payload := extractRequestCompletedPayload(env, "req-001", time.Now())

	// Verify user_content is NOT present (contract violation)
	if _, ok := payload["user_content"]; ok {
		t.Error("payload contains user_content (contract violation)")
	}

	// Verify user_message is NOT present
	if _, ok := payload["user_message"]; ok {
		t.Error("payload contains user_message (should not be in contract)")
	}

	// Verify prompt is NOT present
	if _, ok := payload["prompt"]; ok {
		t.Error("payload contains prompt (should not be in contract)")
	}
}
