package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestWriter_Write tests the basic Write operation against a real PostgreSQL database.
//
// This test is skipped by default. To run it:
//  1. Start a PostgreSQL test instance
//  2. Apply the outbox_events table migration (V357)
//  3. Run: go test -v ./internal/outbox/... -run TestWriter_Write
func TestWriter_Write(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db, err := sql.Open("postgres", "postgres://localhost/llm_gateway_test?sslmode=disable")
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("test database not available: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}
	defer tx.Rollback()

	writer := NewWriter(tx)

	env := EventEnvelope{
		EventID:          "evt-test-" + time.Now().Format("20060102150405.000"),
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "tenant-test",
		AggregateID:      "session-test",
		AggregateVersion: 1,
		OccurredAt:       time.Now(),
		Payload: map[string]any{
			"session_id": "session-test",
			"turn_no":    1,
			"request_id": "req-test-001",
			"status":     "succeeded",
		},
	}

	if err := writer.Write(ctx, env); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Verify inserted
	var count int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE event_id = $1", env.EventID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}

	// Verify status
	var status string
	err = tx.QueryRowContext(ctx, "SELECT status FROM outbox_events WHERE event_id = $1", env.EventID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}
	if status != "pending" {
		t.Errorf("expected status=pending, got %s", status)
	}
}

// TestWriter_Write_MissingFields tests validation of required fields.
//
// These tests do not require a database connection — they validate the
// pre-DB validation logic in Write().
func TestWriter_Write_MissingFields(t *testing.T) {
	writer := &Writer{tx: nil}
	ctx := context.Background()

	tests := []struct {
		name    string
		env     EventEnvelope
		wantErr string
	}{
		{
			name: "missing event_id",
			env: EventEnvelope{
				EventType:        "test.v1",
				TenantID:         "tenant",
				AggregateID:      "agg",
				AggregateVersion: 1,
			},
			wantErr: "event_id is required",
		},
		{
			name: "missing event_type",
			env: EventEnvelope{
				EventID:          "evt-001",
				TenantID:         "tenant",
				AggregateID:      "agg",
				AggregateVersion: 1,
			},
			wantErr: "event_type is required",
		},
		{
			name: "missing tenant_id",
			env: EventEnvelope{
				EventID:          "evt-001",
				EventType:        "test.v1",
				AggregateID:      "agg",
				AggregateVersion: 1,
			},
			wantErr: "tenant_id is required",
		},
		{
			name: "missing aggregate_id",
			env: EventEnvelope{
				EventID:          "evt-001",
				EventType:        "test.v1",
				TenantID:         "tenant",
				AggregateVersion: 1,
			},
			wantErr: "aggregate_id is required",
		},
		{
			name: "zero aggregate_version",
			env: EventEnvelope{
				EventID:          "evt-001",
				EventType:        "test.v1",
				TenantID:         "tenant",
				AggregateID:      "agg",
				AggregateVersion: 0,
			},
			wantErr: "aggregate_version must be > 0",
		},
		{
			name: "negative aggregate_version",
			env: EventEnvelope{
				EventID:          "evt-001",
				EventType:        "test.v1",
				TenantID:         "tenant",
				AggregateID:      "agg",
				AggregateVersion: -1,
			},
			wantErr: "aggregate_version must be > 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writer.Write(ctx, tt.env)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestWriter_Write_Defaults tests default value assignment (schema_version, occurred_at).
func TestWriter_Write_Defaults(t *testing.T) {
	// Defaults are applied via in-place modification of the struct before INSERT.
	// Since we cannot run a real INSERT without a DB, validate the validation-only path:
	// passing schema_version=0 and zero occurred_at should NOT fail with "required" errors
	// because the defaults are intended to fill those.
	writer := &Writer{tx: nil}
	env := EventEnvelope{
		EventID:          "evt-defaults",
		EventType:        "test.v1",
		TenantID:         "tenant",
		AggregateID:      "agg",
		AggregateVersion: 1,
		// SchemaVersion: 0, OccurredAt: zero — defaults should apply
	}

	err := writer.Write(context.Background(), env)
	// Without a DB tx this will fail at INSERT step, but should NOT fail at validation.
	if err != nil && strings.Contains(err.Error(), "is required") {
		t.Errorf("defaults should bypass required-field validation, got: %v", err)
	}
}

// TestWriter_WriteBatch_Empty tests that an empty batch is a no-op.
func TestWriter_WriteBatch_Empty(t *testing.T) {
	writer := &Writer{tx: nil}
	if err := writer.WriteBatch(context.Background(), nil); err != nil {
		t.Errorf("empty batch should not error, got: %v", err)
	}
	if err := writer.WriteBatch(context.Background(), []EventEnvelope{}); err != nil {
		t.Errorf("empty batch should not error, got: %v", err)
	}
}

// TestWriter_Write_PayloadSerialization tests payload JSON serialization.
func TestWriter_Write_PayloadSerialization(t *testing.T) {
	env := EventEnvelope{
		EventID:          "evt-test",
		EventType:        "test.v1",
		TenantID:         "tenant",
		AggregateID:      "agg",
		AggregateVersion: 1,
		Payload: map[string]any{
			"string": "value",
			"number": 42,
			"bool":   true,
			"nested": map[string]any{
				"key": "value",
			},
			"array": []any{1, 2, 3},
		},
	}

	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded["string"] != "value" {
		t.Errorf("string field mismatch")
	}
	if decoded["number"].(float64) != 42 {
		t.Errorf("number field mismatch")
	}
}

// TestWriter_MarshalPayloadError tests that a non-marshalable payload produces a clear error.
//
// Uses an unmarshalable value (channel) to trigger a JSON marshal error.
func TestWriter_MarshalPayloadError(t *testing.T) {
	writer := &Writer{tx: nil}
	env := EventEnvelope{
		EventID:          "evt-bad",
		EventType:        "test.v1",
		TenantID:         "tenant",
		AggregateID:      "agg",
		AggregateVersion: 1,
		Payload: map[string]any{
			"channel": make(chan int), // channels are not JSON-serializable
		},
	}

	err := writer.Write(context.Background(), env)
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "marshal payload") {
		t.Errorf("error = %v, want substring %q", err, "marshal payload")
	}
}
