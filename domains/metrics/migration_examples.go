// Package metrics 提供迁移示例，展示如何将现有统计 API 切换到 request_metrics
package metrics

import (
	"context"
	"database/sql"
	"time"
)

// ============================================================
// 看板页迁移示例
// ============================================================

// BoardSummary 看板汇总数据（原结构保持不变）
type BoardSummary struct {
	TotalRequests  int64
	SuccessCount   int64
	TotalCostUSD   float64
	TotalTokens    int64
	AvgLatencyMs   float64
}

// QueryBoardSummaryFromMetrics 从 request_metrics 查询看板汇总
// 替代原有的 queryBoardSummaryFacts()
func QueryBoardSummaryFromMetrics(
	ctx context.Context,
	db *sql.DB,
	tenantID string,
	startTime, endTime time.Time,
	includeProbes bool,
) (*BoardSummary, error) {
	filter := MetricsFilter{
		TenantID:      tenantID,
		StartTime:     startTime,
		EndTime:       endTime,
		IncludeProbes: includeProbes,
	}
	
	summary, err := QuerySummary(ctx, db, filter)
	if err != nil {
		return nil, err
	}
	
	return &BoardSummary{
		TotalRequests: summary.TotalRequests,
		SuccessCount:  summary.SuccessCount,
		TotalCostUSD:  summary.TotalCostUSD,
		TotalTokens:   summary.TotalTokens,
		AvgLatencyMs:  summary.AvgLatencyMs,
	}, nil
}

// ============================================================
// 会话统计迁移示例
// ============================================================

// SessionAggregates 会话汇总数据（原结构保持不变）
type SessionAggregates struct {
	TotalSessions int64
	TotalRequests int64
	TotalCost     float64
	AvgLatency    float64
}

// QuerySessionAggregatesFromMetrics 从 request_metrics 查询会话汇总
// 替代原有的 querySessionSummaryAggregates()
func QuerySessionAggregatesFromMetrics(
	ctx context.Context,
	db *sql.DB,
	tenantID string,
	startTime, endTime time.Time,
	includeProbes bool,
) (*SessionAggregates, error) {
	filter := MetricsFilter{
		TenantID:      tenantID,
		StartTime:     startTime,
		EndTime:       endTime,
		IncludeProbes: includeProbes,
	}
	
	// 查询汇总统计
	summary, err := QuerySummary(ctx, db, filter)
	if err != nil {
		return nil, err
	}
	
	// 查询会话数（DISTINCT session_id）
	sessionCount, err := QuerySessionCount(ctx, db, filter)
	if err != nil {
		return nil, err
	}
	
	return &SessionAggregates{
		TotalSessions: sessionCount,
		TotalRequests: summary.TotalRequests,
		TotalCost:     summary.TotalCostUSD,
		AvgLatency:    summary.AvgLatencyMs,
	}, nil
}

// ============================================================
// 供应商对账迁移示例
// ============================================================

// DailyProviderCost 供应商每日费用（用于对账报表）
type DailyProviderCost struct {
	Date         time.Time
	ProviderID   int64
	ProviderName string
	RequestCount int64
	TotalTokens  int64
	CostUSD      float64
}

// QueryDailyProviderCostFromMetrics 从 request_metrics 查询供应商每日费用
// 替代原有的 queryDailyGrain()
func QueryDailyProviderCostFromMetrics(
	ctx context.Context,
	db *sql.DB,
	tenantID string,
	startDate, endDate time.Time,
) ([]*DailyProviderCost, error) {
	query := `
		SELECT
			rm.billing_date as date,
			rm.provider_id,
			COALESCE(p.name, 'Unknown') as provider_name,
			COUNT(*) as request_count,
			COALESCE(SUM(rm.total_tokens), 0) as total_tokens,
			COALESCE(SUM(rm.cost_cents), 0) as cost_cents
		FROM request_metrics rm
		LEFT JOIN providers p ON p.id = rm.provider_id
		WHERE rm.tenant_id = $1
		  AND rm.billing_date >= $2
		  AND rm.billing_date < $3
		  AND rm.provider_id IS NOT NULL
		GROUP BY rm.billing_date, rm.provider_id, p.name
		ORDER BY rm.billing_date, cost_cents DESC
	`
	
	rows, err := db.QueryContext(ctx, query, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	results := []*DailyProviderCost{}
	for rows.Next() {
		var item DailyProviderCost
		var costCents int64
		
		err := rows.Scan(
			&item.Date,
			&item.ProviderID,
			&item.ProviderName,
			&item.RequestCount,
			&item.TotalTokens,
			&costCents,
		)
		if err != nil {
			return nil, err
		}
		
		// 转换费用：分 -> 美元
		item.CostUSD = float64(costCents) / 100.0
		
		results = append(results, &item)
	}
	
	return results, rows.Err()
}

// ============================================================
// 团队预算查询示例（新增功能）
// ============================================================

// TeamUsage 团队用量统计
type TeamUsage struct {
	TeamID       int64
	TeamName     string
	RequestCount int64
	TotalTokens  int64
	CostUSD      float64
}

// QueryTeamUsageFromMetrics 查询团队用量（用于团队预算管理）
func QueryTeamUsageFromMetrics(
	ctx context.Context,
	db *sql.DB,
	tenantID string,
	startDate, endDate time.Time,
) ([]*TeamUsage, error) {
	query := `
		SELECT
			rm.team_id,
			COALESCE(t.name, 'Unknown') as team_name,
			COUNT(*) as request_count,
			COALESCE(SUM(rm.total_tokens), 0) as total_tokens,
			COALESCE(SUM(rm.cost_cents), 0) as cost_cents
		FROM request_metrics rm
		LEFT JOIN teams t ON t.id = rm.team_id
		WHERE rm.tenant_id = $1
		  AND rm.billing_date >= $2
		  AND rm.billing_date < $3
		  AND rm.team_id IS NOT NULL
		  AND rm.traffic_class = 'business'
		GROUP BY rm.team_id, t.name
		ORDER BY cost_cents DESC
	`
	
	rows, err := db.QueryContext(ctx, query, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	results := []*TeamUsage{}
	for rows.Next() {
		var item TeamUsage
		var costCents int64
		
		err := rows.Scan(
			&item.TeamID,
			&item.TeamName,
			&item.RequestCount,
			&item.TotalTokens,
			&costCents,
		)
		if err != nil {
			return nil, err
		}
		
		// 转换费用：分 -> 美元
		item.CostUSD = float64(costCents) / 100.0
		
		results = append(results, &item)
	}
	
	return results, rows.Err()
}

// ============================================================
// 网关节点负载查询示例（新增功能）
// ============================================================

// GatewayNodeStats 网关节点统计
type GatewayNodeStats struct {
	GatewayNodeID string
	RequestCount  int64
	SuccessCount  int64
	FailedCount   int64
	AvgLatencyMs  float64
	TotalCostUSD  float64
}

// QueryGatewayNodeStatsFromMetrics 查询网关节点负载（用于容量规划）
func QueryGatewayNodeStatsFromMetrics(
	ctx context.Context,
	db *sql.DB,
	startTime, endTime time.Time,
) ([]*GatewayNodeStats, error) {
	query := `
		SELECT
			gateway_node_id,
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE status = 'success') as success_count,
			COUNT(*) FILTER (WHERE status != 'success') as failed_count,
			COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
			COALESCE(SUM(cost_cents), 0) as cost_cents
		FROM request_metrics
		WHERE occurred_at >= $1
		  AND occurred_at < $2
		  AND gateway_node_id IS NOT NULL
		GROUP BY gateway_node_id
		ORDER BY request_count DESC
	`
	
	rows, err := db.QueryContext(ctx, query, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	results := []*GatewayNodeStats{}
	for rows.Next() {
		var item GatewayNodeStats
		var costCents int64
		
		err := rows.Scan(
			&item.GatewayNodeID,
			&item.RequestCount,
			&item.SuccessCount,
			&item.FailedCount,
			&item.AvgLatencyMs,
			&costCents,
		)
		if err != nil {
			return nil, err
		}
		
		// 转换费用：分 -> 美元
		item.TotalCostUSD = float64(costCents) / 100.0
		
		results = append(results, &item)
	}
	
	return results, rows.Err()
}
