package maas

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreCheckBalance_SufficientBalance 测试余额充足的情况
func TestPreCheckBalance_SufficientBalance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 设置：余额=5000，信用额度=1000，策略=warn
	tenantID := setupTestTenant(t, pool, 5000, 1000, "warn")

	// 执行：预估成本=100
	decision, err := svc.PreCheckBalance(ctx, tenantID, 100)

	// 验证
	require.NoError(t, err)
	assert.True(t, decision.Allow, "should allow request with sufficient balance")
	assert.Equal(t, "allow", decision.Policy)
	assert.Equal(t, int64(5000), decision.BalanceBefore)
	assert.Equal(t, int64(1000), decision.CreditLimit)
	assert.Equal(t, int64(100), decision.EstimatedCost)
	assert.Contains(t, decision.Reason, "sufficient balance")
}

// TestPreCheckBalance_InDebtWarnMode 测试余额不足但在信用额度内（warn模式）
func TestPreCheckBalance_InDebtWarnMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 设置：余额=-200，信用额度=1000，策略=warn
	tenantID := setupTestTenant(t, pool, -200, 1000, "warn")

	// 执行：预估成本=100
	decision, err := svc.PreCheckBalance(ctx, tenantID, 100)

	// 验证
	require.NoError(t, err)
	assert.True(t, decision.Allow, "should allow request in warn mode within credit limit")
	assert.Equal(t, "warn", decision.Policy)
	assert.Equal(t, int64(-200), decision.BalanceBefore)
	assert.Equal(t, int64(800), decision.AvailableCredit) // -200 + 1000
	assert.Contains(t, decision.Reason, "using credit line")
}

// TestPreCheckBalance_InDebtBlockMode 测试余额不足（block模式）
func TestPreCheckBalance_InDebtBlockMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 设置：余额=-200，信用额度=1000，策略=block
	tenantID := setupTestTenant(t, pool, -200, 1000, "block")

	// 执行：预估成本=100
	decision, err := svc.PreCheckBalance(ctx, tenantID, 100)

	// 验证
	require.NoError(t, err)
	assert.False(t, decision.Allow, "should block request in block mode when balance negative")
	assert.Equal(t, "block", decision.Policy)
	assert.Equal(t, int64(-200), decision.BalanceBefore)
	assert.Contains(t, decision.Reason, "policy=block")
}

// TestPreCheckBalance_OverCreditLimit 测试超过信用额度（强制阻断）
func TestPreCheckBalance_OverCreditLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 设置：余额=-1200，信用额度=1000，策略=warn
	tenantID := setupTestTenant(t, pool, -1200, 1000, "warn")

	// 执行：预估成本=100
	decision, err := svc.PreCheckBalance(ctx, tenantID, 100)

	// 验证
	require.NoError(t, err)
	assert.False(t, decision.Allow, "should block when over credit limit")
	assert.Equal(t, "block", decision.Policy)
	assert.Equal(t, int64(-200), decision.AvailableCredit) // -1200 + 1000
	assert.Contains(t, decision.Reason, "over credit limit")
}

// TestPreCheckBalance_NonexistentTenant 测试租户不存在（fail-open）
func TestPreCheckBalance_NonexistentTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 执行：查询不存在的租户
	decision, err := svc.PreCheckBalance(ctx, "nonexistent-tenant-id", 100)

	// 验证：fail-open策略
	require.NoError(t, err)
	assert.True(t, decision.Allow, "should allow request when tenant not found (fail-open)")
	assert.Equal(t, "warn", decision.Policy)
	assert.Contains(t, decision.Reason, "fail-open")
}

// TestPreCheckBalance_MaaSDisabled 测试MaaS未启用
func TestPreCheckBalance_MaaSDisabled(t *testing.T) {
	ctx := context.Background()
	svc := &Service{} // pool未设置，表示MaaS未启用

	decision, err := svc.PreCheckBalance(ctx, "any-tenant", 100)

	require.NoError(t, err)
	assert.True(t, decision.Allow)
	assert.Equal(t, "skipped", decision.Policy)
	assert.Contains(t, decision.Reason, "maas disabled")
}

// TestPreCheckBalance_EdgeCases 测试边界情况
func TestPreCheckBalance_EdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	t.Run("zero balance exactly", func(t *testing.T) {
		tenantID := setupTestTenant(t, pool, 0, 1000, "warn")
		decision, err := svc.PreCheckBalance(ctx, tenantID, 100)
		require.NoError(t, err)
		assert.True(t, decision.Allow, "zero balance should be treated as sufficient")
		assert.Equal(t, "allow", decision.Policy)
	})

	t.Run("exactly at credit limit", func(t *testing.T) {
		tenantID := setupTestTenant(t, pool, -1000, 1000, "warn")
		decision, err := svc.PreCheckBalance(ctx, tenantID, 0)
		require.NoError(t, err)
		assert.True(t, decision.Allow, "exactly at limit with zero cost should allow")
	})

	t.Run("one credit over limit", func(t *testing.T) {
		tenantID := setupTestTenant(t, pool, -1001, 1000, "warn")
		decision, err := svc.PreCheckBalance(ctx, tenantID, 1)
		require.NoError(t, err)
		assert.False(t, decision.Allow, "even one credit over should block")
	})

	t.Run("zero credit limit", func(t *testing.T) {
		tenantID := setupTestTenant(t, pool, -100, 0, "warn")
		decision, err := svc.PreCheckBalance(ctx, tenantID, 10)
		require.NoError(t, err)
		assert.False(t, decision.Allow, "negative balance with zero limit should block")
	})

	t.Run("very large cost estimate", func(t *testing.T) {
		tenantID := setupTestTenant(t, pool, 1000000, 1000, "warn")
		decision, err := svc.PreCheckBalance(ctx, tenantID, 10000000)
		require.NoError(t, err)
		assert.False(t, decision.Allow, "cost larger than balance should block")
	})
}

// TestUpdateTenantCreditPolicy 测试策略更新
func TestUpdateTenantCreditPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	tenantID := setupTestTenant(t, pool, 1000, 1000, "warn")

	// 更新策略为block
	err := svc.UpdateTenantCreditPolicy(ctx, tenantID, "block")
	require.NoError(t, err)

	// 验证更新成功
	_, _, policy, err := svc.GetTenantCreditInfo(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, "block", policy)

	// 测试无效策略
	err = svc.UpdateTenantCreditPolicy(ctx, tenantID, "invalid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid policy")
}

// TestUpdateTenantCreditLimit 测试信用额度更新
func TestUpdateTenantCreditLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	tenantID := setupTestTenant(t, pool, 1000, 1000, "warn")

	// 更新额度为2000
	err := svc.UpdateTenantCreditLimit(ctx, tenantID, 2000)
	require.NoError(t, err)

	// 验证更新成功
	_, limit, _, err := svc.GetTenantCreditInfo(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), limit)

	// 测试负数额度
	err = svc.UpdateTenantCreditLimit(ctx, tenantID, -100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid credit limit")
}

// TestBillingPrecheckLog 测试预检日志记录
func TestBillingPrecheckLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	tenantID := setupTestTenant(t, pool, 5000, 1000, "warn")

	// 执行预检
	_, err := svc.PreCheckBalance(ctx, tenantID, 100)
	require.NoError(t, err)

	// 等待异步日志写入（goroutine）
	time.Sleep(100 * time.Millisecond)

	// 验证日志已写入
	var count int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM billing_precheck_log
		WHERE tenant_id = $1
	`, tenantID).Scan(&count)
	require.NoError(t, err)
	assert.Greater(t, count, 0, "precheck log should be recorded")

	// 验证日志内容
	var decision, policy, reason string
	var balance, limit, cost int64
	err = pool.QueryRow(ctx, `
		SELECT decision, policy, reason, 
		       balance_before_credits, credit_limit_credits, estimated_cost_credits
		FROM billing_precheck_log
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&decision, &policy, &reason, &balance, &limit, &cost)
	require.NoError(t, err)
	assert.Equal(t, "allow", decision)
	assert.Equal(t, "allow", policy)
	assert.Equal(t, int64(5000), balance)
	assert.Equal(t, int64(1000), limit)
	assert.Equal(t, int64(100), cost)
}

// ─────────────────────────────────────────────────────────
// Test Helpers
// ─────────────────────────────────────────────────────────

// setupTestDB 初始化测试数据库连接
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	// 使用环境变量或默认测试数据库
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping database tests")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// setupTestTenant 创建测试租户并设置余额、信用额度、策略
func setupTestTenant(t *testing.T, pool *pgxpool.Pool, balance, creditLimit int64, policy string) string {
	t.Helper()

	ctx := context.Background()
	tenantID := fmt.Sprintf("test-tenant-%d", time.Now().UnixNano())

	// 插入租户钱包记录
	_, err := pool.Exec(ctx, `
		INSERT INTO tenant_credit_wallets 
		(tenant_id, balance_credits, credit_limit_credits, credit_policy)
		VALUES ($1, $2, $3, $4)
	`, tenantID, balance, creditLimit, policy)
	require.NoError(t, err)

	t.Cleanup(func() {
		// 清理测试数据
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM billing_precheck_log WHERE tenant_id = $1
		`, tenantID)
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM tenant_credit_wallets WHERE tenant_id = $1
		`, tenantID)
	})

	return tenantID
}
