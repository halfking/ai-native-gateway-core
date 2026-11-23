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

// effectiveWindow 返回本次查询实际使用的时间窗口。
//
// ★ UseBillingDate=true 时用 StartDate/EndDate；若两者未设置（零值）则回落到
//
//	StartTime/EndTime —— 零值日期会让条件恒为「无结果」，而「无结果」与
//	「过滤太严」在页面上一模一样，极难区分，宁可回落也不要静默返回空。
//
// ★ 窗口一律是**半开区间** [start, end)。
func (f MetricsFilter) effectiveWindow() (start, end time.Time) {
	if f.UseBillingDate && !f.StartDate.IsZero() && !f.EndDate.IsZero() {
		return f.StartDate, f.EndDate
	}
	return f.StartTime, f.EndTime
}

// columnKind 时间列的语义类别
type columnKind int

const (
	// kindTimestamp 时间戳列：直接与窗口端点比较（occurred_at）
	kindTimestamp columnKind = iota
	// kindDate 日期列：按 UTC 日历日裁剪（occurred_date / billing_date）
	kindDate
)

// buildFilterClauses 构造统一的 WHERE 条件（时间范围 + 维度过滤）。
//
// ★ 所有查询函数共用这一份实现。早先每个函数各写一遍过滤条件，导致
//
//	「filter 里的字段被接收了但根本没进 SQL」：ProviderSummary 忽略 ProviderID、
//	DailySummary 忽略 ProviderID/SessionID。调用方以为过滤生效了，其实没有。
//
// ★ 日期列按 **UTC 日历日**裁剪，并显式 AT TIME ZONE 'UTC'：
//
//	DATE 列与 timestamptz 参数比较时，PG 会按**会话时区**截断/转换，
//	同一个条件在 UTC 部署与 +08 部署上会差一天 ⇒ 跨环境不可复现。
//
// ★ prefix 是表别名（"rm." 或 ""），列名统一走它拼，避免漏写别名。
func buildFilterClauses(filter MetricsFilter, prefix string, column string, kind columnKind) ([]string, []interface{}) {
	clauses := []string{}
	args := []interface{}{}
	argIdx := 1

	col := func(name string) string { return prefix + name }

	// 时间范围
	start, end := filter.effectiveWindow()
	switch kind {
	case kindDate:
		clauses = append(clauses, fmt.Sprintf("%s >= ($%d::timestamptz AT TIME ZONE 'UTC')::date", col(column), argIdx))
		args = append(args, start)
		argIdx++
		// 结束边界取「端点前 1 微秒所属的那一天」：
		//   EndTime=11-23T00:00 ⇒ 11-22（当天不计入）
		//   EndTime=11-23T12:00 ⇒ 11-23（当天计入）
		// 与时间戳口径的半开区间 [start, end) 保持一致。
		clauses = append(clauses, fmt.Sprintf("%s <= (($%d::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC')::date", col(column), argIdx))
		args = append(args, end)
		argIdx++
	default:
		clauses = append(clauses, fmt.Sprintf("%s >= $%d", col(column), argIdx))
		args = append(args, start)
		argIdx++
		clauses = append(clauses, fmt.Sprintf("%s < $%d", col(column), argIdx))
		args = append(args, end)
		argIdx++
	}

	// 租户过滤
	if filter.TenantID != "" {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("tenant_id"), argIdx))
		args = append(args, filter.TenantID)
		argIdx++
	}

	// 探测流量过滤
	if !filter.IncludeProbes {
		clauses = append(clauses, fmt.Sprintf("%s = 'business'", col("traffic_class")))
	}

	// 供应商过滤
	if filter.ProviderID != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("provider_id"), argIdx))
		args = append(args, *filter.ProviderID)
		argIdx++
	}

	// 会话过滤
	if filter.SessionID != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("session_id"), argIdx))
		args = append(args, *filter.SessionID)
		argIdx++
	}

	// 付款者过滤
	if filter.PayerID != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("payer_id"), argIdx))
		args = append(args, *filter.PayerID)
		argIdx++
	}

	// 团队过滤
	if filter.TeamID != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("team_id"), argIdx))
		args = append(args, *filter.TeamID)
		argIdx++
	}

	// 网关节点过滤
	if filter.GatewayNodeID != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("gateway_node_id"), argIdx))
		args = append(args, *filter.GatewayNodeID)
		argIdx++
	}

	// 状态过滤
	if filter.Status != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("status"), argIdx))
		args = append(args, *filter.Status)
		argIdx++
	}

	// 流量类型过滤（显式指定）
	if filter.TrafficClass != nil {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col("traffic_class"), argIdx))
		args = append(args, *filter.TrafficClass)
		argIdx++
	}

	return clauses, args
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
	Date         time.Time
	RequestCount int64
	SuccessCount int64
	TotalTokens  int64
	CostCents    int64
	CostUSD      float64
	AvgLatencyMs float64
}

// ProviderSummary 按供应商汇总结果
type ProviderSummary struct {
	ProviderID   int64
	ProviderName string
	RequestCount int64
	SuccessCount int64
	TotalTokens  int64
	CostCents    int64
	CostUSD      float64
}

// BuildSummaryQuery 构造汇总查询
func BuildSummaryQuery(filter MetricsFilter) (string, []interface{}) {
	column, kind := "occurred_at", kindTimestamp
	if filter.UseBillingDate {
		// billing_date 是 DATE 列 ⇒ 走 UTC 日历日裁剪
		column, kind = "billing_date", kindDate
	}
	whereClauses, args := buildFilterClauses(filter, "", column, kind)

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

// buildDailySummaryQuery 构造按日汇总查询（可单测：口径回归靠它钉住）
func buildDailySummaryQuery(filter MetricsFilter) (string, []interface{}) {
	column, kind := "occurred_date", kindDate
	if filter.UseBillingDate {
		column = "billing_date"
	}
	whereClauses, args := buildFilterClauses(filter, "", column, kind)

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
	`, column, strings.Join(whereClauses, " AND "), column, column)

	return query, args
}

// QueryDailySummary 查询按日汇总统计
func QueryDailySummary(ctx context.Context, db *sql.DB, filter MetricsFilter) ([]*DailySummary, error) {
	query, args := buildDailySummaryQuery(filter)

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

// buildProviderSummaryQuery 构造按供应商汇总查询（可单测）
//
// ★ providers 的展示名列是 display_name（**没有** name 列）—— 写 p.name 会直接
//
//	报 "column p.name does not exist"，整个供应商汇总查询不可用。
func buildProviderSummaryQuery(filter MetricsFilter) (string, []interface{}) {
	column, kind := "occurred_at", kindTimestamp
	if filter.UseBillingDate {
		column, kind = "billing_date", kindDate
	}
	whereClauses, args := buildFilterClauses(filter, "rm.", column, kind)

	// 只统计有供应商的记录
	whereClauses = append(whereClauses, "rm.provider_id IS NOT NULL")

	query := fmt.Sprintf(`
		SELECT
			rm.provider_id,
			COALESCE(p.display_name, 'Unknown') as provider_name,
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE rm.status = 'success') as success_count,
			COALESCE(SUM(rm.total_tokens), 0) as total_tokens,
			COALESCE(SUM(rm.cost_cents), 0) as cost_cents
		FROM request_metrics rm
		LEFT JOIN providers p ON p.id = rm.provider_id
		WHERE %s
		GROUP BY rm.provider_id, p.display_name
		ORDER BY cost_cents DESC
	`, strings.Join(whereClauses, " AND "))

	return query, args
}

// QueryProviderSummary 查询按供应商汇总统计
func QueryProviderSummary(ctx context.Context, db *sql.DB, filter MetricsFilter) ([]*ProviderSummary, error) {
	query, args := buildProviderSummaryQuery(filter)

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

// buildSessionCountQuery 构造会话数查询（可单测）
func buildSessionCountQuery(filter MetricsFilter) (string, []interface{}) {
	whereClauses, args := buildFilterClauses(filter, "", "occurred_at", kindTimestamp)

	// 只统计有会话ID的记录
	whereClauses = append(whereClauses, "session_id IS NOT NULL")

	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT session_id)
		FROM request_metrics
		WHERE %s
	`, strings.Join(whereClauses, " AND "))

	return query, args
}

// QuerySessionCount 查询会话数（用于会话统计）
func QuerySessionCount(ctx context.Context, db *sql.DB, filter MetricsFilter) (int64, error) {
	query, args := buildSessionCountQuery(filter)

	var count int64
	err := db.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}
