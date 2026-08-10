package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestWriter_Write tests the basic Write operation.
//
// This test requires a PostgreSQL database with the outbox_events table.
// Run: go test -v ./internal/outbox/... -run TestWriter_Write
func TestWriter_Write(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// TODO: Set up test database connection
	// For now, skip until database is available
	t.Skip("TODO: set up test database connection")

	db, err := sql.Open("postgres", "postgres://localhost/llm_gateway_test?sslmode=disable")
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer tx.Rollback()

	writer := NewWriter(tx)

	env := EventEnvelope{
		EventID:          "evt-test-001",
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
func TestWriter_Write_MissingFields(t *testing.T) {
	// No database required for validation tests
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
			if tt.wantErr != "" && !containsString(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestWriter_Write_Defaults tests default value assignment.
func TestWriter_Write_Defaults(t *testing.T) {
	t.Skip("TODO: implement after database setup")
}

// TestWriter_WriteBatch tests batch insertion.
func TestWriter_WriteBatch(t *testing.T) {
	t.Skip("TODO: implement after database setup")
}

// TestWriter_Write_DuplicateEventID tests idempotency violation.
func TestWriter_Write_DuplicateEventID(t *testing.T) {
	t.Skip("TODO: implement after database setup - should fail with unique constraint violation")
}

// TestWriter_Write_PayloadSerialization tests payload JSON serialization.
func TestWriter_Write_PayloadSerialization(t *testing.T) {
	// Test payload serialization without database
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

	// Verify we can deserialize
	var decoded map[string]any
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// Spot check
	if decoded["string"] != "value" {
		t.Errorf("string field mismatch")
	}
	if decoded["number"].(float64) != 42 {
		t.Errorf("number field mismatch")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			len(s) > len(substr)+1 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
