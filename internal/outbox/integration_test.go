//go:build integration
// +build integration

package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/test/mock/asm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestE2E_CompleteEventFlow verifies the full event lifecycle:
// 1. Write event to outbox_events
// 2. Dispatcher polls and delivers to ASM
// 3. ASM verifies signature and accepts event
// 4. Event marked as 'sent'
//
// Prerequisites:
//   - PostgreSQL with V357 migration applied
//   - ASM_INTERNAL_ENDPOINT and OUTBOX_HMAC_SECRET configured
func TestE2E_CompleteEventFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	// Setup: ASM mock server
	asmServer := asm.NewServer("test-secret-key")
	testServer := httptest.NewServer(asmServer)
	defer testServer.Close()

	// Setup: Database connection
	db, err := sql.Open("pgx", getTestDatabaseURL())
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cleanup: Delete test events before and after
	cleanup := func() {
		db.ExecContext(ctx, `DELETE FROM outbox_events WHERE tenant_id = 'test-e2e'`)
	}
	cleanup()
	defer cleanup()

	// Step 1: Write event to outbox
	writer := outbox.NewWriter(db)
	envelope := outbox.EventEnvelope{
		EventID:          "evt-e2e-test-001",
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "test-e2e",
		AggregateID:      "session-e2e-001",
		AggregateVersion: 1,
		OccurredAt:       time.Now(),
		Payload: map[string]any{
			"session_id":        "session-e2e-001",
			"turn_no":           1,
			"request_id":        "req-e2e-001",
			"correlation_id":    "req-e2e-001",
			"idempotency_key":   "req-e2e-001",
			"provider":          "openai",
			"model":             "gpt-4",
			"status":            "succeeded",
			"prompt_tokens":     100,
			"completion_tokens": 50,
			"latency_ms":        1234,
		},
	}

	err = writer.Write(ctx, envelope)
	require.NoError(t, err, "write event to outbox should succeed")

	// Verify event written with status='pending'
	var status string
	err = db.QueryRowContext(ctx, `
		SELECT status FROM outbox_events 
		WHERE event_id = $1 AND tenant_id = 'test-e2e'
	`, envelope.EventID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "pending", status, "initial status should be pending")

	// Step 2: Start Dispatcher
	dispatcher := outbox.NewDispatcher(
		db,
		testServer.URL+"/internal/v1/events",
		"test-secret-key",
		100*time.Millisecond, // Fast polling for test
		3,                    // max attempts
	)

	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()

	go func() {
		_ = dispatcher.Start(dispatchCtx)
	}()

	// Step 3: Wait for delivery (poll every 100ms, timeout after 5s)
	var deliveredStatus string
	var attempts int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRowContext(ctx, `
			SELECT status, attempts FROM outbox_events 
			WHERE event_id = $1 AND tenant_id = 'test-e2e'
		`, envelope.EventID).Scan(&deliveredStatus, &attempts)
		if err == nil && deliveredStatus == "sent" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Verify delivery
	assert.Equal(t, "sent", deliveredStatus, "event should be marked as sent")
	assert.GreaterOrEqual(t, attempts, 1, "should have at least 1 attempt")

	// Verify ASM received the event
	receivedEvents := asmServer.GetReceivedEvents()
	require.Len(t, receivedEvents, 1, "ASM should receive exactly 1 event")

	received := receivedEvents[0]
	assert.Equal(t, envelope.EventID, received.EventID)
	assert.Equal(t, envelope.EventType, received.EventType)
	assert.Equal(t, envelope.TenantID, received.TenantID)

	// Verify payload
	payload, ok := received.Payload.(map[string]any)
	require.True(t, ok, "payload should be map")
	assert.Equal(t, "session-e2e-001", payload["session_id"])
	assert.Equal(t, float64(1), payload["turn_no"]) // JSON unmarshal converts to float64
	assert.Equal(t, "req-e2e-001", payload["request_id"])
}

// TestE2E_TransactionAtomicity verifies that request_logs + outbox_events
// are written in the same transaction and rollback together on failure.
func TestE2E_TransactionAtomicity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db, err := sql.Open("pgx", getTestDatabaseURL())
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// Test 1: Both succeed
	tx, err := db.Begin()
	require.NoError(t, err)

	// Simulate request_logs INSERT
	reqID := "req-atomic-001"
	_, err = tx.ExecContext(ctx, `
		INSERT INTO request_logs_hot (
			request_id, tenant_id, success, created_at
		) VALUES ($1, 'test-atomic', true, NOW())
	`, reqID)
	require.NoError(t, err)

	// Write outbox event in same transaction
	envelope := outbox.EventEnvelope{
		EventID:          "evt-atomic-001",
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "test-atomic",
		AggregateID:      "session-atomic-001",
		AggregateVersion: 1,
		OccurredAt:       time.Now(),
		Payload:          map[string]any{"request_id": reqID},
	}
	payloadJSON, _ := json.Marshal(envelope.Payload)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
	`, envelope.EventID, envelope.EventType, envelope.SchemaVersion, envelope.TenantID,
		envelope.AggregateID, envelope.AggregateVersion, envelope.OccurredAt, payloadJSON)
	require.NoError(t, err)

	// Commit
	err = tx.Commit()
	require.NoError(t, err)

	// Verify both records exist
	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs_hot WHERE request_id = $1`, reqID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "request_logs should have 1 row")

	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE event_id = $1`, envelope.EventID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "outbox_events should have 1 row")

	// Cleanup
	db.ExecContext(ctx, `DELETE FROM request_logs_hot WHERE request_id = $1`, reqID)
	db.ExecContext(ctx, `DELETE FROM outbox_events WHERE event_id = $1`, envelope.EventID)

	// Test 2: Rollback on failure
	tx, err = db.Begin()
	require.NoError(t, err)

	reqID2 := "req-atomic-002"
	_, err = tx.ExecContext(ctx, `
		INSERT INTO request_logs_hot (
			request_id, tenant_id, success, created_at
		) VALUES ($1, 'test-atomic', true, NOW())
	`, reqID2)
	require.NoError(t, err)

	// Simulate outbox write failure (duplicate event_id)
	envelope2 := envelope
	envelope2.EventID = envelope.EventID // Duplicate, will fail unique constraint
	payloadJSON2, _ := json.Marshal(envelope2.Payload)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
	`, envelope2.EventID, envelope2.EventType, envelope2.SchemaVersion, envelope2.TenantID,
		envelope2.AggregateID, envelope2.AggregateVersion, envelope2.OccurredAt, payloadJSON2)

	// Should fail due to duplicate event_id, trigger rollback
	if err != nil {
		tx.Rollback()
	}

	// Verify neither record exists
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs_hot WHERE request_id = $1`, reqID2).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "request_logs should have 0 rows after rollback")

	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE event_id = $1 AND payload::jsonb @> '{"request_id": "req-atomic-002"}'`, envelope2.EventID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "outbox_events should still have the first event, not the rolled-back one")
}

// getTestDatabaseURL returns the test database connection string.
// Reads from environment variable TEST_DATABASE_URL or uses default.
func getTestDatabaseURL() string {
	// TODO: Read from env or use default test DB
	return "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable"
}
