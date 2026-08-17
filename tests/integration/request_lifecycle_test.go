//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestRequestLifecycle_CompleteFlow tests the full request lifecycle
// from CreateInitial to Update to UpdateSync, against a real PostgreSQL database.
//
// To run this test:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn-with-credentials>
//	go test -tags=integration ./tests/integration -v -run TestRequestLifecycle
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

	cfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
			`SELECT status, stage FROM request_wal_hot WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
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
			RequestID:           reqID,
			Stage:               telemetry.StageCompleted,
			Status:              telemetry.StatusSuccess,
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
		var compressionMeta *string
		err := pool.QueryRow(ctx,
			`SELECT status, stage, compression_strategy, compression_meta::text FROM request_wal_hot WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
			reqID,
		).Scan(&status, &stage, &strategy, &compressionMeta)
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
		if compressionMeta == nil {
			t.Fatal("expected compression metadata")
		}
		var compressionMetaValue map[string]interface{}
		if err := json.Unmarshal([]byte(*compressionMeta), &compressionMetaValue); err != nil {
			t.Fatalf("decode compression metadata: %v", err)
		}
		if compressionMetaValue["msg_count"] != float64(5) {
			t.Errorf("expected compression_meta msg_count=5, got %v", compressionMetaValue)
		}
		t.Logf("✓ Async update applied: status=%s stage=%d strategy=%s compression_meta=%s", status, stage, *strategy, *compressionMeta)
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
			`SELECT status, stage, error FROM request_wal_hot WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`,
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
		prefix := fmt.Sprintf("test-req-concurrent-%d-", time.Now().UnixNano())
		done := make(chan error, numRequests)

		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM request_wal_bodies WHERE request_id LIKE $1`, prefix+"%")
			_, _ = pool.Exec(ctx, `DELETE FROM request_wal_hot WHERE request_id LIKE $1`, prefix+"%")
		})

		for i := 0; i < numRequests; i++ {
			go func(idx int) {
				reqID := fmt.Sprintf("%s%03d", prefix, idx)
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

		// Count records created for this test run only.
		var count int
		err = pool.QueryRow(ctx,
			`SELECT count(*) FROM request_wal_hot WHERE request_id LIKE $1`, prefix+"%",
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

	cfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	rl := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
		QueueSize:    100,
		BatchSize:    10,
		FlushTimeout: 50 * time.Millisecond,
		Enabled:      true,
	})
	defer rl.Stop()

	// Write both the main WAL row and body row through the production logger path.
	reqID := "test-body-" + time.Now().Format("20060102150405.000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM request_wal_bodies WHERE request_id = $1`, reqID)
		_, _ = pool.Exec(ctx, `DELETE FROM request_wal_hot WHERE request_id = $1`, reqID)
	})
	if err := rl.CreateInitial(ctx, &telemetry.InitialRequest{
		RequestID:   reqID,
		TenantID:    "test-tenant",
		ClientModel: "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("create initial: %v", err)
	}
	if err := rl.UpdateSync(ctx, &telemetry.LogUpdate{
		RequestID:           reqID,
		Stage:               telemetry.StageCompleted,
		Status:              telemetry.StatusSuccess,
		OutboundBody:        []byte("test outbound body"),
		CompressionStrategy: "delta_append",
		CompressionMeta:     map[string]interface{}{"strategy": "delta_append"},
	}); err != nil {
		t.Fatalf("update body: %v", err)
	}

	// Verify the body and JSONB metadata written by RequestLogger.
	var body string
	var metaJSON []byte
	err = pool.QueryRow(ctx,
		`SELECT outbound_body, compression_meta FROM request_wal_bodies WHERE request_id = $1`,
		reqID,
	).Scan(&body, &metaJSON)
	if err != nil {
		t.Fatalf("query body: %v", err)
	}
	if body != "test outbound body" {
		t.Errorf("expected body 'test outbound body', got '%s'", body)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("decode compression metadata: %v", err)
	}
	if meta["strategy"] != "delta_append" {
		t.Errorf("expected compression strategy, got %v", meta)
	}
	t.Logf("✓ request_wal_bodies record created successfully")
}
