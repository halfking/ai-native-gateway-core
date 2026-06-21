//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRequestLifecycle_CompleteFlow tests the full request lifecycle
// from CreateInitial to Update to UpdateSync, against a real PostgreSQL database.
//
// To run this test:
//   export LLM_GATEWAY_PG_URL="postgres://user:pass@host:port/db?sslmode=disable"
//   go test -tags=integration ./tests/integration -v -run TestRequestLifecycle
func TestRequestLifecycle_CompleteFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	// Create test request logger
	rl := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
		QueueSize:    1000,
		BatchSize:    10,
		FlushTimeout: 50 * time.Millisecond,
		Enabled:      true,
	})
	defer rl.Stop()

	// Test 1: CreateInitial (sync insert)
	t.Run("CreateInitial", func(t *testing.T) {
		reqID := "test-req-" + time.Now().Format("20060102150405.000000")
		err := rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID:   reqID,
			TenantID:    "test-tenant",
			SessionID:   "gw_test_session",
			ClientModel: "gpt-4o-mini",
		})
		if err != nil {
			t.Fatalf("CreateInitial failed: %v", err)
		}

		// Verify the record was created
		var status string
		var stage int
		err = pool.QueryRow(ctx,
			`SELECT status, stage FROM request_wal WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
			reqID,
		).Scan(&status, &stage)
		if err != nil {
			t.Fatalf("query record: %v", err)
		}
		if status != "pending" {
			t.Errorf("expected status=pending, got %s", status)
		}
		if stage != 0 {
			t.Errorf("expected stage=0, got %d", stage)
		}
		t.Logf("✓ CreateInitial created record: status=%s stage=%d", status, stage)
	})

	// Test 2: Update (async batch)
	t.Run("Update_AsyncBatch", func(t *testing.T) {
		reqID := "test-req-async-" + time.Now().Format("20060102150405.000000")
		_ = rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID:   reqID,
			TenantID:    "test-tenant",
			ClientModel: "gpt-4o-mini",
		})

		// Trigger async update
		rl.Update(&telemetry.LogUpdate{
			RequestID: reqID,
			Stage:     telemetry.StageCompleted,
			Status:    telemetry.StatusSuccess,
			CompressionStrategy: "delta_append",
			CompressionMeta: map[string]interface{}{
				"msg_count": 5,
			},
		})

		// Wait for batch flush (50ms timeout + 100ms buffer)
		time.Sleep(200 * time.Millisecond)

		// Verify the record was updated
		var status string
		var stage int
		var strategy *string
		err := pool.QueryRow(ctx,
			`SELECT status, stage, compression_strategy FROM request_wal WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
			reqID,
		).Scan(&status, &stage, &strategy)
		if err != nil {
			t.Fatalf("query record: %v", err)
		}
		if status != "success" {
			t.Errorf("expected status=success, got %s", status)
		}
		if stage != 4 {
			t.Errorf("expected stage=4, got %d", stage)
		}
		if strategy == nil || *strategy != "delta_append" {
			t.Errorf("expected compression_strategy=delta_append, got %v", strategy)
		}
		t.Logf("✓ Async update applied: status=%s stage=%d strategy=%s", status, stage, *strategy)
	})

	// Test 3: UpdateSync (sync for failures)
	t.Run("UpdateSync_Failure", func(t *testing.T) {
		reqID := "test-req-fail-" + time.Now().Format("20060102150405.000000")
		_ = rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID:   reqID,
			TenantID:    "test-tenant",
			ClientModel: "gpt-4o-mini",
		})

		// Sync update with error
		err := rl.UpdateSync(ctx, &telemetry.LogUpdate{
			RequestID: reqID,
			Stage:     telemetry.StageExecuteFail,
			Status:    telemetry.StatusFailure,
			Error:     "upstream timeout after 30s",
		})
		if err != nil {
			t.Fatalf("UpdateSync failed: %v", err)
		}

		// Verify the record was updated immediately
		var status string
		var stage int
		var errMsg *string
		err = pool.QueryRow(ctx,
			`SELECT status, stage, error FROM request_wal WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
			reqID,
		).Scan(&status, &stage, &errMsg)
		if err != nil {
			t.Fatalf("query record: %v", err)
		}
		if status != "failure" {
			t.Errorf("expected status=failure, got %s", status)
		}
		if stage != 12 {
			t.Errorf("expected stage=12, got %d", stage)
		}
		if errMsg == nil || *errMsg != "upstream timeout after 30s" {
			t.Errorf("expected error message, got %v", errMsg)
		}
		t.Logf("✓ Sync failure update: status=%s stage=%d error=%s", status, stage, *errMsg)
	})

	// Test 4: Concurrent writes
	t.Run("ConcurrentWrites_1000", func(t *testing.T) {
		const numRequests = 100
		done := make(chan error, numRequests)

		for i := 0; i < numRequests; i++ {
			go func(idx int) {
				reqID := "test-req-concurrent-" + time.Now().Format("20060102150405") + "-" + string(rune('A'+idx%26))
				err := rl.CreateInitial(ctx, &telemetry.InitialRequest{
					RequestID:   reqID,
					TenantID:    "test-tenant",
					ClientModel: "gpt-4o-mini",
				})
				done <- err
			}(i)
		}

		// Wait for all to complete
		for i := 0; i < numRequests; i++ {
			if err := <-done; err != nil {
				t.Errorf("concurrent write %d failed: %v", i, err)
			}
		}

		// Count records created
		var count int
		err = pool.QueryRow(ctx,
			`SELECT count(*) FROM request_wal WHERE request_id LIKE 'test-req-concurrent-%'`,
		).Scan(&count)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != numRequests {
			t.Errorf("expected %d records, got %d", numRequests, count)
		}
		t.Logf("✓ Created %d concurrent records", count)
	})
}

// TestRequestBodies_Storage tests the request_wal_bodies auxiliary table
func TestRequestBodies_Storage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Create a request and body record
	reqID := "test-body-" + time.Now().Format("20060102150405.000000")
	_, err = pool.Exec(ctx,
		`INSERT INTO request_wal (request_id, tenant_id, client_model) VALUES ($1, $2, $3)`,
		reqID, "test-tenant", "gpt-4o-mini",
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Insert body
	_, err = pool.Exec(ctx,
		`INSERT INTO request_wal_bodies (request_id, outbound_body, compression_meta) VALUES ($1, $2, $3)`,
		reqID, []byte("test outbound body"), []byte(`{"strategy":"delta_append"}`),
	)
	if err != nil {
		t.Fatalf("insert body: %v", err)
	}

	// Verify
	var body []byte
	var meta []byte
	err = pool.QueryRow(ctx,
		`SELECT outbound_body, compression_meta FROM request_wal_bodies WHERE request_id = $1`,
		reqID,
	).Scan(&body, &meta)
	if err != nil {
		t.Fatalf("query body: %v", err)
	}
	if string(body) != "test outbound body" {
		t.Errorf("expected body 'test outbound body', got '%s'", string(body))
	}
	t.Logf("✓ request_wal_bodies record created successfully")
}
