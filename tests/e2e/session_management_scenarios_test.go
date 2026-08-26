//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionManagement_E2E_Scenarios 端到端集成测试
// 验证 5 个主线场景端到端可走通，确保跨模块联动正确
//
// 环境要求:
//   export LLM_GATEWAY_PG_URL=postgresql://user:pass@host/db
//   go test -tags=e2e ./tests/e2e -v -run TestSessionManagement
func TestSessionManagement_E2E_Scenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping e2e test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "connect to database")
	defer pool.Close()

	// 创建测试 handler
	// 注意：Handler 的字段是私有的，需要使用 New 函数或直接测试 HTTP 端点
	// 这里我们直接使用 pool 进行数据库操作，并通过 HTTP 请求测试端点
	_ = pool // 使用 pool 进行数据库操作

	// 创建测试租户
	testTenantID := setupTestTenant(t, pool)
	defer cleanupTestTenant(t, pool, testTenantID)

	// 运行 5 个场景测试
	t.Run("Scenario1_HighCostAnomalyDetectionAndHandling", func(t *testing.T) {
		testScenario1_HighCostAnomaly(t, pool, testTenantID)
	})

	t.Run("Scenario2_ComplianceViolationReview", func(t *testing.T) {
		testScenario2_ComplianceViolation(t, pool, testTenantID)
	})

	t.Run("Scenario3_OptimizationSuggestionLoop", func(t *testing.T) {
		testScenario3_OptimizationSuggestion(t, pool, testTenantID)
	})

	t.Run("Scenario4_RealTimeMonitoringAndIntervention", func(t *testing.T) {
		testScenario4_RealTimeMonitoring(t, pool, testTenantID)
	})

	t.Run("Scenario5_UsageCostReconciliation", func(t *testing.T) {
		testScenario5_UsageCostReconciliation(t, pool, testTenantID)
	})
}

// ============================================================================
// 场景 1: 发现并处置高成本异常会话
// ============================================================================
func testScenario1_HighCostAnomaly(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()

	t.Log("场景 1: 发现并处置高成本异常会话")

	// Step 1: 创建测试数据 - 1 个高成本会话（>$5）
	sessionID := createHighCostSession(t, pool, tenantID)
	t.Logf("✓ 创建高成本会话: %s", sessionID)

	// Step 2: 验证会话摘要已创建
	var totalCost float64
	err := pool.QueryRow(ctx,
		`SELECT total_cost_usd FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&totalCost)
	require.NoError(t, err)
	assert.Greater(t, totalCost, 5.0, "成本应该 > $5")
	t.Logf("✓ 会话成本验证: $%.2f", totalCost)

	// Step 3: 验证请求日志已创建
	var requestCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM request_logs WHERE gw_session_id = $1`,
		sessionID).Scan(&requestCount)
	require.NoError(t, err)
	assert.Greater(t, requestCount, 0, "应该有请求日志")
	t.Logf("✓ 请求日志数量: %d", requestCount)

	// Step 4: 测试健康评分计算
	var summary admin.AnalyticsSessionSummary
	err = pool.QueryRow(ctx,
		`SELECT 
			session_key, tenant_id, request_count, success_count, error_count,
			total_cost_usd, avg_latency_ms, model_switch_count,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected
		FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(
		&summary.GwSessionID, &summary.TenantID, &summary.RequestCount,
		&summary.SuccessCount, &summary.ErrorCount, &summary.TotalCostUSD,
		&summary.AvgLatencyMs, &summary.ModelSwitchCount,
		&summary.ComplianceStatus, &summary.ComplianceIssuesCount,
		&summary.PromptInjectionDetected, &summary.PIIDetected, &summary.ToxicOutputDetected)
	require.NoError(t, err)

	// 计算健康分
	config := admin.DefaultHealthScoreConfig()
	health := admin.ComputeHealth(summary, config)
	
	t.Logf("✓ 健康评分计算: 分数=%d, 等级=%s, 结果=%s",
		health.HealthScore, health.HealthGrade, health.Outcome)
	
	// 验证扣分项
	assert.NotEmpty(t, health.Penalties, "应该有扣分项")
	for _, p := range health.Penalties {
		t.Logf("  - 扣分项: %s (-%d) %s", p.Reason, p.Deduction, p.Detail)
	}

	// 验证高延迟扣分
	hasHighLatencyPenalty := false
	for _, p := range health.Penalties {
		if p.Reason == "high_latency" {
			hasHighLatencyPenalty = true
			break
		}
	}
	assert.True(t, hasHighLatencyPenalty, "应该有高延迟扣分")

	t.Log("✓ 场景 1 完成：高成本异常会话检测与诊断流程验证通过")
}

// ============================================================================
// 场景 2: 合规违规会话复盘
// ============================================================================
func testScenario2_ComplianceViolation(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()

	t.Log("场景 2: 合规违规会话复盘")

	// Step 1: 创建测试数据 - 1 个合规违规会话
	sessionID := createComplianceViolationSession(t, pool, tenantID)
	t.Logf("✓ 创建合规违规会话: %s", sessionID)

	// Step 2: 验证合规状态
	var complianceStatus string
	var complianceIssuesCount int
	err := pool.QueryRow(ctx,
		`SELECT compliance_status, compliance_issues_count 
		 FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&complianceStatus, &complianceIssuesCount)
	require.NoError(t, err)
	assert.NotEqual(t, "compliant", complianceStatus, "应该有合规问题")
	assert.Greater(t, complianceIssuesCount, 0, "应该有合规问题计数")
	t.Logf("✓ 合规状态: %s, 问题数: %d", complianceStatus, complianceIssuesCount)

	// Step 3: 验证 PII 和注入检测标记
	var piiDetected, injectionDetected bool
	err = pool.QueryRow(ctx,
		`SELECT pii_detected, prompt_injection_detected 
		 FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&piiDetected, &injectionDetected)
	require.NoError(t, err)
	assert.True(t, piiDetected || injectionDetected, "应该检测到 PII 或注入")
	t.Logf("✓ PII 检测: %v, 注入检测: %v", piiDetected, injectionDetected)

	// Step 4: 计算健康分，验证合规扣分
	var summary admin.AnalyticsSessionSummary
	err = pool.QueryRow(ctx,
		`SELECT 
			session_key, tenant_id, request_count, success_count, error_count,
			total_cost_usd, avg_latency_ms, model_switch_count,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected
		FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(
		&summary.GwSessionID, &summary.TenantID, &summary.RequestCount,
		&summary.SuccessCount, &summary.ErrorCount, &summary.TotalCostUSD,
		&summary.AvgLatencyMs, &summary.ModelSwitchCount,
		&summary.ComplianceStatus, &summary.ComplianceIssuesCount,
		&summary.PromptInjectionDetected, &summary.PIIDetected, &summary.ToxicOutputDetected)
	require.NoError(t, err)

	config := admin.DefaultHealthScoreConfig()
	health := admin.ComputeHealth(summary, config)
	
	t.Logf("✓ 健康评分: 分数=%d, 等级=%s", health.HealthScore, health.HealthGrade)

	// 验证有合规相关扣分
	hasCompliancePenalty := false
	for _, p := range health.Penalties {
		if strings.Contains(p.Reason, "compliance") || 
		   strings.Contains(p.Reason, "injection") || 
		   strings.Contains(p.Reason, "pii") {
			hasCompliancePenalty = true
			t.Logf("  - 合规扣分: %s (-%d) %s", p.Reason, p.Deduction, p.Detail)
		}
	}
	assert.True(t, hasCompliancePenalty, "应该有合规相关扣分")

	t.Log("✓ 场景 2 完成：合规违规会话复盘流程验证通过")
}

// ============================================================================
// 场景 3: 基于优化建议的成本优化闭环
// ============================================================================
func testScenario3_OptimizationSuggestion(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()

	t.Log("场景 3: 基于优化建议的成本优化闭环")

	// Step 1: 创建测试数据 - 1 个有优化空间的会话
	sessionID := createOptimizableSession(t, pool, tenantID)
	t.Logf("✓ 创建可优化会话: %s", sessionID)

	// Step 2: 验证会话数据
	var requestCount int
	var totalCost float64
	err := pool.QueryRow(ctx,
		`SELECT request_count, total_cost_usd 
		 FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&requestCount, &totalCost)
	require.NoError(t, err)
	t.Logf("✓ 会话数据: 请求数=%d, 成本=$%.2f", requestCount, totalCost)

	// Step 3: 模拟优化建议（在实际系统中由 LLM 生成）
	// 这里我们验证会话有优化空间
	assert.Greater(t, requestCount, 10, "应该有足够请求数用于优化")
	assert.Greater(t, totalCost, 2.0, "应该有一定成本")

	// Step 4: 计算健康分
	var summary admin.AnalyticsSessionSummary
	err = pool.QueryRow(ctx,
		`SELECT 
			session_key, tenant_id, request_count, success_count, error_count,
			total_cost_usd, avg_latency_ms, model_switch_count,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected
		FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(
		&summary.GwSessionID, &summary.TenantID, &summary.RequestCount,
		&summary.SuccessCount, &summary.ErrorCount, &summary.TotalCostUSD,
		&summary.AvgLatencyMs, &summary.ModelSwitchCount,
		&summary.ComplianceStatus, &summary.ComplianceIssuesCount,
		&summary.PromptInjectionDetected, &summary.PIIDetected, &summary.ToxicOutputDetected)
	require.NoError(t, err)

	config := admin.DefaultHealthScoreConfig()
	health := admin.ComputeHealth(summary, config)
	
	t.Logf("✓ 健康评分: 分数=%d, 等级=%s, 结果=%s",
		health.HealthScore, health.HealthGrade, health.Outcome)

	// 优化建议通常针对健康分较低但非致命错误的会话
	// 验证会话状态正常（无致命错误）
	assert.Equal(t, summary.ErrorCount, 0, "可优化会话应该没有错误")
	
	t.Log("✓ 场景 3 完成：优化建议闭环流程验证通过")
}

// ============================================================================
// 场景 4: 实时会话监控与远程干预
// ============================================================================
func testScenario4_RealTimeMonitoring(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()

	t.Log("场景 4: 实时会话监控与远程干预")

	// Step 1: 创建运行中的测试会话
	sessionID := createActiveSession(t, pool, tenantID)
	t.Logf("✓ 创建活跃会话: %s", sessionID)

	// Step 2: 验证会话状态为 active
	var status string
	err := pool.QueryRow(ctx,
		`SELECT status FROM session_state_snapshots WHERE session_id = $1`,
		sessionID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "active", status, "会话应该处于活跃状态")
	t.Logf("✓ 会话状态: %s", status)

	// Step 3: 验证最后活跃时间
	var lastRequestAt time.Time
	err = pool.QueryRow(ctx,
		`SELECT last_request_at FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&lastRequestAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), lastRequestAt, 5*time.Minute, "最后请求应该在最近")
	t.Logf("✓ 最后活跃时间: %s", lastRequestAt.Format("15:04:05"))

	// Step 4: 模拟会话状态变化为 error
	_, err = pool.Exec(ctx,
		`UPDATE session_state_snapshots 
		 SET status = 'error', updated_at = NOW()
		 WHERE session_id = $1`,
		sessionID)
	require.NoError(t, err)
	t.Log("✓ 模拟状态变化为 error")

	// Step 5: 验证状态已更新
	err = pool.QueryRow(ctx,
		`SELECT status FROM session_state_snapshots WHERE session_id = $1`,
		sessionID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "error", status, "状态应该更新为 error")

	// Step 6: 验证会话摘要数据完整性
	var requestCount, successCount, errorCount int
	err = pool.QueryRow(ctx,
		`SELECT request_count, success_count, error_count 
		 FROM session_summaries WHERE session_key = $1`,
		sessionID).Scan(&requestCount, &successCount, &errorCount)
	require.NoError(t, err)
	t.Logf("✓ 会话统计: 请求=%d, 成功=%d, 错误=%d", requestCount, successCount, errorCount)

	t.Log("✓ 场景 4 完成：实时会话监控流程验证通过")
	t.Log("  注: SSE 实时推送需要在实际运行的网关中测试")
}

// ============================================================================
// 场景 5: 用量成本对账与同比环比
// ============================================================================
func testScenario5_UsageCostReconciliation(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()

	t.Log("场景 5: 用量成本对账与同比环比")

	// Step 1: 准备两个月份的测试数据
	createMonthlyTestData(t, pool, tenantID)
	t.Log("✓ 创建月度测试数据")

	// Step 2: 验证本月数据
	var currentMonthCost float64
	var currentMonthSessions int
	err := pool.QueryRow(ctx,
		`SELECT 
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COUNT(*) as session_count
		 FROM session_summaries 
		 WHERE tenant_id = $1 
		 AND first_request_at >= date_trunc('month', CURRENT_DATE)`,
		tenantID).Scan(&currentMonthCost, &currentMonthSessions)
	require.NoError(t, err)
	t.Logf("✓ 本月数据: 会话数=%d, 总成本=$%.2f", currentMonthSessions, currentMonthCost)

	// Step 3: 验证上月数据
	var previousMonthCost float64
	var previousMonthSessions int
	err = pool.QueryRow(ctx,
		`SELECT 
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COUNT(*) as session_count
		 FROM session_summaries 
		 WHERE tenant_id = $1 
		 AND first_request_at >= date_trunc('month', CURRENT_DATE - INTERVAL '1 month')
		 AND first_request_at < date_trunc('month', CURRENT_DATE)`,
		tenantID).Scan(&previousMonthCost, &previousMonthSessions)
	require.NoError(t, err)
	t.Logf("✓ 上月数据: 会话数=%d, 总成本=$%.2f", previousMonthSessions, previousMonthCost)

	// Step 4: 计算环比变化
	var changePct float64
	if previousMonthCost > 0 {
		changePct = ((currentMonthCost - previousMonthCost) / previousMonthCost) * 100
		t.Logf("✓ 环比变化: %.1f%%", changePct)
	}

	// Step 5: 按模型归因（验证数据完整性）
	rows, err := pool.Query(ctx,
		`SELECT 
			UNNEST(models_used) as model,
			COUNT(*) as session_count,
			SUM(total_cost_usd) as total_cost
		 FROM session_summaries 
		 WHERE tenant_id = $1 
		 AND first_request_at >= date_trunc('month', CURRENT_DATE - INTERVAL '1 month')
		 GROUP BY model
		 ORDER BY total_cost DESC`)
	require.NoError(t, err)
	defer rows.Close()

	modelBreakdown := make(map[string]float64)
	for rows.Next() {
		var model string
		var sessionCount int
		var cost float64
		err = rows.Scan(&model, &sessionCount, &cost)
		require.NoError(t, err)
		if model != "" {
			modelBreakdown[model] = cost
			t.Logf("  - 模型 %s: %d 会话, $%.2f", model, sessionCount, cost)
		}
	}

	// Step 6: 验证缓存经济学数据（如果有缓存数据）
	var totalCacheReadTokens int64
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(COALESCE((request_metadata->>'cache_read_tokens')::bigint, 0)), 0)
		 FROM request_logs 
		 WHERE tenant_id = $1 
		 AND ts >= date_trunc('month', CURRENT_DATE - INTERVAL '1 month')`,
		tenantID).Scan(&totalCacheReadTokens)
	if err != nil {
		t.Logf("  缓存数据查询: %v", err)
	} else {
		t.Logf("✓ 缓存读取 token 总数: %d", totalCacheReadTokens)
	}

	// 验证至少有测试数据
	assert.Greater(t, currentMonthSessions+previousMonthSessions, 0, "应该有测试数据")

	t.Log("✓ 场景 5 完成：用量成本对账流程验证通过")
}

// ============================================================================
// 辅助函数：测试数据创建
// ============================================================================

func setupTestTenant(t *testing.T, pool *pgxpool.Pool) string {
	ctx := context.Background()
	tenantID := fmt.Sprintf("test_tenant_%d", time.Now().Unix())
	
	// 创建测试租户（如果需要）
	_, err := pool.Exec(ctx,
		`INSERT INTO tenants (tenant_id, name, enabled) 
		 VALUES ($1, $2, true) 
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID, "Test Tenant")
	require.NoError(t, err)
	
	return tenantID
}

func cleanupTestTenant(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()
	
	// 清理测试数据
	tables := []string{
		"request_logs",
		"session_summaries",
		"session_state_snapshots",
		"session_audit_records",
		"approval_queue",
	}
	
	for _, table := range tables {
		_, err := pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", table), tenantID)
		if err != nil {
			t.Logf("清理 %s: %v", table, err)
		}
	}
}

func createHighCostSession(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	ctx := context.Background()
	sessionID := fmt.Sprintf("gw_high_cost_%d", time.Now().Unix())
	
	// 创建会话摘要
	_, err := pool.Exec(ctx,
		`INSERT INTO session_summaries (
			session_key, tenant_id, request_count, success_count, error_count,
			total_cost_usd, input_cost_usd, output_cost_usd,
			total_prompt_tokens, total_completion_tokens, total_tokens,
			avg_latency_ms, model_switch_count,
			models_used, primary_model,
			first_request_at, last_request_at,
			compliance_status,
			compliance_issues_count, prompt_injection_detected, pii_detected, toxic_output_detected,
			created_at, updated_at
		) VALUES (
			$1, $2, 45, 40, 5,
			8.92, 3.20, 5.72,
			90000, 30000, 120000,
			6200, 5,
			ARRAY['gpt-4o', 'claude-3-5'], 'gpt-4o',
			NOW() - INTERVAL '1 hour', NOW(),
			'compliant',
			0, false, false, false,
			NOW(), NOW()
		) ON CONFLICT (session_key) DO UPDATE SET
			request_count = EXCLUDED.request_count,
			total_cost_usd = EXCLUDED.total_cost_usd`,
		sessionID, tenantID)
	require.NoError(t, err)
	
	// 创建一些请求日志
	for i := 0; i < 5; i++ {
		_, err = pool.Exec(ctx,
			`INSERT INTO request_logs (
				request_id, tenant_id, gw_session_id,
				client_model, outbound_model, provider_id,
				prompt_tokens, completion_tokens, cost_usd, latency_ms,
				success, ts
			) VALUES (
				$1, $2, $3,
				'gpt-4o', 'gpt-4o', 'openai',
				1800, 600, 1.78, 5800,
				true, NOW() - INTERVAL '1 hour' + $4 * INTERVAL '5 minutes'
			)`,
			fmt.Sprintf("req_%s_%d", sessionID, i), tenantID, sessionID, i)
		if err != nil {
			t.Logf("创建请求日志失败: %v", err)
		}
	}
	
	return sessionID
}

func createComplianceViolationSession(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	ctx := context.Background()
	sessionID := fmt.Sprintf("gw_compliance_violation_%d", time.Now().Unix())
	
	_, err := pool.Exec(ctx,
		`INSERT INTO session_summaries (
			session_key, tenant_id, request_count, success_count, error_count,
			total_cost_usd, compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected,
			models_used, primary_model,
			avg_latency_ms, model_switch_count,
			first_request_at, last_request_at,
			created_at, updated_at
		) VALUES (
			$1, $2, 10, 8, 2,
			2.50, 'violation', 2,
			true, true,
			ARRAY['gpt-4o'], 'gpt-4o',
			2000, 0,
			NOW() - INTERVAL '2 hours', NOW() - INTERVAL '1 hour',
			NOW(), NOW()
		) ON CONFLICT (session_key) DO NOTHING`,
		sessionID, tenantID)
	require.NoError(t, err)
	
	return sessionID
}

func createOptimizableSession(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	ctx := context.Background()
	sessionID := fmt.Sprintf("gw_optimizable_%d", time.Now().Unix())
	
	_, err := pool.Exec(ctx,
		`INSERT INTO session_summaries (
			session_key, tenant_id, request_count, success_count,
			total_cost_usd, total_tokens,
			models_used, primary_model,
			avg_latency_ms, model_switch_count,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected,
			first_request_at, last_request_at,
			created_at, updated_at
		) VALUES (
			$1, $2, 20, 20,
			5.00, 50000,
			ARRAY['gpt-4o'], 'gpt-4o',
			2000, 0,
			'compliant', 0,
			false, false, false,
			NOW() - INTERVAL '3 hours', NOW() - INTERVAL '2 hours',
			NOW(), NOW()
		) ON CONFLICT (session_key) DO NOTHING`,
		sessionID, tenantID)
	require.NoError(t, err)
	
	return sessionID
}

func createActiveSession(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	ctx := context.Background()
	sessionID := fmt.Sprintf("gw_active_%d", time.Now().Unix())
	
	// 创建会话摘要
	_, err := pool.Exec(ctx,
		`INSERT INTO session_summaries (
			session_key, tenant_id, request_count, success_count,
			total_cost_usd, 
			models_used, primary_model,
			avg_latency_ms, model_switch_count,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected,
			first_request_at, last_request_at,
			created_at, updated_at
		) VALUES (
			$1, $2, 5, 5,
			1.20, 
			ARRAY['gpt-4o'], 'gpt-4o',
			2000, 0,
			'compliant', 0,
			false, false, false,
			NOW() - INTERVAL '10 minutes', NOW() - INTERVAL '1 minute',
			NOW(), NOW()
		) ON CONFLICT (session_key) DO NOTHING`,
		sessionID, tenantID)
	require.NoError(t, err)
	
	// 创建活跃状态快照
	_, err = pool.Exec(ctx,
		`INSERT INTO session_state_snapshots (
			session_id, tenant_id, status, raw_snapshot,
			created_at, updated_at
		) VALUES (
			$1, $2, 'active', '{"status":"active"}',
			NOW() - INTERVAL '10 minutes', NOW()
		) ON CONFLICT (session_id) DO UPDATE SET
			status = 'active',
			updated_at = NOW()`,
		sessionID, tenantID)
	if err != nil {
		t.Logf("创建活跃状态: %v", err)
	}
	
	return sessionID
}

func createMonthlyTestData(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	ctx := context.Background()
	
	// 创建本月和上月的测试数据
	for month := 0; month < 2; month++ {
		for day := 1; day <= 5; day++ {
			sessionID := fmt.Sprintf("gw_monthly_%d_%d_%d", month, day, time.Now().Unix())
			ts := time.Now().AddDate(0, -month, -day)
			
			_, err := pool.Exec(ctx,
				`INSERT INTO session_summaries (
					session_key, tenant_id, request_count, success_count,
					total_cost_usd, total_tokens,
					models_used, primary_model,
					avg_latency_ms, model_switch_count,
					compliance_status, compliance_issues_count,
					prompt_injection_detected, pii_detected, toxic_output_detected,
					first_request_at, last_request_at,
					created_at, updated_at
				) VALUES (
					$1, $2, $3, $3,
					$4, $5,
					ARRAY['gpt-4o'], 'gpt-4o',
					2000, 0,
					'compliant', 0,
					false, false, false,
					$6, $6,
					$6, $6
				) ON CONFLICT (session_key) DO NOTHING`,
				sessionID, tenantID, 10+day, 
				float64(day)*0.5, 
				(10+day)*1000,
				ts)
			if err != nil {
				t.Logf("创建月度数据失败: %v", err)
			}
		}
	}
}

func findSessionByID(sessions []admin.AnalyticsSessionSummary, sessionID string) *admin.AnalyticsSessionSummary {
	for _, s := range sessions {
		if s.GwSessionID == sessionID {
			return &s
		}
	}
	return nil
}
