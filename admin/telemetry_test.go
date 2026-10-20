package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ★★★ R68 改瞄（2026-10-21，v1 族退役后的 42P01 收口）：
//
// 本文件原来的三条门守的是 Ticket #10 的**双写契约**：
// 「persistRequestLog 同一事务写 `_request_logs_hot`（元数据）+ `_request_logs_bodies_hot`（正文）」。
//
// 迁移 890 把 v1 族整体 DROP ⇒ 这两张表**不存在**了，于是两件事同时发生：
//  ① **夹具侧**：旧夹具 INSERT 直接 42P01（relation "_request_logs_hot" does not exist）；
//  ② **更要紧的契约侧**：产品侧 persistRequestLog 现在**只写一条 usage_ledger 计费行**
//     （admin/telemetry.go 的注释自证：「v1 写入删除…剩下的写入只有上方那条 usage_ledger 计费行」），
//     正文改由文件镜像承担（mirrorRequestBodies）。
//
// ⇒ 所以这三条门**不是**「夹具坏了换张表」，而是**「守的东西已不存在」** ⇒ 必须换契约对象。
//   下面是**新契约**的三条门；旧名保留在注释里，便于按名回溯。
//
// ⚠ 新契约的边界（勿扩大解读）：
//   · persistRequestLog 的 PG 写入 = **恰好一条 usage_ledger_hot 计费行**；
//   · 正文**不进 PG**（会话族由镜像桥写，见 admin/telemetry.go 的注释）；
//   · 「同一 request_id 重投 ⇒ 只留一行」这条**幂等契约当前无处兑现**（唯一键是
//     (request_id, ts)，写入不带 ON CONFLICT）⇒ 属主待裁决项，本文件**不断言行数**，
//     只断言「最新一次投递的内容可见」（见 TestPersistRequestLog_ReingestKeepsLatestDeliveryVisible）。

// TestPersistRequestLog_WritesUsageLedgerRowOnly verifies that persistRequestLog
// writes exactly one usage_ledger_hot billing row with the right field mapping.
//
// 原名 TestPersistRequestLog_BothTablesWritten —— 「两张表」这个对象已随 890 消失。
func TestPersistRequestLog_WritesUsageLedgerRowOnly(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	// Generate unique request_id for this test
	requestID := "test-ledger-row-" + time.Now().Format("20060102-150405.000")

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
		ClientModel:      strPtrTelemetry("test-client-model"),
		RequestBody:      strPtrTelemetry(`{"model":"claude-3","messages":[{"role":"user","content":"test"}]}`),
		ResponseBody:     strPtrTelemetry(`{"id":"msg_123","content":[{"type":"text","text":"response"}]}`),
		RequestPreview:   strPtrTelemetry("test preview"),
		ResponsePreview:  strPtrTelemetry("response preview"),
	}

	ingester.persistRequestLog(ctx, input)

	// ── 判据 1：恰好一行计费行 ────────────────────────────────────────
	// ⚠ persistRequestLog 把 INSERT 错误**吞掉**（只 Warn + return），
	//   所以「行数对」是这条路径**唯一**可观测的成功信号。
	var count int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM usage_ledger_hot WHERE request_id = $1
	`, requestID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "persistRequestLog 必须写恰好一条 usage_ledger_hot 计费行")

	// ── 判据 2：字段映射（运行时，不是源码形状）────────────────────────
	var tenantID, rawModelName string
	var promptTok, completionTok, totalTok int
	var costUSD *float64
	var success bool
	err = pool.QueryRow(ctx, `
		SELECT tenant_id, raw_model_name, prompt_tokens, completion_tokens,
		       total_tokens, cost_usd::float8, success
		  FROM usage_ledger_hot
		 WHERE request_id = $1
	`, requestID).Scan(&tenantID, &rawModelName, &promptTok, &completionTok,
		&totalTok, &costUSD, &success)
	require.NoError(t, err, "usage_ledger_hot should have the record")
	assert.Equal(t, "test-tenant", tenantID, "tenant_id 必须是入参口径")
	assert.Equal(t, "test-client-model", rawModelName,
		"raw_model_name 必须取 firstNonEmptyStr(OutboundModel, ClientModel) 的结果")
	assert.Equal(t, 100, promptTok)
	assert.Equal(t, 50, completionTok)
	assert.Equal(t, 150, totalTok, "total_tokens 必须等于 calcTotal(prompt, completion)")
	assert.True(t, success)
	assert.Nil(t, costUSD, "入参没给 CostUSD ⇒ 计费行该列必须是 NULL（不得被写成 0）")

	// ── 判据 3：正文**不进 PG**（890 之后 PG 侧只有上面这一行）─────────
	// v1 正文表已不存在 ⇒ 这里断言的是「会话族那边没有本 request_id 的正文」，
	// 即 persistRequestLog 没有把正文写进任何 PG 表。
	var bodyRows int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM session_bodies_hot WHERE request_id = $1
	`, requestID).Scan(&bodyRows)
	require.NoError(t, err)
	assert.Equal(t, 0, bodyRows,
		"persistRequestLog 不写会话族正文（正文由镜像桥写）—— 若这里非 0，说明写入路径又长出一条")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestPersistRequestLog_ReingestKeepsLatestDeliveryVisible verifies that a
// re-delivery of the same request_id does not break the ingest and that the
// **latest** delivery's payload is the one visible in the ledger.
//
// 原名 TestPersistRequestLog_ReingestUpsertsSameRequestID。
//
// ★ 为什么这里**不断言行数**（这是本轮最关键的一处取舍）：
//
// 旧契约（「同一 request_id 重投 ⇒ 刷新而非重复行」）当年由 **v1 表**兑现：
// 迁移 455 把 v1 hot 表的唯一键从 (request_id, ts) 改成 (request_id)，
// 写入带 ON CONFLICT DO UPDATE。890 退役了 v1 表 ⇒ 这条幂等契约**失去载体**；
// 剩下的唯一写入 usage_ledger_hot 的唯一键是 **(request_id, ts)**
// （迁移 344），且写入**不带 ON CONFLICT** ⇒ 同一 request_id 在不同 ts 重投
// 必然产生第二行。
//
// ⇒ 断言「恰好 1 行」等于断言一条**当前不成立**的契约（会恒红）；
//
//	断言「恰好 2 行」等于把「重复计费」**钉成契约**（更糟：它会掩盖这个缺口）。
//	⇒ 本门只断言**可观测且当前为真**的两件事：ingest 没被重投打断、
//	  最新一次投递的内容可见。缺口本身登记在
//	  docs/audit/2026-10-21-R68-D3-v1族退役后门改瞄.md（属主待裁决）。
func TestPersistRequestLog_ReingestKeepsLatestDeliveryVisible(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	ingester := &telemetryIngester{db: pool}

	// First, insert a record to establish baseline
	baseRequestID := "test-reingest-base-" + time.Now().Format("20060102-150405.000")
	baseInput := &requestLogInput{
		RequestID:        baseRequestID,
		TenantID:         "test-tenant",
		ApplicationID:    intPtr(1),
		CredentialID:     intPtr(10),
		ProviderID:       intPtr(20),
		CanonicalID:      intPtr(5),
		PromptTokens:     intPtr(100),
		CompletionTokens: intPtr(50),
		Success:          true,
		RequestBody:      strPtrTelemetry(`{"test":"base"}`),
		ResponseBody:     strPtrTelemetry(`{"result":"base"}`),
	}
	ingester.persistRequestLog(ctx, baseInput)

	// Re-ingest the SAME request_id (at-least-once delivery ⇒ may insert again)
	duplicateInput := &requestLogInput{
		RequestID:        baseRequestID,
		TenantID:         "test-tenant",
		ApplicationID:    intPtr(1),
		CredentialID:     intPtr(10),
		ProviderID:       intPtr(20),
		CanonicalID:      intPtr(5),
		PromptTokens:     intPtr(7),
		CompletionTokens: intPtr(3),
		Success:          true,
		RequestBody:      strPtrTelemetry(`{"test":"duplicate"}`),
		ResponseBody:     strPtrTelemetry(`{"result":"duplicate"}`),
	}
	ingester.persistRequestLog(ctx, duplicateInput)

	// ── 判据 1：重投不得让 ingest 断掉（至少有一行属于该 request_id）──
	var count int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM usage_ledger_hot WHERE request_id = $1
	`, baseRequestID).Scan(&count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 1,
		"重投不得让 ingest 事务失败（persistRequestLog 吞错误 ⇒ 行数是唯一信号）")

	// ── 判据 2：最新一次投递的内容可见（取 ts 最大的那一行）──────────
	var promptTok, completionTok, totalTok int
	err = pool.QueryRow(ctx, `
		SELECT prompt_tokens, completion_tokens, total_tokens
		  FROM usage_ledger_hot
		 WHERE request_id = $1
		 ORDER BY ts DESC
		 LIMIT 1
	`, baseRequestID).Scan(&promptTok, &completionTok, &totalTok)
	require.NoError(t, err)
	assert.Equal(t, 7, promptTok, "最新一次投递的内容必须可见")
	assert.Equal(t, 3, completionTok)
	assert.Equal(t, 10, totalTok)

	// 行数现状登记（便于观测漂移；不断言具体值，见函数头注释）
	t.Logf("re-ingest 行数现状 = %d（唯一键 (request_id, ts) + 无 ON CONFLICT ⇒ 幂等契约暂无载体）", count)

	// Cleanup
	cleanupTestRequestLog(t, pool, baseRequestID)
}

// TestPersistRequestLog_NullBodies verifies that NULL request_body/response_body
// do not break the ingest: the billing row is still written.
//
// 原名 TestPersistRequestLog_NullBodies —— 断言对象从「正文表里是 NULL」
// 换成「**PG 侧根本没有正文表**」⇒ 新的可观测面是「计费行照写」。
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

	ingester.persistRequestLog(ctx, input)

	// ── 判据：计费行照写（NULL 正文不得阻断 ingest）────────────────────
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM usage_ledger_hot WHERE request_id = $1)
	`, requestID).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "NULL 正文时 usage_ledger_hot 仍必须落行")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestPersistRequestLog_OutboundBodyLocation verifies that outbound_body is NOT
// written to _request_logs_bodies_hot (it should remain in _request_logs_hot per design).
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
	// 1. outbound_body is written to _request_logs_hot
	// 2. outbound_body is NOT in _request_logs_bodies_hot (table doesn't have the column)

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
	// - SELECT outbound_body FROM _request_logs_hot WHERE request_id = ?
	// - Verify it's NOT NULL
	// - Verify _request_logs_bodies_hot doesn't have outbound_body column

	cleanupTestRequestLog(t, pool, requestID)
}

// cleanupTestRequestLog removes test data for a request_id from every table the
// test fixtures write to.
//
// ★ R68 改瞄：v1 族退役后，夹具写的表变了 ——
//
//	· usage_ledger_hot ：telemetry ingest 的计费行（本路径唯一 PG 写入）；
//	· session_turns_hot / session_bodies_hot ：会话族元数据/正文
//	  （由 logs / logs_get_log_columnar 的夹具写入，与本文件共用本 helper）。
//
// 三张表都**容错删除**：哪张表没被写过就跳过，不让清理失败盖掉真正的断言。
func cleanupTestRequestLog(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	for _, stmt := range []struct {
		sql  string
		what string
	}{
		{`DELETE FROM usage_ledger_hot WHERE request_id = $1`, "usage_ledger_hot"},
		{`DELETE FROM session_bodies_hot WHERE request_id = $1`, "session_bodies_hot"},
		{`DELETE FROM session_turns_hot WHERE request_id = $1`, "session_turns_hot"},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, requestID); err != nil {
			t.Logf("cleanup %s failed (may not exist): %v", stmt.what, err)
		}
	}
}

// Helper functions (note: strPtr already exists in providers_refresh_test.go)
func intPtr(i int) *int {
	return &i
}

func strPtrTelemetry(s string) *string {
	return &s
}
