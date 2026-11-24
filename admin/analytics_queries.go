package admin

// analytics_queries.go — 分析详情数据查询逻辑 (2026-11-20)
//
// 复用 dashboard_board_queries.go 的查询模式,从 usage_facts 或 request_stats_minute 聚合数据

import (
	"context"
	"fmt"
	"strconv"
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
			COALESCE(SUM(cost_amount), 0) AS total_cost_usd,
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
		// 如果 usage_facts 表不存在或查询失败,尝试降级到 request_stats_minute
		return h.queryAnalyticsSummaryFromMinute(ctx, dimension, identifier, tr, tenantID)
	}

	// 计算成功率
	if summary.TotalRequests > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.TotalRequests)
	}

	return summary, true
}

// queryAnalyticsSummaryFromMinute 从分钟维度表降级查询汇总数据
//
// 2026-11-23 修订：原版查 request_stats_minute 并按 model_name/provider_name/
// client_id 过滤——这些列在该表不存在（该表只有 tenant_id/provider_id/canonical_id
// 数值列），SQL 必报 column does not exist ⇒ 降级路径从未工作过。改查
// request_stats_dim_minute（dim_type/dim_key 维度真源，与 dashboard_board_queries.go
// 的 queryDimPie 同一真源）。dim 表无 latency 列 ⇒ AvgLatencyMS 恒 0（降级路径的
// 已知取舍，主路径 queryAnalyticsSummary 从 usage_facts 拿真值）。
func (h *Handler) queryAnalyticsSummaryFromMinute(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) (AnalyticsSummary, bool) {
	dimType := dimTypeFor(dimension)
	args := []interface{}{dimType, identifier, tr.Start, tr.End}
	where := "dim_type = $1 AND dim_key = $2 AND bucket >= $3 AND bucket < $4"
	if tenantID != "" {
		where += " AND tenant_id = $5"
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(requests), 0) AS total_requests,
			COALESCE(SUM(success_count), 0) AS success_count,
			COALESCE(SUM(failure_count), 0) AS error_count,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			COALESCE(SUM(credits_charged), 0) AS total_credits
		FROM request_stats_dim_minute
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
//
// 2026-11-23 修订：原版查 request_stats_minute 用 SUM(success)/SUM(tokens)/
// SUM(credits)/latency_sum 与 model_name 过滤——列全不存在 ⇒ 趋势区块从未渲染
// （详情页只见汇总与交叉分析，无趋势图）。改查 request_stats_dim_minute，
// 列名对齐真实 schema（success_count/failure_count/total_tokens/credits_charged/
// cost_usd）；dim 表无 latency 列 ⇒ AvgLatencyMS 恒 0。
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

	dimType := dimTypeFor(dimension)
	args := []interface{}{dimType, identifier, tr.Start, tr.End}
	where := "dim_type = $1 AND dim_key = $2 AND bucket >= $3 AND bucket < $4"
	if tenantID != "" {
		where += " AND tenant_id = $5"
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', bucket) AS time_bucket,
			COALESCE(SUM(requests), 0) AS requests,
			COALESCE(SUM(success_count), 0) AS success,
			COALESCE(SUM(failure_count), 0) AS errors,
			COALESCE(SUM(total_tokens), 0) AS tokens,
			COALESCE(SUM(cost_usd), 0) AS cost_usd,
			COALESCE(SUM(credits_charged), 0) AS credits
		FROM request_stats_dim_minute
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
	if err := rows.Err(); err != nil {
		return nil, false
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
//
// 2026-11-23 修订：原版查 request_stats_minute 并按 model_name/provider_name/
// client_id 过滤与分组——列全不存在 ⇒ 交叉分析恒空（「没有可用的交叉分析数据」）。
// 改查 request_stats_dim_minute：filter = dim_type(本对象) + dim_key(identifier)，
// group = dim_type(交叉维度) GROUP BY dim_key。与 queryDimPie 同一真源。
func (h *Handler) queryCrossDimension(
	ctx context.Context,
	filterDimension string,
	filterIdentifier string,
	groupDimension string,
	tr boardTimeRange,
	tenantID string,
) ([]boardPieItem, bool) {
	filterType := dimTypeFor(filterDimension)
	groupType := dimTypeFor(groupDimension)

	// dim_minute 表一行只挂一个维度 ⇒ 跨维度交叉 = 在「filter 维度命中的 bucket
	// 集合」上按 group 维度聚合（bucket 级半连接，不扫 usage_facts 大表）。
	args := []interface{}{groupType, filterType, filterIdentifier, tr.Start, tr.End}
	fTenant, gTenant := "", ""
	if tenantID != "" {
		args = append(args, tenantID)
		fTenant = " AND f.tenant_id = $6"
		gTenant = " AND g.tenant_id = $6"
	}

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT g.dim_key AS key,
			COALESCE(SUM(g.requests), 0),
			COALESCE(SUM(g.total_tokens), 0),
			COALESCE(SUM(g.credits_charged), 0),
			COALESCE(SUM(g.cost_usd), 0)
		FROM request_stats_dim_minute g
		WHERE g.dim_type = $1%s
		  AND g.bucket IN (
		      SELECT f.bucket
		      FROM request_stats_dim_minute f
		      WHERE f.dim_type = $2 AND f.dim_key = $3
		        AND f.bucket >= $4 AND f.bucket < $5%s
		      )
		GROUP BY g.dim_key
		ORDER BY SUM(g.requests) DESC
		LIMIT 10
	`, gTenant, fTenant), args...)
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
	if err := rows.Err(); err != nil {
		return nil, false
	}

	return items, true
}

// queryCrossErrors 查询错误类型分布
//
// 2026-11-23 修订：原版查 usage_facts 用 error_type 列——该列不存在（真列是
// error_kind/error_class/failure_stage）⇒ 错误分布恒空。改查聚合表
// request_stats_error_drill_minute（按 error_kind GROUP BY，bucket/tenant_id/
// model_name/provider_id 可作过滤）；client 维度在 drill 表无对应列 ⇒ 降级。
func (h *Handler) queryCrossErrors(
	ctx context.Context,
	dimension string,
	identifier string,
	tr boardTimeRange,
	tenantID string,
) ([]boardPieItem, bool) {
	// drill 表可用的过滤列：tenant_id / model_name / provider_id。
	// client（agent_name）维度在 drill 表没有对应列 ⇒ 显式降级。
	var filterClause string
	var filterArg interface{}
	switch dimension {
	case "tenant":
		filterClause, filterArg = "tenant_id = $3", identifier
	case "model":
		filterClause, filterArg = "model_name = $3", identifier
	case "provider":
		// dim_minute 里 provider 的 dim_key 是 provider_id 的文本 ⇒ 详情页
		// identifier 也是 id 文本；解析失败按降级处理。
		pid, err := strconv.ParseInt(identifier, 10, 64)
		if err != nil {
			return nil, false
		}
		filterClause, filterArg = "provider_id = $3", pid
	default:
		return nil, false
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(error_kind, ''), 'unknown') AS key,
			COALESCE(SUM(requests), 0) AS requests,
			0 AS tokens,
			0 AS credits,
			0 AS cost_usd
		FROM request_stats_error_drill_minute
		WHERE bucket >= $1 AND bucket < $2 AND %s
		GROUP BY error_kind
		ORDER BY requests DESC
		LIMIT 10
	`, filterClause)

	args := []interface{}{tr.Start, tr.End, filterArg}
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
	// ★ rows 终检：迭代中断（连接断开 / 服务端错误）时 rows.Next() 会提前返回
	//   false ⇒ 不看 rows.Err() 就会把**半截结果**当成完整结果交出去
	//   （silent truncation ⇒ 看板少了一格却看不出来）。
	//   ★ 形态与本文件另两处循环（boardTrends / boardPie）逐字一致。
	if err := rows.Err(); err != nil {
		return nil, false
	}

	return items, true
}

// getDimensionColumn 获取 usage_facts 表中维度对应的真实列名
//
// 2026-11-23 修订：原返回值与 usage_facts 真实 schema 不符——
//   - model → model_name：不存在（真列 raw_model_name，537 迁移）
//   - provider → provider_name：不存在（真列 provider_id bigint；详情页 identifier
//     来自 dim_minute 的 dim_key = provider_id 文本，恰好可被 PG 隐式转换）
//   - client → client_id：usage_facts 无客户端列（agent_name 只进 dim_minute）⇒
//     主路径报错，由 queryAnalyticsSummaryFromMinute（dim_minute agent_name）降级接住。
func getDimensionColumn(dimension string) string {
	switch dimension {
	case "model":
		return "raw_model_name"
	case "provider":
		return "provider_id"
	case "client":
		return "client_id" // usage_facts 无此列 ⇒ 主路径失败 ⇒ dim_minute 降级
	case "tenant":
		return "tenant_id"
	default:
		return dimension
	}
}

// dimTypeFor 把 analytics 维度映射到 request_stats_dim_minute.dim_type 的真实取值。
// dim_type 由 bg/stats_minute_rollup.go 的 rollupDimQueries 写入：model/tenant/
// provider（dim_key = provider_id 文本）/client_profile/agent_name/client_ip/
// virtual_ip/identity_hash。client 维度与看板 clients 饼图同源，取 agent_name。
func dimTypeFor(dimension string) string {
	switch dimension {
	case "model":
		return "model"
	case "provider":
		return "provider"
	case "client":
		return "agent_name"
	case "tenant":
		return "tenant"
	default:
		return dimension
	}
}
