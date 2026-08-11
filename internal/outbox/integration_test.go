//go:build integration
// +build integration

package outbox_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/test/mock/asm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/lib/pq"
)

// TestE2E_CompleteEventFlow verifies the full event lifecycle:
//  1. Write event to outbox_events inside a business transaction.
//  2. Dispatcher polls and delivers to ASM.
//  3. ASM verifies signature and accepts the event.
//  4. Event is marked 'sent'.
//
// Prerequisites:
//   - PostgreSQL with the V357 outbox_events migration applied.
//   - TEST_DATABASE_URL set.
func TestE2E_CompleteEventFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	asmServer := asm.NewServer("test-secret-key")
	testServer := httptest.NewServer(asmServer)
	defer testServer.Close()

	db := mustOpenTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tenantID = "test-e2e"
	cleanup := func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM outbox_events WHERE tenant_id = $1`, tenantID)
	}
	cleanup()
	defer cleanup()

	// Step 1: write the event inside a business transaction. The outbox
	// contract requires the event INSERT to share the business tx so the
	// fact and its delivery record commit (or roll back) together.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)

	writer := outbox.NewWriter(tx)
	envelope := validEnvelope(tenantID, "evt-e2e-test-001", "session-e2e-001", 1)
	require.NoError(t, writer.Write(ctx, envelope))
	require.NoError(t, tx.Commit())

	// Verify the row landed as pending.
	var status string
	err = db.QueryRowContext(ctx, `SELECT status FROM outbox_events WHERE event_id = $1`, envelope.EventID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "pending", status)

	// Step 2: start the dispatcher (current API: DispatcherConfig, not positional args).
	dispatcher := outbox.NewDispatcher(outbox.DispatcherConfig{
		DB:           db,
		ASMEndpoint:  testServer.URL + "/internal/v1/events",
		HMACSecret:   "test-secret-key",
		PollInterval: 100 * time.Millisecond,
		MaxAttempts:  3,
	})

	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	go func() { _ = dispatcher.Start(dispatchCtx) }()

	// Step 3: wait for delivery.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var delivered string
		if err := db.QueryRowContext(ctx, `SELECT status FROM outbox_events WHERE event_id = $1`, envelope.EventID).Scan(&delivered); err == nil && delivered == "sent" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	var delivered string
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, attempts FROM outbox_events WHERE event_id = $1`, envelope.EventID).Scan(&delivered, &attempts))
	assert.Equal(t, "sent", delivered, "event should be marked sent")
	assert.GreaterOrEqual(t, attempts, 0)

	received := asmServer.Events()
	require.Len(t, received, 1, "ASM should receive exactly 1 event")
	assert.Equal(t, envelope.EventID, received[0]["event_id"])
}

// TestE2E_ConcurrentDispatchers_NoDoubleDelivery is the regression test for
// the per-event transaction fix (doc 16 FIX1). Two dispatcher instances poll
// the same database concurrently. With the fix, SELECT ... FOR UPDATE SKIP
// LOCKED inside a per-event transaction serialises the claims: each event is
// delivered exactly once. Without the fix (the claim SELECT ran in
// autocommit, so the row lock was released immediately), both dispatchers
// could claim the same event and double-deliver it — the second POST would
// be rejected by ASM as a duplicate event_id (409), inflating the attempt
// count above N.
//
// Assertion: asm.Attempts() == N (exactly one POST per event, no retries
// caused by duplicate-delivery 409s).
func TestE2E_ConcurrentDispatchers_NoDoubleDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const (
		n        = 20
		tenantID = "test-concurrent"
	)

	asmServer := asm.NewServer("test-secret-key")
	testServer := httptest.NewServer(asmServer)
	defer testServer.Close()

	db := mustOpenTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cleanup := func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM outbox_events WHERE tenant_id = $1`, tenantID)
	}
	cleanup()
	defer cleanup()

	// Seed N events, each in its own committed tx.
	for i := 0; i < n; i++ {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		w := outbox.NewWriter(tx)
		require.NoError(t, w.Write(ctx, validEnvelope(
			tenantID,
			"evt-concurrent-"+pad(i),
			"session-concurrent-"+pad(i),
			1,
		)))
		require.NoError(t, tx.Commit())
	}

	mkDispatcher := func() *outbox.Dispatcher {
		return outbox.NewDispatcher(outbox.DispatcherConfig{
			DB:           db,
			ASMEndpoint:  testServer.URL + "/internal/v1/events",
			HMACSecret:   "test-secret-key",
			PollInterval: 50 * time.Millisecond,
			MaxAttempts:  3,
		})
	}
	d1, d2 := mkDispatcher(), mkDispatcher()

	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = d1.Start(dispatchCtx) }()
	go func() { defer wg.Done(); _ = d2.Start(dispatchCtx) }()

	// Wait until all events are settled (sent or dlq), then stop.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var settled int
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM outbox_events
			WHERE tenant_id = $1 AND status IN ('sent','dlq')
		`, tenantID).Scan(&settled))
		if settled >= n {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	dispatchCancel()
	wg.Wait()

	// Core FIX1 regression assertion: exactly one delivery attempt per event.
	// Any value > n means a concurrent dispatcher double-delivered (the dup
	// POST was rejected by ASM, inflating the count).
	assert.Equal(t, n, asmServer.Attempts(),
		"each event must be delivered exactly once; extra attempts indicate double-delivery")

	// And every event should be cleanly sent (no DLQ from 409 retry loops).
	var sent, dlq int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE tenant_id = $1 AND status = 'sent'`, tenantID).Scan(&sent))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE tenant_id = $1 AND status = 'dlq'`, tenantID).Scan(&dlq))
	assert.Equal(t, n, sent, "all events should be delivered")
	assert.Zero(t, dlq, "no event should reach the DLQ")
}

// validEnvelope builds an EventEnvelope whose payload satisfies the ASM mock's
// validateEvent (all required fields present, no forbidden fields).
func validEnvelope(tenantID, eventID, sessionID string, turnNo int) outbox.EventEnvelope {
	reqID := "req-" + eventID
	return outbox.EventEnvelope{
		EventID:          eventID,
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         tenantID,
		AggregateID:      sessionID,
		AggregateVersion: turnNo,
		OccurredAt:       time.Now(),
		Payload: map[string]any{
			"session_id":      sessionID,
			"turn_no":         turnNo,
			"request_id":      reqID,
			"correlation_id":  reqID,
			"idempotency_key": reqID,
			"provider":        "openai",
			"model":           "gpt-4",
			"status":          "succeeded",
			"token_usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
			"latency_ms": 1234,
			"body_refs": map[string]any{
				"prompt_ref":   "internal://body/" + reqID + "/prompt",
				"response_ref": "internal://body/" + reqID + "/response",
			},
		},
	}
}

func pad(i int) string { return fmt.Sprintf("%04d", i) }

func mustOpenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("skipping integration test: test database not reachable: %v", err)
	}
	return db
}
