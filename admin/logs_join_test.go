package admin

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetLogDetail_TimestampMismatch verifies that request_body and response_body
// can still be retrieved even when the ts field differs slightly between
// request_logs_hot and request_logs_bodies_hot.
//
// Background: The current JOIN condition uses both request_id AND ts:
//
//	LEFT JOIN request_logs_bodies_with_current_month rb
//	  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
//
// This causes JOIN failures when:
// - Network latency between two INSERT statements
// - Transaction commit time differences
// - Database timestamp precision issues
// - Partition table timestamp conversions
//
// Expected behavior: Since request_id is semantically unique (one request
// has only one message body), the JOIN should work with request_id alone.
func TestGetLogDetail_TimestampMismatch(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-ts-mismatch-" + time.Now().Format("20060102-150405.000")

	// Simulate the scenario where metadata and body are written with slightly different timestamps
	ts1 := time.Now().UTC()
	ts2 := ts1.Add(500 * time.Microsecond) // 500 microseconds difference

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Insert into usage_ledger_hot
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, success
		) VALUES ($1, $2, 'test-tenant', true)
	`, requestID, ts1)
	require.NoError(t, err)

	// Insert into request_logs_hot with ts1
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, success,
			client_model, outbound_model
		) VALUES ($1, $2, 'test-tenant', 1, true, 'gpt-4', 'gpt-4')
	`, requestID, ts1)
	require.NoError(t, err)

	// Insert into request_logs_bodies_hot with ts2 (slightly different!)
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_bodies_hot (
			request_id, ts, tenant_id,
			request_body, response_body
		) VALUES ($1, $2, 'test-tenant',
			'{"messages":[{"role":"user","content":"test"}]}'::jsonb,
			'{"choices":[{"message":{"role":"assistant","content":"response"}}]}'::jsonb
		)
	`, requestID, ts2)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))

	// Query using the current JOIN pattern (with ts condition)
	var requestBodyWithTS, responseBodyWithTS *string
	err = pool.QueryRow(ctx, `
		SELECT
		  COALESCE(rb.request_body::text, '') AS request_body,
		  COALESCE(rb.response_body::text, '') AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBodyWithTS, &responseBodyWithTS)
	require.NoError(t, err)

	// Current behavior: JOIN fails due to ts mismatch, bodies are NULL
	assert.Nil(t, requestBodyWithTS, "Expected NULL with ts condition (current broken behavior)")
	assert.Nil(t, responseBodyWithTS, "Expected NULL with ts condition (current broken behavior)")

	// Query using the proposed JOIN pattern (without ts condition)
	var requestBodyWithoutTS, responseBodyWithoutTS *string
	err = pool.QueryRow(ctx, `
		SELECT
		  COALESCE(rb.request_body::text, '') AS request_body,
		  COALESCE(rb.response_body::text, '') AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBodyWithoutTS, &responseBodyWithoutTS)
	require.NoError(t, err)

	// Expected behavior: JOIN succeeds with request_id alone, bodies are present
	require.NotNil(t, requestBodyWithoutTS, "Expected body to be found without ts condition")
	require.NotNil(t, responseBodyWithoutTS, "Expected body to be found without ts condition")
	assert.Contains(t, *requestBodyWithoutTS, "test", "Request body should contain test message")
	assert.Contains(t, *responseBodyWithoutTS, "response", "Response body should contain response")

	t.Logf("✅ Test demonstrates that removing ts from JOIN condition fixes the issue")
	t.Logf("   - With ts condition: bodies are NULL (broken)")
	t.Logf("   - Without ts condition: bodies are retrieved correctly (fixed)")
}
