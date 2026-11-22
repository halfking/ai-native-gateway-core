package admin

// analytics_queries.go — 分析详情数据查询逻辑 (2026-11-20)
//
// 复用 dashboard_board_queries.go 的查询模式,从 usage_facts 或 request_stats_minute 聚合数据

import (
	"context"
	"fmt"
)

// queryAnalyticsSummary 查询核心指标汇总
//
// 从 usage_facts 聚合指定对象的汇总数据
func (h *Handler) queryAnalyticsSummary(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) (AnalyticsSummary, bool) {
	// 构建 WHERE 条件
	where := "occurred_at >= $1 AND occurred_at < $2"
	args := []interface{}{tr.Start, tr.End}
	argIdx := 3

	// 添加维度过滤
	dimensionCol := getDimensionColumn(dimension)
	where += fmt.Sprintf(" AND %s = $%d", dimensionCol, argIdx)
	args = append(args, identifier)
	argIdx++

	// 可选租户过滤
	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	// 只统计终态行 (success/failure/rate_limited)
	where += " AND status IN ('success', 'failure', 'rate_limited')"

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) AS total_requests,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_count,
			SUM(CASE WHEN status IN ('failure', 'rate_limited') THEN 1 ELSE 0 END) AS error_count,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(prompt_tokens), 0) AS total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS total_completion_tokens,
			COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			COALESCE(SUM(credits_charged), 0) AS total_credits,
			COALESCE(AVG(CASE WHEN status = 'success' THEN latency_ms END), 0) AS avg_latency_ms
		FROM usage_facts
		WHERE %s
	`, where)

	var summary AnalyticsSummary
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests,
		&summary.SuccessCount,
		&summary.ErrorCount,
		&summary.TotalTokens,
		&summary.TotalPromptTokens,
		&summary.TotalCompletionTokens,
		&summary.TotalCostUSD,
		&summary.TotalCredits,
		&summary.AvgLatencyMS,
	)

	if err != nil {
		// 如果 usage_facts 表不存在或查询失��,尝试降级到 request_stats_minute
		return h.queryAnalyticsSummaryFromMinute(ctx, dimension, identifier, tr, tenantID)
	}

	// 计算成功率
	if summary.TotalRequests > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.TotalRequests)
	}

	return summary, true
}

// queryAnalyticsSummaryFromMinute 从分钟表降级查询汇总数据
func (h *Handler) queryAnalyticsSummaryFromMinute(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) (AnalyticsSummary, bool) {
	where := "bucket >= $1 AND bucket < $2"
	args := []interface{}{tr.Start, tr.End}
	argIdx := 3

	dimensionCol := getDimensionColumn(dimension)
	where += fmt.Sprintf(" AND %s = $%d", dimensionCol, argIdx)
	args = append(args, identifier)
	argIdx++

	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(requests), 0) AS total_requests,
			COALESCE(SUM(success), 0) AS success_count,
			COALESCE(SUM(requests) - SUM(success), 0) AS error_count,
			COALESCE(SUM(tokens), 0) AS total_tokens,
			COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			COALESCE(SUM(credits), 0) AS total_credits,
			COALESCE(AVG(CASE WHEN requests > 0 THEN latency_sum / requests END), 0) AS avg_latency_ms
		FROM request_stats_minute
		WHERE %s
	`, where)

	var summary AnalyticsSummary
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests,
		&summary.SuccessCount,
		&summary.ErrorCount,
		&summary.TotalTokens,
		&summary.TotalCostUSD,
		&summary.TotalCredits,
		&summary.AvgLatencyMS,
	)

	if err != nil {
		return AnalyticsSummary{}, false
	}

	if summary.TotalRequests > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.TotalRequests)
	}

	return summary, true
}

// queryAnalyticsTrends 查询趋势数据
//
// 按时间桶聚合,返回时间序列数据
func (h *Handler) queryAnalyticsTrends(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) ([]AnalyticsTrendPoint, bool) {
	// 根据时间范围选择粒度
	// 1天: 15分钟, 7天: 1小时, 30天+: 6小时
	var bucketInterval string
	if tr.Days <= 1 {
		bucketInterval = "15 minutes"
	} else if tr.Days <= 7 {
		bucketInterval = "1 hour"
	} else {
		bucketInterval = "6 hours"
	}

	where := "bucket >= $1 AND bucket < $2"
	args := []interface{}{tr.Start, tr.End}
	argIdx := 3

	dimensionCol := getDimensionColumn(dimension)
	where += fmt.Sprintf(" AND %s = $%d", dimensionCol, argIdx)
	args = append(args, identifier)
	argIdx++

	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', bucket) AS time_bucket,
			SUM(requests) AS requests,
			SUM(success) AS success,
			SUM(requests) - SUM(success) AS errors,
			SUM(tokens) AS tokens,
			SUM(cost_usd) AS cost_usd,
			SUM(credits) AS credits,
			AVG(CASE WHEN requests > 0 THEN latency_sum / requests END) AS avg_latency_ms
		FROM request_stats_minute
		WHERE %s
		GROUP BY time_bucket
		ORDER BY time_bucket
	`, bucketInterval, where)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var trends []AnalyticsTrendPoint
	for rows.Next() {
		var point AnalyticsTrendPoint
		var bucket interface{}

		err := rows.Scan(
			&bucket,
			&point.Requests,
			&point.Success,
			&point.Errors,
			&point.Tokens,
			&point.CostUSD,
			&point.Credits,
			&point.AvgLatencyMS,
		)
		if err != nil {
			continue
		}

		// 将时间格式化为 ISO 8601
		if t, ok := bucket.(interface{ Format(string) string }); ok {
			point.Bucket = t.Format("2006-01-02T15:04:05Z07:00")
		}

		trends = append(trends, point)
	}

	return trends, true
}

// queryAnalyticsCrossAnalysis 查询交叉分析数据
//
// 按其他维度 GROUP BY,返回 Top 10
func (h *Handler) queryAnalyticsCrossAnalysis(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) (AnalyticsCrossAnalysis, []string) {
	var result AnalyticsCrossAnalysis
	var degraded []string

	// 对于每个其他维度,执行 GROUP BY 查询
	dimensions := []string{"model", "provider", "client", "tenant"}

	for _, crossDim := range dimensions {
		// 跳过当前维度
		if crossDim == dimension {
			continue
		}

		items, ok := h.queryCrossDimension(ctx, dimension, identifier, crossDim, tr, tenantID)
		if !ok {
			degraded = append(degraded, crossDim)
			continue
		}

		// 设置到对应字段
		switch crossDim {
		case "model":
			result.ByModel = items
		case "provider":
			result.ByProvider = items
		case "client":
			result.ByClient = items
		case "tenant":
			result.ByTenant = items
		}
	}

	// 查询错误类型分布
	errors, errOK := h.queryCrossErrors(ctx, dimension, identifier, tr, tenantID)
	if errOK {
		result.ByError = errors
	} else {
		degraded = append(degraded, "error")
	}

	return result, degraded
}

// queryCrossDimension 查询单个交叉维度
func (h *Handler) queryCrossDimension(
	ctx context.Context,
	filterDimension string,
	filterIdentifier string,
	groupDimension string,
	tr boardTimeRange,
	tenantID string,
) ([]boardPieItem, bool) {
	where := "bucket >= $1 AND bucket < $2"
	args := []interface{}{tr.Start, tr.End}
	argIdx := 3

	filterCol := getDimensionColumn(filterDimension)
	where += fmt.Sprintf(" AND %s = $%d", filterCol, argIdx)
	args = append(args, filterIdentifier)
	argIdx++

	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	groupCol := getDimensionColumn(groupDimension)

	query := fmt.Sprintf(`
		SELECT
			%s AS key,
			SUM(requests) AS requests,
			SUM(tokens) AS tokens,
			SUM(credits) AS credits,
			SUM(cost_usd) AS cost_usd
		FROM request_stats_minute
		WHERE %s
		GROUP BY %s
		ORDER BY requests DESC
		LIMIT 10
	`, groupCol, where, groupCol)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD)
		if err != nil {
			continue
		}
		items = append(items, item)
	}

	return items, true
}

// queryCrossErrors 查询错误类型分布
func (h *Handler) queryCrossErrors(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) ([]boardPieItem, bool) {
	where := "occurred_at >= $1 AND occurred_at < $2"
	args := []interface{}{tr.Start, tr.End}
	argIdx := 3

	dimensionCol := getDimensionColumn(dimension)
	where += fmt.Sprintf(" AND %s = $%d", dimensionCol, argIdx)
	args = append(args, identifier)
	argIdx++

	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	where += " AND status IN ('failure', 'rate_limited')"

	query := fmt.Sprintf(`
		SELECT
			COALESCE(error_type, 'unknown') AS key,
			COUNT(*) AS requests,
			0 AS tokens,
			0 AS credits,
			0 AS cost_usd
		FROM usage_facts
		WHERE %s
		GROUP BY error_type
		ORDER BY requests DESC
		LIMIT 10
	`, where)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD)
		if err != nil {
			continue
		}
		items = append(items, item)
	}

	return items, true
}

// getDimensionColumn 获取维度对应的数据库列名
func getDimensionColumn(dimension string) string {
	switch dimension {
	case "model":
		return "model_name"
	case "provider":
		return "provider_name"
	case "client":
		return "client_id"
	case "tenant":
		return "tenant_id"
	default:
		return dimension
	}
}
