package analytics

// aligned_source.go —— 会话分析总览指标的**单一真源查询**（2026-11-21 统计
// 口径对齐轮）。
//
// # 为什么放在 domains/analytics
//
// /admin/session-analytics（总览页）与 /dashboard（会话统计面板的对齐指标行）
// 要展示**同一组数字**。两页分属两个包（session_analytics ⇄ dashboardapi，
// 且二者经 admin 包互相可达，不能互相 import）⇒ 共同的下游只有 domains/*。
// SQL 从 admin/session_analytics/queries.go 的 getOverviewSummary /
// getModelStats 逐字迁来（迁移后 session_analytics 反过来调用本文件）⇒
// 「两面对齐」是构造保证：同一份 SQL、同一组参数、同一套扫描。
//
// # 读的是什么
//
// user_session_aggregates / model_usage_aggregates 两个物化视图（迁移 981/
// 994 定义，bg.MaterializedViewRefresher 15 分钟一刷）——会话宇宙（探测排除、
// owner 双源键、上海日历日桶）都在视图定义里，这里**不得**再拼排除谓词。
// 调用方处理 42P01（未迁移环境）降级：对齐块缺席比假绿诚实。
//
// # 单位纪律（framework §3.2）
//
// InternalCostUSD = 美元（session_summaries.total_cost_usd 聚合）、
// ExternalCredits = 积分（total_credits_charged 聚合）。TotalCost 保留
// internal+external 跨单位合计为既有契约；新消费方用分列，**禁止**展示合计。

import (
	"context"
	"database/sql"
)

// OverviewSummaryRow 是 getOverviewSummary 当前周期扫描的原始行（增长率由
// session_analytics 层基于它计算，不在本层重复）。
type OverviewSummaryRow struct {
	TotalSessions      int64
	AvgTokens          float64
	AvgDurationSeconds float64
	AvgTurns           float64
	TotalCost          float64 // internal+external 跨单位合计（既有契约，勿上屏）
	InternalCostUSD    float64 // 美元
	ExternalCredits    float64 // 积分
	ActiveUsers        int64
	ActiveProjects     int64
	ActiveTasks        int64
}

// ModelStatsRow 是 getModelStats 的原始行（请求级模型视图聚合，百分比 0-100）。
type ModelStatsRow struct {
	SuccessRatePct float64
	CacheRatePct   float64
}

// QueryOverviewSummary 读 user_session_aggregates 的 [startDate, endDate]
// （YYYY-MM-DD，与物化视图上海日历日桶同尺）窗口聚合。
func QueryOverviewSummary(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*OverviewSummaryRow, error) {
	query := `
		SELECT
			COALESCE(SUM(session_count)::bigint, 0) as total_sessions,
			ROUND(COALESCE(AVG(total_tokens::NUMERIC / NULLIF(session_count, 0)), 0), 2) as avg_tokens,
			COALESCE(AVG(avg_duration_seconds), 0) as avg_duration_seconds,
			COALESCE(AVG(avg_turns), 0) as avg_turns,
			COALESCE(SUM(internal_cost + external_cost), 0) as total_cost,
			COALESCE(SUM(internal_cost), 0) as internal_cost,
			COALESCE(SUM(external_cost), 0) as external_cost,
			COALESCE(COUNT(DISTINCT owner), 0) as active_users,
			COALESCE(SUM(project_count)::bigint, 0) as active_projects,
			COALESCE(SUM(task_count)::bigint, 0) as active_tasks
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	row := &OverviewSummaryRow{}
	err := db.QueryRowContext(ctx, query, args...).Scan(
		&row.TotalSessions,
		&row.AvgTokens,
		&row.AvgDurationSeconds,
		&row.AvgTurns,
		&row.TotalCost,
		&row.InternalCostUSD,
		&row.ExternalCredits,
		&row.ActiveUsers,
		&row.ActiveProjects,
		&row.ActiveTasks,
	)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// QueryModelStats 读 model_usage_aggregates 的请求级成功率 / 缓存命中率
// （百分比 0-100）。请求级与会话级视图不可相加（981 口径分层），本查询只
// 服务「比率」类指标。
func QueryModelStats(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*ModelStatsRow, error) {
	query := `
		SELECT
			COALESCE(SUM(success_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as success_rate,
			COALESCE(SUM(cache_hit_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as cache_rate
		FROM model_usage_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	row := &ModelStatsRow{}
	err := db.QueryRowContext(ctx, query, args...).Scan(&row.SuccessRatePct, &row.CacheRatePct)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return row, nil
}
