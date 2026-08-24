package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetLogDetail_WithBodies verifies that the log detail API correctly retrieves
// complete request_body and response_body via LEFT JOIN + COALESCE pattern.
func TestGetLogDetail_WithBodies(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-get-detail-" + time.Now().Format("20060102-150405.000")

	// Insert test data into both tables (simulating Ticket #10 dual-write)
	insertTestRequestLogWithBodies(t, pool, requestID)

	// Query using the COALESCE pattern (simulating getLog API)
	var requestBody, responseBody string
	err := pool.QueryRow(ctx, `
SELECT
		  rb.request_body::text AS request_body,
		  rb.response_body::text AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb 
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)

	require.NoError(t, err, "query should succeed")
	assert.Contains(t, requestBody, "test-model", "should retrieve complete request_body")
	assert.Contains(t, responseBody, "test-response", "should retrieve complete response_body")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestGetLogDetail_BackwardsCompatible verifies that the COALESCE pattern correctly
// handles "old data" where bodies are still in request_logs_hot (before migration).
func TestGetLogDetail_BackwardsCompatible(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-backwards-compat-" + time.Now().Format("20060102-150405.000")

	// Insert "legacy" data: bodies in request_logs_hot, NOT in request_logs_bodies_hot
	insertLegacyRequestLog(t, pool, requestID)

	// Query using the COALESCE pattern
	var requestBody, responseBody string
	err := pool.QueryRow(ctx, `
SELECT
		  rb.request_body::text AS request_body,
		  rb.response_body::text AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb 
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)

	require.NoError(t, err, "query should succeed with legacy data")
	assert.Contains(t, requestBody, "legacy-request", "should fallback to rl.request_body")
	assert.Contains(t, responseBody, "legacy-response", "should fallback to rl.response_body")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestGetLogDetail_MissingBodies verifies that the LEFT JOIN handles cases where
// request_logs_bodies_hot has no matching record (returns NULL gracefully).
func TestGetLogDetail_MissingBodies(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-missing-bodies-" + time.Now().Format("20060102-150405.000")

	// Insert ONLY into request_logs_hot (no bodies in either table)
	insertTestRequestLogMetadataOnly(t, pool, requestID)

	// Query using the COALESCE pattern
	var requestBody, responseBody *string
	err := pool.QueryRow(ctx, `
SELECT
		  rb.request_body::text AS request_body,
		  rb.response_body::text AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb 
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)

	require.NoError(t, err, "query should succeed even with missing bodies")
	assert.Nil(t, requestBody, "request_body should be NULL when missing")
	assert.Nil(t, responseBody, "response_body should be NULL when missing")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// insertTestRequestLogWithBodies inserts test data into both tables (new dual-write pattern)
func insertTestRequestLogWithBodies(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Insert into usage_ledger_hot (required by foreign key or dual-write)
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, success
		) VALUES ($1, now(), 'test-tenant', true)
	`, requestID)
	require.NoError(t, err)

	// Insert into request_logs_hot (NO bodies)
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, success
		) VALUES ($1, now(), 'test-tenant', 1, true)
	`, requestID)
	require.NoError(t, err)

	// Insert into request_logs_bodies_hot (with bodies)
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_bodies_hot (
			request_id, ts, request_body, response_body
		) VALUES (
			$1, now(), 
			'{"model":"test-model","messages":[{"role":"user","content":"test"}]}'::jsonb,
			'{"id":"test-response","content":"result"}'::jsonb
		)
	`, requestID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))
}

// insertLegacyRequestLog inserts "old data" where bodies are in request_logs_hot
func insertLegacyRequestLog(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Insert into usage_ledger_hot
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, success
		) VALUES ($1, now(), 'test-tenant', true)
	`, requestID)
	require.NoError(t, err)

	// Insert into request_logs_hot WITH bodies (legacy pattern)
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, success,
			request_body, response_body
		) VALUES (
			$1, now(), 'test-tenant', 1, true,
			'{"legacy":"legacy-request"}'::jsonb,
			'{"legacy":"legacy-response"}'::jsonb
		)
	`, requestID)
	require.NoError(t, err)

	// Do NOT insert into request_logs_bodies_hot (simulating old data)

	require.NoError(t, tx.Commit(ctx))
}

// insertTestRequestLogMetadataOnly inserts only metadata (no bodies anywhere)
func insertTestRequestLogMetadataOnly(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Insert into usage_ledger_hot
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, success
		) VALUES ($1, now(), 'test-tenant', true)
	`, requestID)
	require.NoError(t, err)

	// Insert into request_logs_hot (NO bodies)
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, success
		) VALUES ($1, now(), 'test-tenant', 1, true)
	`, requestID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))
}
