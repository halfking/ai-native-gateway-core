package asm

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testSecret = "asm-test-secret"

func TestServerAcceptsSignedEvent(t *testing.T) {
	server := NewServer(testSecret)
	response := postEvent(t, server, validEvent(), testSecret, "tenant-1")
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if got := server.Events(); len(got) != 1 {
		t.Fatalf("stored events = %d, want 1", len(got))
	}
}

func TestServerRejectsInvalidSignature(t *testing.T) {
	server := NewServer(testSecret)
	response := postEvent(t, server, validEvent(), "wrong-secret", "tenant-1")
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestServerRejectsTenantMismatch(t *testing.T) {
	server := NewServer(testSecret)
	response := postEvent(t, server, validEvent(), testSecret, "tenant-2")
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestServerRejectsInvalidJSON(t *testing.T) {
	server := NewServer(testSecret)
	body := []byte("not-json")
	request := httptest.NewRequest(http.MethodPost, eventPath, bytes.NewReader(body))
	request.Header.Set("X-Tenant-ID", "tenant-1")
	request.Header.Set("X-Event-Signature", sign(body, testSecret))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestServerRejectsDuplicateEvent(t *testing.T) {
	server := NewServer(testSecret)
	event := validEvent()
	first := postEvent(t, server, event, testSecret, "tenant-1")
	first.Body.Close()
	second := postEvent(t, server, event, testSecret, "tenant-1")
	defer second.Body.Close()

	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", second.StatusCode, http.StatusConflict)
	}
}

func TestServerRejectsStaleAggregateVersion(t *testing.T) {
	server := NewServer(testSecret)
	firstEvent := validEvent()
	firstEvent["aggregate_version"] = 3
	first := postEvent(t, server, firstEvent, testSecret, "tenant-1")
	first.Body.Close()

	staleEvent := validEvent()
	staleEvent["event_id"] = "event-2"
	staleEvent["aggregate_version"] = 2
	second := postEvent(t, server, staleEvent, testSecret, "tenant-1")
	defer second.Body.Close()

	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", second.StatusCode, http.StatusConflict)
	}
}

func TestServerRejectsForbiddenPayloadField(t *testing.T) {
	server := NewServer(testSecret)
	event := validEvent()
	event["payload"].(map[string]any)["api_key"] = "not-a-real-key"
	response := postEvent(t, server, event, testSecret, "tenant-1")
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnprocessableEntity)
	}
}

func postEvent(t *testing.T, server http.Handler, event map[string]any, secret, tenant string) *http.Response {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, eventPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Tenant-ID", tenant)
	request.Header.Set("X-Event-Signature", sign(body, secret))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response.Result()
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func validEvent() map[string]any {
	return map[string]any{
		"event_id":          "event-1",
		"event_type":        "request.completed.v1",
		"schema_version":    1,
		"tenant_id":         "tenant-1",
		"aggregate_id":      "session-1",
		"aggregate_version": 1,
		"occurred_at":       "2026-08-11T10:30:00Z",
		"payload": map[string]any{
			"session_id":      "session-1",
			"turn_no":         1,
			"request_id":      "request-1",
			"correlation_id":  "correlation-1",
			"idempotency_key": "idempotency-1",
			"provider":        "anthropic",
			"model":           "claude-test",
			"status":          "succeeded",
			"token_usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
			"latency_ms": 1,
			"body_refs": map[string]any{
				"prompt_ref":   "internal://prompt",
				"response_ref": "internal://response",
			},
		},
	}
}
