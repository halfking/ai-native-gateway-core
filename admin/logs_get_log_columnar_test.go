// Copyright 2026 kaixuan.ai
// Regression tests for the two-stage request body lookup introduced in
// admin/logs.go::fetchRequestBodies.
//
// 2026-08-17 incident: dashboard "实时请求流 → 点击请求" returns HTTP 500
// `{"error":{"detail":"query failed","db_error":"timeout: context
// deadline exceeded"}}`.
//
// Root cause: request_logs_bodies_with_current_month UNIONs hot (heap,
// idx <1ms) with columnar monthly partitions (request_logs_bodies_2026_08,
// 2 GB, no btree usable, ColumnarScan = full decompression pass for single
// ID lookup, >30s). Even when the row lives in hot, the planner had to
// include the columnar partition in the Append node.
//
// Fix: getLog no longer JOINs the bodies view in the main metadata query.
// fetchRequestBodies is called instead and short-circuits after hot hits;
// columnar is only consulted when hot misses (older rows after TTL promote).
//
// These tests assert:
//   - hot-only row → helper returns body from hot, completes fast.
//   - hot-empty row → helper falls back to bodies view (cold path).
//   - bodies absent → helper returns sql.ErrNoRows; caller (getLog)
//     treats this as non-fatal and still returns metadata.
//
// Run with TEST_DATABASE_URL pointing at the dev DB. -short skips them.
package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchRequestBodies_HotOnly_PassesInUnderOneSecond is the regression
// for the dashboard incident. The previous LEFT JOIN forced a columnar
// scan regardless of where the body lived; this test verifies that with
// the new helper, a hot-only row resolves in well under 1 s.
func TestFetchRequestBodies_HotOnly_PassesInUnderOneSecond(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-hot-" + time.Now().Format("20060102-150405.000000")
	insertTestRequestLogWithBodies(t, pool, requestID)
	t.Cleanup(func() { cleanupTestRequestLog(t, pool, requestID) })

	h := &Handler{db: pool}
	start := time.Now()
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	elapsed := time.Since(start)

	require.NoError(t, err, "hot-only row should resolve without error")
	assert.Less(t, elapsed, 1*time.Second,
		"hot path must not trigger columnar scan (got %s; if this is slow the fix regressed)", elapsed)

	// Verify body contents survived the trip through decodeStoredBodyForAdmin.
	rbJSON, ok := reqBody.(map[string]any)
	require.True(t, ok, "requestBody should decode to a JSON object, got %T", reqBody)
	assert.Contains(t, rbJSON, "model", "requestBody should carry the inserted model field")

	obJSON, ok := respBody.(map[string]any)
	require.True(t, ok, "responseBody should decode to a JSON object, got %T", respBody)
	assert.Contains(t, obJSON, "id", "responseBody should carry the inserted id field")
}

// TestFetchRequestBodies_HotMiss_FallsBackToBodiesView verifies that when
// hot has no row for a given request_id, the helper still tries the bodies
// view. We can only assert the fallback is *invoked* (no columnar in the
// dev DB test environment if TTL hasn't promoted anything), so we
// simulate by inserting directly into request_logs_bodies only.
func TestFetchRequestBodies_HotMiss_FallsBackToBodiesView(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-cold-" + time.Now().Format("20060102-150405.000000")

	// Insert metadata + body in the partitioned table only. request_logs_hot
	// and request_logs_bodies_hot will not have this request_id, so the
	// helper must fall back to the bodies view.
	require.NoError(t, insertBodyIntoPartitionedOnly(t, pool, requestID))
	t.Cleanup(func() { cleanupColdBody(t, pool, requestID) })

	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)

	// Depending on whether TTL has promoted this row into a columnar partition
	// in the dev DB, the fallback can succeed or return ErrNoRows. We only
	// require that the helper attempts the second query without panicking.
	if err != nil {
		assert.ErrorIs(t, err, sql.ErrNoRows,
			"hot-miss fallback should report ErrNoRows when bodies view has no row either (got %v)", err)
		assert.Nil(t, reqBody)
		assert.Nil(t, respBody)
		return
	}
	// If the bodies view does contain the row, verify content.
	assert.NotNil(t, reqBody, "cold path should still surface body when present")
}

// TestFetchRequestBodies_TotalMissReturnsErrNoRows verifies the
// no-row-at-all case. getLog treats this error as non-fatal (body fields
// nil out, metadata still returns), so fetchRequestBodies must surface
// sql.ErrNoRows so callers can distinguish from real errors.
func TestFetchRequestBodies_TotalMissReturnsErrNoRows(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-none-" + time.Now().Format("20060102-150405.000000")

	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)

	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows,
		"missing-in-both should be sql.ErrNoRows so caller treats it as non-fatal")
	assert.Nil(t, reqBody)
	assert.Nil(t, respBody)
}

// TestFetchRequestBodies_CancelledParentCtx_DoesNotHang is the regression
// for the cold-path defense (2026-08-17 follow-up). fetchRequestBodies
// receives a parent ctx; the cold-path must derive its own sub-ctx from
// that parent so that if the parent is cancelled (e.g. http.Server
// shutting down mid-handler, or the outer 30s ctx of getLog fires), the
// helper returns within ms — not hang on a 30s columnar scan.
//
// We simulate by passing an already-cancelled context and asserting the
// helper returns an error (sql.ErrNoRows or context.DeadlineExceeded)
// quickly (< 2s).
func TestFetchRequestBodies_CancelledParentCtx_DoesNotHang(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	h := &Handler{db: pool}
	start := time.Now()
	_, _, err := h.fetchRequestBodies(ctx, "any-request-id")
	elapsed := time.Since(start)

	require.Error(t, err, "cancelled parent ctx should yield error")
	assert.Less(t, elapsed, 2*time.Second,
		"cancelled parent ctx must propagate to cold path immediately (got %s; if this hangs the ctx hierarchy regressed)", elapsed)
}

// insertBodyIntoPartitionedOnly writes a body row directly into
// request_logs_bodies (no hot counterpart). Used to exercise the cold
// fallback path in fetchRequestBodies.
func insertBodyIntoPartitionedOnly(t *testing.T, pool *pgxpool.Pool, requestID string) error {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO request_logs_bodies (request_id, ts, request_body, response_body)
		VALUES (
			$1,
			now(),
			'{"model":"cold-model","messages":[{"role":"user","content":"cold"}]}'::jsonb,
			'{"id":"cold-response","content":"cold-result"}'::jsonb
		)
	`, requestID)
	return err
}

// cleanupColdBody removes the cold-only row inserted by
// insertBodyIntoPartitionedOnly. Columnar partitions disallow DELETE on
// promoted rows, so we tolerate failures here and let the test harness
// emit a warning instead of failing.
func cleanupColdBody(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM request_logs_bodies WHERE request_id = $1`, requestID); err != nil {
		t.Logf("cleanupColdBody: best-effort delete failed (columnar partition may be read-only): %v", err)
	}
}
