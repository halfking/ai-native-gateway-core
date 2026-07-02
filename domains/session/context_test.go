package session

import (
	"net/http"
	"testing"
	"time"
)

func TestNewSessionContext(t *testing.T) {
	req, err := http.NewRequest("POST", "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "cursor/1.0")
	req.Header.Set("X-Request-Id", "test_request")

	sc := NewSessionContext(req)

	if sc.State != StateInitial {
		t.Errorf("Expected initial state %s, got %s", StateInitial, sc.State)
	}

	if sc.ClientMethod != "POST" {
		t.Errorf("Expected method POST, got %s", sc.ClientMethod)
	}

	if sc.ClientPath != "/v1/chat/completions" {
		t.Errorf("Expected path /v1/chat/completions, got %s", sc.ClientPath)
	}

	if sc.Metadata == nil {
		t.Error("Expected Metadata to be initialized")
	}

	if sc.ClientHeaders == nil {
		t.Error("Expected ClientHeaders to be initialized")
	}

	if ua := sc.ClientHeaders.Get("User-Agent"); ua != "cursor/1.0" {
		t.Errorf("Expected User-Agent cursor/1.0, got %s", ua)
	}
}

func TestSessionContext_Duration(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)
	sc.CreatedAt = time.Now().Add(-1 * time.Second)

	duration := sc.Duration()
	if duration < time.Second {
		t.Errorf("Expected duration >= 1s, got %v", duration)
	}

	// Test with ClientRespondedAt set
	sc.ClientRespondedAt = sc.CreatedAt.Add(500 * time.Millisecond)
	duration = sc.Duration()
	if duration != 500*time.Millisecond {
		t.Errorf("Expected duration 500ms, got %v", duration)
	}
}

func TestSessionContext_UpstreamDuration(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	// No times set
	if d := sc.UpstreamDuration(); d != 0 {
		t.Errorf("Expected upstream duration 0, got %v", d)
	}

	// Set upstream times
	sc.LLMSentAt = time.Now()
	sc.LLMReceivedAt = sc.LLMSentAt.Add(200 * time.Millisecond)

	duration := sc.UpstreamDuration()
	if duration != 200*time.Millisecond {
		t.Errorf("Expected upstream duration 200ms, got %v", duration)
	}
}

func TestSessionContext_TransformDuration(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	now := time.Now()
	sc.CreatedAt = now
	sc.LLMSentAt = now.Add(100 * time.Millisecond)
	sc.LLMReceivedAt = now.Add(300 * time.Millisecond)
	sc.ClientRespondedAt = now.Add(400 * time.Millisecond)

	// Total: 400ms, Upstream: 200ms, Transform: 200ms
	transformDuration := sc.TransformDuration()
	if transformDuration != 200*time.Millisecond {
		t.Errorf("Expected transform duration 200ms, got %v", transformDuration)
	}
}

func TestSessionContext_Metadata(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	// Test SetMetadata and GetMetadata
	sc.SetMetadata("key1", "value1")
	sc.SetMetadata("key2", 123)

	val, ok := sc.GetMetadata("key1")
	if !ok {
		t.Error("Expected key1 to exist")
	}
	if val != "value1" {
		t.Errorf("Expected value1, got %v", val)
	}

	val, ok = sc.GetMetadata("key2")
	if !ok {
		t.Error("Expected key2 to exist")
	}
	if val != 123 {
		t.Errorf("Expected 123, got %v", val)
	}

	_, ok = sc.GetMetadata("nonexistent")
	if ok {
		t.Error("Expected nonexistent key to return false")
	}
}

func TestSessionContext_GetStateHistory(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	// No transitions yet
	history := sc.GetStateHistory()
	if len(history) != 1 || history[0] != StateInitial.String() {
		t.Errorf("Expected initial state only, got %v", history)
	}

	// Add transitions
	sc.Transitions = []StateTransition{
		{From: StateInitial, To: StateReceivingFromClient, Timestamp: time.Now()},
		{From: StateReceivingFromClient, To: StatePendingToLLM, Timestamp: time.Now()},
		{From: StatePendingToLLM, To: StateCompleted, Timestamp: time.Now()},
	}

	history = sc.GetStateHistory()
	expected := []string{
		StateReceivingFromClient.String(),
		StatePendingToLLM.String(),
		StateCompleted.String(),
	}

	if len(history) != len(expected) {
		t.Errorf("Expected %d states, got %d", len(expected), len(history))
	}

	for i, state := range history {
		if state != expected[i] {
			t.Errorf("Expected state %s at index %d, got %s", expected[i], i, state)
		}
	}
}

func TestSessionContext_IsError(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	if sc.IsError() {
		t.Error("Expected IsError to return false initially")
	}

	sc.State = StateError
	if !sc.IsError() {
		t.Error("Expected IsError to return true when state is ERROR")
	}
}

func TestSessionContext_IsCompleted(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	if sc.IsCompleted() {
		t.Error("Expected IsCompleted to return false initially")
	}

	sc.State = StateCompleted
	if !sc.IsCompleted() {
		t.Error("Expected IsCompleted to return true when state is COMPLETED")
	}
}

func TestSessionContext_MarkError(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	testErr := http.ErrAbortHandler
	sc.MarkError(testErr)

	if sc.State != StateError {
		t.Errorf("Expected state ERROR, got %s", sc.State)
	}

	if sc.Error != testErr {
		t.Errorf("Expected error %v, got %v", testErr, sc.Error)
	}
}

func TestSessionContext_SetMetadata_NilMap(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)
	sc.Metadata = nil

	// Should not panic
	sc.SetMetadata("key", "value")

	if sc.Metadata == nil {
		t.Error("Expected Metadata to be initialized")
	}

	val, ok := sc.GetMetadata("key")
	if !ok || val != "value" {
		t.Error("Expected key to be set")
	}
}

func TestSessionContext_GetMetadata_NilMap(t *testing.T) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)
	sc.Metadata = nil

	val, ok := sc.GetMetadata("key")
	if ok {
		t.Error("Expected GetMetadata to return false for nil map")
	}
	if val != nil {
		t.Error("Expected GetMetadata to return nil value for nil map")
	}
}

func BenchmarkNewSessionContext(b *testing.B) {
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "test")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewSessionContext(req)
	}
}

func BenchmarkSessionContext_SetMetadata(b *testing.B) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc.SetMetadata("key", "value")
	}
}

func BenchmarkSessionContext_GetMetadata(b *testing.B) {
	req, _ := http.NewRequest("GET", "/", nil)
	sc := NewSessionContext(req)
	sc.SetMetadata("key", "value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = sc.GetMetadata("key")
	}
}
