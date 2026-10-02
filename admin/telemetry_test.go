package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPersistRequestLog_BothTablesWritten verifies that persistRequestLog writes
// to both request_logs_hot and request_logs_bodies_hot in a single transaction.
// This is the core test for Ticket #10 dual-write logic.
func TestPersistRequestLog_BothTablesWritten(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	// Generate unique request_id for this test
	requestID := "test-dual-write-" + time.Now().Format("20060102-150405.000")

	// Prepare test input with complete bodies
	input := &requestLogInput{
		RequestID:        requestID,
		TenantID:         "test-tenant",
		ApplicationID:    intPtr(1),
		APIKeyID:         intPtr(100),
		CredentialID:     intPtr(10),
		ProviderID:       intPtr(20),
		CanonicalID:      intPtr(5),
		PromptTokens:     intPtr(100),
		CompletionTokens: intPtr(50),
		Success:          true,
		RequestBody:      strPtrTelemetry(`{"model":"claude-3","messages":[{"role":"user","content":"test"}]}`),
		ResponseBody:     strPtrTelemetry(`{"id":"msg_123","content":[{"type":"text","text":"response"}]}`),
		RequestPreview:   strPtrTelemetry("test preview"),
		ResponsePreview:  strPtrTelemetry("response preview"),
	}

	// Execute dual-write
	ingester.persistRequestLog(ctx, input)

	// Verify request_logs_hot has metadata WITHOUT bodies columns.
	// R65 夹具适配：迁移 573 已 DROP request_logs_hot.{request,response}_body
	//（bodies SSOT 迁往 request_logs_bodies_hot），元数据表只余 preview 字段；
	// 修前查询引用已删列实锤 42703。列不存在本身即双写分离的存储层契约
	//（由下方 bodies 查询断言补全内容侧）。
	var rlRequestPreview, rlResponsePreview *string
	err := pool.QueryRow(ctx, `
		SELECT request_preview, response_preview
		FROM request_logs_hot
		WHERE request_id = $1
	`, requestID).Scan(&rlRequestPreview, &rlResponsePreview)
	require.NoError(t, err, "request_logs_hot should have the record")

	assert.NotNil(t, rlRequestPreview, "request_logs_hot.request_preview should exist")
	assert.NotNil(t, rlResponsePreview, "request_logs_hot.response_preview should exist")

	// Verify request_logs_bodies_hot has complete bodies
	var rbRequestBody, rbResponseBody string
	err = pool.QueryRow(ctx, `
		SELECT request_body::text, response_body::text
		FROM request_logs_bodies_hot
		WHERE request_id = $1
	`, requestID).Scan(&rbRequestBody, &rbResponseBody)
	require.NoError(t, err, "request_logs_bodies_hot should have the record")

	assert.Contains(t, rbRequestBody, "claude-3", "request_body should contain model")
	assert.Contains(t, rbResponseBody, "msg_123", "response_body should contain response id")

	var joinedBody string
	err = pool.QueryRow(ctx, `
		SELECT rb.request_body::text
		FROM request_logs_hot rl
		JOIN request_logs_bodies_hot rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
	`, requestID).Scan(&joinedBody)
	require.NoError(t, err, "metadata/body join should find the request body")
	assert.Contains(t, joinedBody, "claude-3")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestPersistRequestLog_ReingestUpsertsSameRequestID verifies the idempotent
// re-ingest contract for a repeated request_id.
//
// R65 契约适配：本测试原名 TransactionRollback，断言"重复 INSERT 违反
// UNIQUE 后整事务回滚、旧 payload 保留"。生产实现自 migration-455
// UNIQUE(request_id) 起改为**幂等 upsert**（request_logs_hot
// ON CONFLICT DO UPDATE、bodies UPDATE-then-INSERT，见
// persistRequestLog/upsertRequestLogBodies 注释：遥测为 at-least-once
// 投递，同 request_id 重投语义是刷新而非重复行）。修前断言与该契约
// 矛盾实锤红（重投后 bodies 为最新 payload）。现行契约：
//   - 两个表都恰好 1 行（不产生重复）
//   - bodies 内容被最新一次投递覆盖（新投递胜出）
func TestPersistRequestLog_ReingestUpsertsSameRequestID(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	// First, insert a record to establish baseline
	baseRequestID := "test-rollback-base-" + time.Now().Format("20060102-150405.000")
	baseInput := &requestLogInput{
		RequestID:     baseRequestID,
		TenantID:      "test-tenant",
		ApplicationID: intPtr(1),
		CredentialID:  intPtr(10),
		ProviderID:    intPtr(20),
		CanonicalID:   intPtr(5),
		Success:       true,
		RequestBody:   strPtrTelemetry(`{"test":"base"}`),
		ResponseBody:  strPtrTelemetry(`{"result":"base"}`),
	}
	ingester.persistRequestLog(ctx, baseInput)

	// Re-ingest the SAME request_id (upsert refresh, no duplicate row)
	duplicateInput := &requestLogInput{
		RequestID:     baseRequestID,
		TenantID:      "test-tenant",
		ApplicationID: intPtr(1),
		CredentialID:  intPtr(10),
		ProviderID:    intPtr(20),
		CanonicalID:   intPtr(5),
		Success:       true,
		RequestBody:   strPtrTelemetry(`{"test":"duplicate"}`),
		ResponseBody:  strPtrTelemetry(`{"result":"duplicate"}`),
	}
	ingester.persistRequestLog(ctx, duplicateInput)

	// Verify only ONE record exists in request_logs_hot
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs_hot WHERE request_id = $1
	`, baseRequestID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "re-ingest must not create a duplicate metadata row")

	// Verify only ONE record exists in request_logs_bodies_hot
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs_bodies_hot WHERE request_id = $1
	`, baseRequestID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "re-ingest must not create a duplicate bodies row")

	// Verify the bodies content is from the LATEST delivery (upsert wins)
	var requestBody string
	err = pool.QueryRow(ctx, `
		SELECT request_body::text FROM request_logs_bodies_hot WHERE request_id = $1
	`, baseRequestID).Scan(&requestBody)
	require.NoError(t, err)
	assert.Contains(t, requestBody, "duplicate", "latest delivery must win under the upsert contract")

	// Cleanup
	cleanupTestRequestLog(t, pool, baseRequestID)
}

// TestPersistRequestLog_NullBodies verifies that NULL request_body/response_body
// are handled correctly (written as SQL NULL in bodies table).
func TestPersistRequestLog_NullBodies(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	requestID := "test-null-bodies-" + time.Now().Format("20060102-150405.000")

	// Input with NULL bodies (simulating edge case)
	input := &requestLogInput{
		RequestID:     requestID,
		TenantID:      "test-tenant",
		ApplicationID: intPtr(1),
		CredentialID:  intPtr(10),
		ProviderID:    intPtr(20),
		CanonicalID:   intPtr(5),
		Success:       true,
		RequestBody:   nil, // NULL
		ResponseBody:  nil, // NULL
	}

	// Execute dual-write with NULL bodies
	ingester.persistRequestLog(ctx, input)

	// Verify request_logs_hot exists
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM request_logs_hot WHERE request_id = $1)
	`, requestID).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "request_logs_hot should have the record")

	// Verify request_logs_bodies_hot exists with NULL bodies
	var rbRequestBody, rbResponseBody *string
	err = pool.QueryRow(ctx, `
		SELECT request_body::text, response_body::text
		FROM request_logs_bodies_hot
		WHERE request_id = $1
	`, requestID).Scan(&rbRequestBody, &rbResponseBody)
	require.NoError(t, err, "request_logs_bodies_hot should have the record even with NULL bodies")

	assert.Nil(t, rbRequestBody, "request_body should be NULL")
	assert.Nil(t, rbResponseBody, "response_body should be NULL")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestPersistRequestLog_OutboundBodyLocation verifies that outbound_body is NOT
// written to request_logs_bodies_hot (it should remain in request_logs_hot per design).
// Note: The current requestLogInput struct doesn't have OutboundBody field, so this
// test documents the expected behavior when that field is added.
func TestPersistRequestLog_OutboundBodyLocation(t *testing.T) {
	t.Skip("OutboundBody field not yet in requestLogInput struct; test documents expected behavior")

	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	requestID := "test-outbound-body-" + time.Now().Format("20060102-150405.000")

	// When OutboundBody is added to requestLogInput, this test should verify:
	// 1. outbound_body is written to request_logs_hot
	// 2. outbound_body is NOT in request_logs_bodies_hot (table doesn't have the column)

	input := &requestLogInput{
		RequestID:     requestID,
		TenantID:      "test-tenant",
		ApplicationID: intPtr(1),
		CredentialID:  intPtr(10),
		ProviderID:    intPtr(20),
		CanonicalID:   intPtr(5),
		Success:       true,
		RequestBody:   strPtrTelemetry(`{"test":"request"}`),
		ResponseBody:  strPtrTelemetry(`{"test":"response"}`),
		// OutboundBody:  strPtrTelemetry(`{"compressed":"session"}`), // Future field
	}

	ingester.persistRequestLog(ctx, input)

	// Future verification:
	// - SELECT outbound_body FROM request_logs_hot WHERE request_id = ?
	// - Verify it's NOT NULL
	// - Verify request_logs_bodies_hot doesn't have outbound_body column

	cleanupTestRequestLog(t, pool, requestID)
}

// cleanupTestRequestLog removes test data from both tables
func cleanupTestRequestLog(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	// Delete from bodies table first (foreign key constraint if exists)
	_, err := pool.Exec(ctx, `DELETE FROM request_logs_bodies_hot WHERE request_id = $1`, requestID)
	if err != nil {
		t.Logf("cleanup bodies_hot failed (may not exist): %v", err)
	}

	// Delete from main table
	_, err = pool.Exec(ctx, `DELETE FROM request_logs_hot WHERE request_id = $1`, requestID)
	if err != nil {
		t.Logf("cleanup request_logs_hot failed: %v", err)
	}

	// Also cleanup usage_ledger_hot (written in same transaction)
	_, err = pool.Exec(ctx, `DELETE FROM usage_ledger_hot WHERE request_id = $1`, requestID)
	if err != nil {
		t.Logf("cleanup usage_ledger_hot failed: %v", err)
	}
}

// Helper functions (note: strPtr already exists in providers_refresh_test.go)
func intPtr(i int) *int {
	return &i
}

func strPtrTelemetry(s string) *string {
	return &s
}
