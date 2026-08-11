// Package asm provides a small in-memory ASM event receiver for contract tests.
package asm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

const eventPath = "/internal/v1/events"

// Server validates Gateway event requests and stores accepted events in memory.
type Server struct {
	secret string

	mu       sync.RWMutex
	events   []map[string]any
	ids      map[string]struct{}
	versions map[string]int
}

// NewServer creates an ASM mock server using the supplied shared HMAC secret.
func NewServer(secret string) *Server {
	return &Server{
		secret:   secret,
		events:   make([]map[string]any, 0),
		ids:      make(map[string]struct{}),
		versions: make(map[string]int),
	}
}

// ServeHTTP implements POST /internal/v1/events.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != eventPath {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body failed")
		return
	}
	if !verifySignature(body, s.secret, r.Header.Get("X-Event-Signature")) {
		writeError(w, http.StatusUnauthorized, "invalid event signature")
		return
	}

	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "decode event failed")
		return
	}

	tenantHeader := r.Header.Get("X-Tenant-ID")
	tenantID, ok := event["tenant_id"].(string)
	if !ok || tenantID == "" || tenantHeader == "" {
		writeError(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	if tenantHeader != tenantID {
		writeError(w, http.StatusForbidden, "tenant id mismatch")
		return
	}

	eventID, ok := event["event_id"].(string)
	if !ok || eventID == "" {
		writeError(w, http.StatusBadRequest, "event id is required")
		return
	}

	if err := validateEvent(event); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.ids[eventID]; exists {
		writeError(w, http.StatusConflict, "duplicate event")
		return
	}
	aggregateKey := tenantID + "\x00" + event["aggregate_id"].(string)
	aggregateVersion := int(event["aggregate_version"].(float64))
	if previous, exists := s.versions[aggregateKey]; exists && aggregateVersion < previous {
		writeError(w, http.StatusConflict, "stale aggregate version")
		return
	}
	s.ids[eventID] = struct{}{}
	s.events = append(s.events, event)
	s.versions[aggregateKey] = aggregateVersion

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

// Events returns a snapshot of accepted events for assertions in tests.
func (s *Server) Events() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]map[string]any, len(s.events))
	for i, event := range s.events {
		result[i] = cloneMap(event)
	}
	return result
}

func verifySignature(body []byte, secret, provided string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(provided))
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func writeError(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}

func validateEvent(event map[string]any) error {
	if event["event_type"] != "request.completed.v1" {
		return &validationError{"unsupported event type"}
	}
	if event["schema_version"] != float64(1) {
		return &validationError{"unsupported schema version"}
	}
	if _, ok := event["aggregate_id"].(string); !ok {
		return &validationError{"aggregate id is required"}
	}
	if _, ok := event["aggregate_version"].(float64); !ok {
		return &validationError{"aggregate version is required"}
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		return &validationError{"payload is required"}
	}
	for _, field := range []string{
		"session_id", "turn_no", "request_id", "correlation_id", "idempotency_key",
		"provider", "model", "status", "token_usage", "latency_ms", "body_refs",
	} {
		if _, ok := payload[field]; !ok {
			return &validationError{"payload field is required: " + field}
		}
	}
	for _, field := range []string{"user_content", "response_text", "api_key", "routing", "compression"} {
		if _, ok := payload[field]; ok {
			return &validationError{"forbidden payload field: " + field}
		}
	}
	return nil
}

type validationError struct{ message string }

func (e *validationError) Error() string { return e.message }
