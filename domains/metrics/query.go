// Package metrics 提供统一的 request_metrics 查询接口
// 用于替代分散的统计查询逻辑（usage_facts / session_summaries 等）
package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MetricsFilter 统一的查询过滤器
type MetricsFilter struct {
	TenantID      string
	StartTime     time.Time
	EndTime       time.Time
	IncludeProbes bool
	
	// 可选维度过滤
	ProviderID    *int64
	SessionID     *string
	PayerID       *string
	TeamID        *int64
	GatewayNodeID *string
	Status        *string
	TrafficClass  *string
	
	// 日期过滤（用于账单查询）
	UseBillingDate bool      // 是否使用记账日期而非发生日期
	StartDate      time.Time // 开始日期（仅当 UseBillingDate=true 时使用）
	EndDate        time.Time // 结束日期（仅当 UseBillingDate=true 时使用）
}

// Summary 汇总统计结果
type Summary struct {
	TotalRequests         int64
	SuccessCount          int64
	FailedCount           int64
	TotalPromptTokens     int64
	TotalCompletionTokens int64
	TotalCachedTokens     int64
	TotalTokens           int64
	TotalCostCents        int64   // 费用（分）
	TotalCostUSD          float64 // 费用（美元）
	TotalCredits          int64
	AvgLatencyMs          float64
	AvgTTFTMs             float64
}

// DailySummary 按日汇总结果
type DailySummary struct {
	Date              time.Time
	RequestCount      int64
	SuccessCount      int64
	TotalTokens       int64
	CostCents         int64
	CostUSD           float64
	AvgLatencyMs      float64
}

// ProviderSummary 按供应商汇总结果
type ProviderSummary struct {
	ProviderID       int64
	ProviderName     string
	RequestCount     int64
	SuccessCount     int64
	TotalTokens      int64
	CostCents        int64
	CostUSD          float64
}

// BuildSummaryQuery 构造汇总查询
func BuildSummaryQuery(filter MetricsFilter) (string, []interface{}) {
	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1
	
	// 时间范围过滤
	if filter.UseBillingDate {
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date >= $%d", argIdx))
		args = append(args, filter.StartDate)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date < $%d", argIdx))
		args = append(args, filter.EndDate)
		argIdx++
	} else {
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_at >= $%d", argIdx))
		args = append(args, filter.StartTime)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_at < $%d", argIdx))
		args = append(args, filter.EndTime)
		argIdx++
	}
	
	// 租户过滤
	if filter.TenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filter.TenantID)
		argIdx++
	}
	
	// 探测流量过滤
	if !filter.IncludeProbes {
		whereClauses = append(whereClauses, "traffic_class = 'business'")
	}
	
	// 供应商过滤
	if filter.ProviderID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("provider_id = $%d", argIdx))
		args = append(args, *filter.ProviderID)
		argIdx++
	}
	
	// 会话过滤
	if filter.SessionID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("session_id = $%d", argIdx))
		args = append(args, *filter.SessionID)
		argIdx++
	}
	
	// 付款者过滤
	if filter.PayerID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("payer_id = $%d", argIdx))
		args = append(args, *filter.PayerID)
		argIdx++
	}
	
	// 团队过滤
	if filter.TeamID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("team_id = $%d", argIdx))
		args = append(args, *filter.TeamID)
		argIdx++
	}
	
	// 网关节点过滤
	if filter.GatewayNodeID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("gateway_node_id = $%d", argIdx))
		args = append(args, *filter.GatewayNodeID)
		argIdx++
	}
	
	// 状态过滤
	if filter.Status != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *filter.Status)
		argIdx++
	}
	
	// 流量类型过滤（显式指定）
	if filter.TrafficClass != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("traffic_class = $%d", argIdx))
		args = append(args, *filter.TrafficClass)
		argIdx++
	}
	
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COUNT(*) FILTER (WHERE status = 'success') as success_count,
			COUNT(*) FILTER (WHERE status != 'success') as failed_count,
			COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as total_completion_tokens,
			COALESCE(SUM(cached_tokens), 0) as total_cached_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cost_cents), 0) as total_cost_cents,
			COALESCE(SUM(credits_charged), 0) as total_credits,
			COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
			COALESCE(AVG(ttft_ms), 0) as avg_ttft_ms
		FROM request_metrics
		WHERE %s
	`, strings.Join(whereClauses, " AND "))
	
	return query, args
}

// QuerySummary 查询汇总统计
func QuerySummary(ctx context.Context, db *sql.DB, filter MetricsFilter) (*Summary, error) {
	query, args := BuildSummaryQuery(filter)
	
	var summary Summary
	var totalCostCents int64
	var avgLatency, avgTTFT sql.NullFloat64
	
	err := db.QueryRowContext(ctx, query, args...).Scan(
		&summary.TotalRequests,
		&summary.SuccessCount,
		&summary.FailedCount,
		&summary.TotalPromptTokens,
		&summary.TotalCompletionTokens,
		&summary.TotalCachedTokens,
		&summary.TotalTokens,
		&totalCostCents,
		&summary.TotalCredits,
		&avgLatency,
		&avgTTFT,
	)
	
	if err != nil {
		return nil, err
	}
	
	// 转换费用：分 -> 美元
	summary.TotalCostCents = totalCostCents
	summary.TotalCostUSD = float64(totalCostCents) / 100.0
	
	// 处理可空字段
	if avgLatency.Valid {
		summary.AvgLatencyMs = avgLatency.Float64
	}
	if avgTTFT.Valid {
		summary.AvgTTFTMs = avgTTFT.Float64
	}
	
	return &summary, nil
}

// QueryDailySummary 查询按日汇总统计
func QueryDailySummary(ctx context.Context, db *sql.DB, filter MetricsFilter) ([]*DailySummary, error) {
	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1
	
	// 时间范围过滤
	if filter.UseBillingDate {
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date >= $%d", argIdx))
		args = append(args, filter.StartDate)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date < $%d", argIdx))
		args = append(args, filter.EndDate)
		argIdx++
	} else {
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_date >= $%d", argIdx))
		args = append(args, filter.StartDate)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_date < $%d", argIdx))
		args = append(args, filter.EndDate)
		argIdx++
	}
	
	// 租户过滤
	if filter.TenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filter.TenantID)
		argIdx++
	}
	
	// 探测流量过滤
	if !filter.IncludeProbes {
		whereClauses = append(whereClauses, "traffic_class = 'business'")
	}
	
	dateField := "occurred_date"
	if filter.UseBillingDate {
		dateField = "billing_date"
	}
	
	query := fmt.Sprintf(`
		SELECT
			%s as date,
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE status = 'success') as success_count,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cost_cents), 0) as cost_cents,
			COALESCE(AVG(latency_ms), 0) as avg_latency_ms
		FROM request_metrics
		WHERE %s
		GROUP BY %s
		ORDER BY %s
	`, dateField, strings.Join(whereClauses, " AND "), dateField, dateField)
	
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	results := []*DailySummary{}
	for rows.Next() {
		var summary DailySummary
		var avgLatency sql.NullFloat64
		
		err := rows.Scan(
			&summary.Date,
			&summary.RequestCount,
			&summary.SuccessCount,
			&summary.TotalTokens,
			&summary.CostCents,
			&avgLatency,
		)
		if err != nil {
			return nil, err
		}
		
		// 转换费用：分 -> 美元
		summary.CostUSD = float64(summary.CostCents) / 100.0
		
		// 处理可空字段
		if avgLatency.Valid {
			summary.AvgLatencyMs = avgLatency.Float64
		}
		
		results = append(results, &summary)
	}
	
	return results, rows.Err()
}

// QueryProviderSummary 查询按供应商汇总统计
func QueryProviderSummary(ctx context.Context, db *sql.DB, filter MetricsFilter) ([]*ProviderSummary, error) {
	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1
	
	// 时间范围过滤
	if filter.UseBillingDate {
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date >= $%d", argIdx))
		args = append(args, filter.StartDate)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("billing_date < $%d", argIdx))
		args = append(args, filter.EndDate)
		argIdx++
	} else {
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_at >= $%d", argIdx))
		args = append(args, filter.StartTime)
		argIdx++
		whereClauses = append(whereClauses, fmt.Sprintf("occurred_at < $%d", argIdx))
		args = append(args, filter.EndTime)
		argIdx++
	}
	
	// 租户过滤
	if filter.TenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filter.TenantID)
		argIdx++
	}
	
	// 探测流量过滤
	if !filter.IncludeProbes {
		whereClauses = append(whereClauses, "traffic_class = 'business'")
	}
	
	// 只统计有供应商的记录
	whereClauses = append(whereClauses, "provider_id IS NOT NULL")
	
	query := fmt.Sprintf(`
		SELECT
			rm.provider_id,
			COALESCE(p.name, 'Unknown') as provider_name,
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE rm.status = 'success') as success_count,
			COALESCE(SUM(rm.total_tokens), 0) as total_tokens,
			COALESCE(SUM(rm.cost_cents), 0) as cost_cents
		FROM request_metrics rm
		LEFT JOIN providers p ON p.id = rm.provider_id
		WHERE %s
		GROUP BY rm.provider_id, p.name
		ORDER BY cost_cents DESC
	`, strings.Join(whereClauses, " AND "))
	
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	results := []*ProviderSummary{}
	for rows.Next() {
		var summary ProviderSummary
		
		err := rows.Scan(
			&summary.ProviderID,
			&summary.ProviderName,
			&summary.RequestCount,
			&summary.SuccessCount,
			&summary.TotalTokens,
			&summary.CostCents,
		)
		if err != nil {
			return nil, err
		}
		
		// 转换费用：分 -> 美元
		summary.CostUSD = float64(summary.CostCents) / 100.0
		
		results = append(results, &summary)
	}
	
	return results, rows.Err()
}

// QuerySessionCount 查询会话数（用于会话统计）
func QuerySessionCount(ctx context.Context, db *sql.DB, filter MetricsFilter) (int64, error) {
	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1
	
	// 时间范围过滤
	whereClauses = append(whereClauses, fmt.Sprintf("occurred_at >= $%d", argIdx))
	args = append(args, filter.StartTime)
	argIdx++
	whereClauses = append(whereClauses, fmt.Sprintf("occurred_at < $%d", argIdx))
	args = append(args, filter.EndTime)
	argIdx++
	
	// 租户过滤
	if filter.TenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filter.TenantID)
		argIdx++
	}
	
	// 探测流量过滤
	if !filter.IncludeProbes {
		whereClauses = append(whereClauses, "traffic_class = 'business'")
	}
	
	// 只统计有会话ID的记录
	whereClauses = append(whereClauses, "session_id IS NOT NULL")
	
	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT session_id)
		FROM request_metrics
		WHERE %s
	`, strings.Join(whereClauses, " AND "))
	
	var count int64
	err := db.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, err
	}
	
	return count, nil
}
