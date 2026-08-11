package outbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestDispatcher_Config tests dispatcher configuration defaults.
func TestDispatcher_Config(t *testing.T) {
	cfg := DispatcherConfig{
		DB:          &sql.DB{},
		ASMEndpoint: "http://asm:8080/internal/v1/events",
		HMACSecret:  "test-secret",
	}

	d := NewDispatcher(cfg)

	// Check defaults
	if d.pollInterval != 5*time.Second {
		t.Errorf("pollInterval = %v, want 5s", d.pollInterval)
	}
	if d.maxAttempts != 5 {
		t.Errorf("maxAttempts = %d, want 5", d.maxAttempts)
	}
	if d.logger == nil {
		t.Error("logger should default to slog.Default()")
	}
}

// TestDispatcher_ConfigCustom tests custom configuration.
func TestDispatcher_ConfigCustom(t *testing.T) {
	logger := slog.Default()
	cfg := DispatcherConfig{
		DB:           &sql.DB{},
		ASMEndpoint:  "http://custom:9090/events",
		HMACSecret:   "custom-secret",
		PollInterval: 10 * time.Second,
		MaxAttempts:  3,
		Logger:       logger,
	}

	d := NewDispatcher(cfg)

	if d.pollInterval != 10*time.Second {
		t.Errorf("pollInterval = %v, want 10s", d.pollInterval)
	}
	if d.maxAttempts != 3 {
		t.Errorf("maxAttempts = %d, want 3", d.maxAttempts)
	}
	if d.logger != logger {
		t.Error("logger should be custom logger")
	}
	if d.asmEndpoint != "http://custom:9090/events" {
		t.Errorf("asmEndpoint = %s", d.asmEndpoint)
	}
}

// TestDispatcher_Start tests that Start respects context cancellation.
func TestDispatcher_Start(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cfg := DispatcherConfig{
		// A DB whose connector always errors. The original fixture used a
		// zero-value &sql.DB{}, but database/sql panics (nil connector) on the
		// first QueryContext of such a value, which crashes the test instead
		// of exercising Start's "keep polling despite query errors" path.
		// errorConnector makes QueryContext return a real error, matching the
		// documented intent of this test.
		DB:           sql.OpenDB(errorConnector{}),
		ASMEndpoint:  "http://localhost:9999/events",
		HMACSecret:   "test",
		PollInterval: 100 * time.Millisecond,
	}

	d := NewDispatcher(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Start should block until ctx is cancelled
	err := d.Start(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Start() error = %v, want context.DeadlineExceeded", err)
	}
}

// TestDispatcher_EnvelopeMarshaling tests that dispatch correctly marshals EventEnvelope.
func TestDispatcher_EnvelopeMarshaling(t *testing.T) {
	env := EventEnvelope{
		EventID:          "evt-001",
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "tenant-001",
		AggregateID:      "req-001",
		AggregateVersion: 1,
		OccurredAt:       time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
		Payload: map[string]any{
			"session_id": "session-001",
			"status":     "succeeded",
		},
	}

	// Marshal envelope as Dispatcher would
	envelopeJSON, err := json.Marshal(struct {
		EventID          string         `json:"event_id"`
		EventType        string         `json:"event_type"`
		SchemaVersion    int            `json:"schema_version"`
		TenantID         string         `json:"tenant_id"`
		AggregateID      string         `json:"aggregate_id"`
		AggregateVersion int            `json:"aggregate_version"`
		OccurredAt       time.Time      `json:"occurred_at"`
		Payload          map[string]any `json:"payload"`
	}{
		EventID:          env.EventID,
		EventType:        env.EventType,
		SchemaVersion:    env.SchemaVersion,
		TenantID:         env.TenantID,
		AggregateID:      env.AggregateID,
		AggregateVersion: env.AggregateVersion,
		OccurredAt:       env.OccurredAt,
		Payload:          env.Payload,
	})

	if err != nil {
		t.Fatalf("marshal envelope failed: %v", err)
	}

	// Verify JSON structure
	var parsed map[string]any
	if err := json.Unmarshal(envelopeJSON, &parsed); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}

	if parsed["event_id"] != "evt-001" {
		t.Errorf("event_id = %v", parsed["event_id"])
	}
	if parsed["event_type"] != "request.completed.v1" {
		t.Errorf("event_type = %v", parsed["event_type"])
	}
	if parsed["tenant_id"] != "tenant-001" {
		t.Errorf("tenant_id = %v", parsed["tenant_id"])
	}

	payload, ok := parsed["payload"].(map[string]any)
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["session_id"] != "session-001" {
		t.Errorf("payload.session_id = %v", payload["session_id"])
	}
}

// TestDispatcher_SignatureGeneration tests that dispatcher generates valid signatures.
func TestDispatcher_SignatureGeneration(t *testing.T) {
	secret := "test-hmac-secret"
	data := []byte(`{"event_id":"evt-001","tenant_id":"tenant-001"}`)

	// Compute signature as Dispatcher would
	signature := computeHMAC(data, secret)

	// Signature should be 64-char hex
	if len(signature) != 64 {
		t.Errorf("signature length = %d, want 64", len(signature))
	}

	// ASM should be able to verify
	if !VerifyHMAC(data, secret, signature) {
		t.Error("ASM would reject this signature")
	}
}

// errorConnector is a database/sql/driver.Connector whose Connect always fails.
// It backs a *sql.DB that returns query errors instead of panicking, so tests
// can exercise error-handling paths (e.g. the dispatcher's "keep polling"
// resilience) without a live database.
type errorConnector struct{}

func (errorConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("test: database connector unavailable")
}
func (errorConnector) Driver() driver.Driver { return nil }
