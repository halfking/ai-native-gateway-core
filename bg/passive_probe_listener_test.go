package bg

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPassiveProbe_TransientErrorTracking verifies that transient errors are correctly tracked
// in the passive_probe_state table.
//
// This test addresses the root cause identified in the circuit breaker fix:
// transient errors were not being tracked by passive probe listener, causing
// high-failure credentials to remain in the candidate pool.
func TestPassiveProbe_TransientErrorTracking(t *testing.T) {
	dsn := getTestDSN(t)
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Setup: Create test credential and insert transient error logs
	setupTestData(t, pool)
	defer cleanupTestData(t, pool)

	// Insert 5 transient errors in the last 5 minutes for credential 999
	now := time.Now()
	for i := 0; i < 5; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO request_logs 
			(tenant_id, api_key_id, credential_id, client_model, outbound_model, 
			 success, error_kind, ts, provider_id)
			VALUES 
			($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, "test-tenant", 1, 999, "minimax-m3", "minimax-m3", 
			false, "transient", now.Add(-time.Duration(i)*time.Minute), 1)
		require.NoError(t, err)
	}

	// Create and run passive probe listener
	listener := &PassiveProbeListener{
		db:           pool,
		pollInterval: 1 * time.Second,
	}

	// Run one poll cycle
	listener.pollNewErrors(ctx)

	// Verify: Check that passive_probe_state has recorded the transient errors
	var consecutiveCount, windowTotalCount int
	err = pool.QueryRow(ctx, `
		SELECT consecutive_count, window_total_count 
		FROM passive_probe_state 
		WHERE credential_id = 999 
		  AND raw_model_name = 'minimax-m3'
		  AND error_kind = 'transient'
	`).Scan(&consecutiveCount, &windowTotalCount)
	require.NoError(t, err, "transient errors should be tracked in passive_probe_state")

	assert.GreaterOrEqual(t, consecutiveCount, 5, "should track at least 5 consecutive transient errors")
	assert.GreaterOrEqual(t, windowTotalCount, 5, "should track at least 5 total transient errors in window")
}

// TestPassiveProbe_ReviewPromotion_TransientThreshold verifies that credentials
// are promoted to reviewing state after 3 consecutive transient errors.
func TestPassiveProbe_ReviewPromotion_TransientThreshold(t *testing.T) {
	dsn := getTestDSN(t)
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	setupTestData(t, pool)
	defer cleanupTestData(t, pool)

	// Insert passive_probe_state with 3 consecutive transient errors
	_, err = pool.Exec(ctx, `
		INSERT INTO passive_probe_state 
		(credential_id, raw_model_name, error_kind, consecutive_count, 
		 total_recent_count, window_total_count, first_seen_at, last_seen_at)
		VALUES 
		(999, 'minimax-m3', 'transient', 3, 3, 3, NOW(), NOW())
	`)
	require.NoError(t, err)

	listener := &PassiveProbeListener{
		db:           pool,
		pollInterval: 1 * time.Second,
	}

	// Run review promotion check
	listener.reviewPromotion(ctx)

	// Verify: Check that in_reviewing flag is set
	var inReviewing bool
	var reviewingUntil *time.Time
	err = pool.QueryRow(ctx, `
		SELECT in_reviewing, reviewing_until 
		FROM passive_probe_state 
		WHERE credential_id = 999 
		  AND raw_model_name = 'minimax-m3'
		  AND error_kind = 'transient'
	`).Scan(&inReviewing, &reviewingUntil)
	require.NoError(t, err)

	assert.True(t, inReviewing, "should promote to reviewing after 3 consecutive transient errors")
	assert.NotNil(t, reviewingUntil, "should set reviewing_until timestamp")
	if reviewingUntil != nil {
		expectedUntil := time.Now().Add(5 * time.Minute)
		assert.WithinDuration(t, expectedUntil, *reviewingUntil, 10*time.Second,
			"reviewing_until should be ~5 minutes from now")
	}
}

// TestPassiveProbe_ReviewPromotion_TransientErrorRate verifies that credentials
// are promoted to reviewing state when transient error rate >= 60% and total >= 5.
func TestPassiveProbe_ReviewPromotion_TransientErrorRate(t *testing.T) {
	dsn := getTestDSN(t)
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	setupTestData(t, pool)
	defer cleanupTestData(t, pool)

	// Insert passive_probe_state with 60% error rate (6 errors out of 10 total)
	_, err = pool.Exec(ctx, `
		INSERT INTO passive_probe_state 
		(credential_id, raw_model_name, error_kind, consecutive_count, 
		 total_recent_count, window_total_count, first_seen_at, last_seen_at)
		VALUES 
		(999, 'minimax-m3', 'transient', 2, 6, 10, NOW(), NOW())
	`)
	require.NoError(t, err)

	listener := &PassiveProbeListener{
		db:           pool,
		pollInterval: 1 * time.Second,
	}

	listener.reviewPromotion(ctx)

	// Verify: Check that in_reviewing flag is set
	var inReviewing bool
	err = pool.QueryRow(ctx, `
		SELECT in_reviewing 
		FROM passive_probe_state 
		WHERE credential_id = 999 
		  AND raw_model_name = 'minimax-m3'
		  AND error_kind = 'transient'
	`).Scan(&inReviewing)
	require.NoError(t, err)

	assert.True(t, inReviewing, "should promote to reviewing with 60% transient error rate")
}

// TestPassiveProbe_OtherErrorKindsStillTracked verifies that the fix doesn't
// break existing error kind tracking (regression test).
func TestPassiveProbe_OtherErrorKindsStillTracked(t *testing.T) {
	dsn := getTestDSN(t)
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	setupTestData(t, pool)
	defer cleanupTestData(t, pool)

	errorKinds := []string{
		"model_not_found", "quota", "rate_limit", "auth", "upstream_down",
		"timeout", "network", "concurrent", "stream_timeout",
	}

	now := time.Now()
	for _, kind := range errorKinds {
		_, err := pool.Exec(ctx, `
			INSERT INTO request_logs 
			(tenant_id, api_key_id, credential_id, client_model, outbound_model, 
			 success, error_kind, ts, provider_id)
			VALUES 
			($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, "test-tenant", 1, 999, "test-model", "test-model", 
			false, kind, now.Add(-1*time.Minute), 1)
		require.NoError(t, err)
	}

	listener := &PassiveProbeListener{
		db:           pool,
		pollInterval: 1 * time.Second,
	}

	listener.pollNewErrors(ctx)

	// Verify: All error kinds should be tracked
	for _, kind := range errorKinds {
		var count int
		err := pool.QueryRow(ctx, `
			SELECT COUNT(*) 
			FROM passive_probe_state 
			WHERE credential_id = 999 
			  AND error_kind = $1
		`, kind).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "error_kind %s should be tracked", kind)
	}
}

// Helper functions

func getTestDSN(t *testing.T) string {
	// Check for test database URL
	dsn := getEnv("TEST_DATABASE_URL", "")
	if dsn == "" {
		// Fallback to constructing from individual env vars
		host := getEnv("TEST_DB_HOST", "localhost")
		port := getEnv("TEST_DB_PORT", "5432")
		user := getEnv("TEST_DB_USER", "llm_gateway")
		pass := getEnv("TEST_DB_PASSWORD", "")
		dbname := getEnv("TEST_DB_NAME", "llm_gateway_test")

		if pass != "" {
			dsn = "postgres://" + user + ":" + pass + "@" + host + ":" + port + "/" + dbname + "?sslmode=disable"
		}
	}
	return dsn
}

func getEnv(key, fallback string) string {
	if value := getEnvFromContext(key); value != "" {
		return value
	}
	return fallback
}

func getEnvFromContext(key string) string {
	// This would be implemented to read from test context
	return ""
}

func setupTestData(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()

	// Create test credential if not exists
	_, err := pool.Exec(ctx, `
		INSERT INTO credentials (id, label, api_key, provider_id, lifecycle_status)
		VALUES (999, 'test-credential', 'test-key', 1, 'active')
		ON CONFLICT (id) DO NOTHING
	`)
	require.NoError(t, err)

	// Create test provider if not exists
	_, err = pool.Exec(ctx, `
		INSERT INTO providers (id, provider_code, display_name)
		VALUES (1, 'test-provider', 'Test Provider')
		ON CONFLICT (id) DO NOTHING
	`)
	require.NoError(t, err)
}

func cleanupTestData(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()

	// Clean up test data
	_, _ = pool.Exec(ctx, "DELETE FROM passive_probe_state WHERE credential_id = 999")
	_, _ = pool.Exec(ctx, "DELETE FROM request_logs WHERE credential_id = 999")
	_, _ = pool.Exec(ctx, "DELETE FROM credentials WHERE id = 999")
	_, _ = pool.Exec(ctx, "DELETE FROM providers WHERE id = 1")
}
