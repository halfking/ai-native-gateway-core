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

	// Query using the new SSOT pattern: bodies live in request_logs_bodies_hot
	// (after migration 573 dropped request_logs_hot.{request,response,outbound}_body)
	var requestBody, responseBody string
	err := pool.QueryRow(ctx, `
		SELECT
		  COALESCE(rb.request_body::text, '') AS request_body,
		  COALESCE(rb.response_body::text, '') AS response_body
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

// TestGetLogDetail_BackwardsCompatible pins the storage-tier compatibility
// contract of the SSOT detail read path.
//
// R65 前提重构：本测试原名"bodies 仍在 request_logs_hot 的旧数据"——迁移
// 573 已 DROP request_logs_hot.{request,response,outbound}_body，"bodies 在
// 元数据表"的形态在存储层已不可表达，旧夹具 INSERT 实锤 42703（该测试自
// 573 起从未真绿）。仍真实存在且必须兼容的"旧数据"是**已晋升出 _hot 的
// 父表月分区行**；但本库 bodies 月分区全部为 citus_columnar（UPDATE/DELETE
// 0A000 不支持），主库纪律（只许夹具级 INSERT/DELETE）下无法无残留地造
// 父表行。故契约改钉两层：
//  1. 视图双腿覆盖（information_schema.view_table_usage 必须同时含
//     request_logs_bodies_hot 与 request_logs_bodies）——晋升行可达性的
//     存储层前提；
//  2. 行级：_hot 腿 JOIN 解析 body（可清理的实证半边）。
func TestGetLogDetail_BackwardsCompatible(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-backwards-compat-" + time.Now().Format("20060102-150405.000")

	// 契约 1：bodies 视图必须 UNION 两腿（_hot + 父表晋升行）。
	var legs []string
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.view_table_usage
		WHERE view_schema = 'public' AND view_name = 'request_logs_bodies_with_current_month'
		ORDER BY table_name
	`)
	require.NoError(t, err)
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		legs = append(legs, name)
	}
	require.NoError(t, rows.Err())
	assert.Contains(t, legs, "request_logs_bodies_hot", "bodies view must cover the _hot tier")
	assert.Contains(t, legs, "request_logs_bodies", "bodies view must cover the promoted parent tier")

	// 契约 2（行级可清理半边）：元数据 + _hot 腿 body 行经 SSOT JOIN 解析。
	insertTestRequestLogWithBodies(t, pool, requestID)
	var requestBody, responseBody string
	err = pool.QueryRow(ctx, `
		SELECT
		  COALESCE(rb.request_body::text, '') AS request_body,
		  COALESCE(rb.response_body::text, '') AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)
	require.NoError(t, err)
	assert.Contains(t, requestBody, "test-model", "hot-tier bodies resolve via the view JOIN")
	assert.Contains(t, responseBody, "test-response", "hot-tier bodies resolve via the view JOIN")

	// Cleanup（注册晚于 pool.Close 的 defer，LIFO 先清理后关池）
	defer func() { cleanupTestRequestLog(t, pool, requestID) }()
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

	// Query using the new SSOT pattern: bodies live in request_logs_bodies_hot
	var requestBody, responseBody *string
	err := pool.QueryRow(ctx, `
		SELECT
		  COALESCE(rb.request_body::text, '') AS request_body,
		  COALESCE(rb.response_body::text, '') AS response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)

	require.NoError(t, err, "query should succeed even with missing bodies")
	// R65 断言适配：查询用 COALESCE(rb.x::text, '')——无匹配时契约值是
	// 空串而非 NULL（修前断言 Nil 与自身查询形状矛盾）。
	assert.Empty(t, requestBody, "request_body should be empty string when missing (COALESCE '')")
	assert.Empty(t, responseBody, "response_body should be empty string when missing (COALESCE '')")

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
