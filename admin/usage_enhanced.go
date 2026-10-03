// Package admin — Usage Enhanced API (T1.4)
//
// 用量成本增强端点：
//
//	GET /api/admin/usage/cost-trend?group_by=model|provider|intent|work_type|api_key
//	GET /api/admin/usage/period-compare?current=2026-07&previous=2026-06
//	GET /api/admin/usage/cache-economics?date_from=&date_to=
//
// 参考文档：docs/session-management-analytics-plan.md 第 4.6 节
package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────
// 1. GET /api/admin/usage/cost-trend?group_by=model|provider|intent|work_type|api_key
// ──────────────────────────────────────────────────────────────────────────

// CostTrendEntry 成本趋势条目（按指定维度分组）
type CostTrendEntry struct {
	DimensionValue   string  `json:"dimension_value"`   // 分组维度的值（如 "gpt-4o", "openai"）
	RequestCount     int     `json:"request_count"`     // 请求数
	TotalCostUSD     float64 `json:"total_cost_usd"`    // 总成本
	InputCostUSD     float64 `json:"input_cost_usd"`    // 输入成本
	OutputCostUSD    float64 `json:"output_cost_usd"`   // 输出成本
	PromptTokens     int64   `json:"prompt_tokens"`     // prompt tokens
	CompletionTokens int64   `json:"completion_tokens"` // completion tokens
	AvgLatencyMs     int     `json:"avg_latency_ms"`    // 平均延迟
	ErrorRate        float64 `json:"error_rate"`        // 错误率
	Percentage       float64 `json:"percentage"`        // 占总成本百分比
}

// CostTrendResponse 成本趋势响应
type CostTrendResponse struct {
	GroupBy    string           `json:"group_by"`    // 分组维度
	DateFrom   string           `json:"date_from"`   // 开始日期
	DateTo     string           `json:"date_to"`     // 结束日期
	TotalCost  float64          `json:"total_cost"`  // 总成本
	Entries    []CostTrendEntry `json:"entries"`     // 分组条目
	OtherCost  float64          `json:"other_cost"`  // 其他（占比<2%合并）
	OtherCount int              `json:"other_count"` // 其他条目数量
}

// costTrendPlan 是 cost-trend 某个 group_by 的取数方案：基表、基表别名、
// 分组列表达式、以及为该维度额外需要的 JOIN。
//
// 把它抽成纯函数（而不是让 handler 内联拼装）的唯一理由是可测：回归门直接
// 断言每个维度的归属，而不是用正则去猜 handler 源码里写了什么。维度与基表
// 的对应关系就是契约，一旦有人把 work_type 挪回计费宽表，门立刻红。
type costTrendPlan struct {
	GroupBy     string
	BaseTable   string
	BaseAlias   string
	GroupColumn string
	JoinClause  string
	RequestSide bool
}

// planCostTrend 返回某个 group_by 的取数方案；维度未知时 ok=false。
//
// work_type / intent 是「请求侧」维度：原生归属是 request_logs。usage_ledger
// 是计费宽表，只有 20 列（2026-10-03 实测 information_schema），从未投影
// work_type / gw_session_id —— 在它上面引用必然 42703。
//
// 两个都不可接受的旧修法：
//
//	A. 从 ledger LEFT JOIN request_logs 补列 —— 2M×2M 嵌套循环，30 天窗口
//	   实测 28s，超过本端点 15s 预算，且随窗口线性劣化；
//	B. 继续报错让 IsSchemaBehindError 降级 —— 得到 200 + 全 0，而 intent 是
//	   UsageCost.vue 下拉框里的可选项，用户一点就中招。
//
// 采用：这两个维度以 request_logs 为基表。它按月分区、自带 ts 剪枝，30 天
// 窗口实测 2.4s（work_type）/ 4.1s（intent，含 session_summaries 连接）。
// 两表 cost 口径经核对一致（同窗口均 150.15 美元），故换基表不引入新的计费口径。
func planCostTrend(groupBy string) (costTrendPlan, bool) {
	// 列前缀写死而非从别名拼：ul. = 计费宽表，rl. = 请求表，
	// 一眼可辨某个维度读的是哪张表。
	ledgerSide := map[string]struct {
		groupColumn string
		joinClause  string
	}{
		"model":    {"ul.raw_model_name", ""},
		"provider": {"p.code", " LEFT JOIN providers p ON p.id = ul.provider_id"},
		"api_key":  {"ak.key_prefix", " LEFT JOIN api_keys ak ON ak.id = ul.api_key_id"},
	}
	requestSide := map[string]struct {
		groupColumn string
		joinClause  string
	}{
		// 只有 intent 需要连 session_summaries 取 user_intent；work_type 是
		// request_logs 自己的列，连一次 34 万行的表纯属白花 1.7s。
		"work_type": {"rl.work_type", ""},
		"intent":    {"ss.user_intent", " LEFT JOIN session_summaries ss ON ss.session_key = rl.gw_session_id"},
	}

	if e, ok := ledgerSide[groupBy]; ok {
		return costTrendPlan{
			GroupBy: groupBy, BaseTable: "usage_ledger_with_current_month ul",
			BaseAlias: "ul", GroupColumn: e.groupColumn, JoinClause: e.joinClause,
		}, true
	}
	if e, ok := requestSide[groupBy]; ok {
		return costTrendPlan{
			GroupBy: groupBy, BaseTable: "request_logs rl",
			BaseAlias: "rl", GroupColumn: e.groupColumn, JoinClause: e.joinClause,
			RequestSide: true,
		}, true
	}
	return costTrendPlan{}, false
}

func (h *Handler) usageCostTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析参数
	groupBy := queryString(r, "group_by")
	if groupBy == "" {
		groupBy = "model" // 默认按模型分组
	}

	plan, ok := planCostTrend(groupBy)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group_by parameter, must be one of: model, provider, intent, work_type, api_key")
		return
	}
	baseTable, baseAlias, groupColumn := plan.BaseTable, plan.BaseAlias, plan.GroupColumn

	// 解析时间范围（使用 resolveUsageTimeRange）
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 7)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tid := EffectiveTenantIDAll(r)

	// 构建查询
	var query string
	var args []any
	args = append(args, startTime, endTime) // $1, $2

	// 根据 group_by 构建不同的查询
	selectClause := fmt.Sprintf("COALESCE(%s, 'unknown') AS dimension_value", groupColumn)

	// 基础 FROM 子句：基表按维度归属选定 + 该维度需要的 JOIN
	fromClause := "FROM " + baseTable + plan.JoinClause

	whereClause := fmt.Sprintf("WHERE %s.ts >= $1 AND %s.ts < $2", baseAlias, baseAlias)
	if tid != "" {
		whereClause += fmt.Sprintf(" AND %s.tenant_id = $3", baseAlias)
		args = append(args, tid)
	}

	query = fmt.Sprintf(`
		WITH aggregated AS (
			SELECT
				%[1]s,
				COUNT(*) AS request_count,
				COALESCE(SUM(%[4]s.cost_usd), 0.0) AS total_cost_usd,
				COALESCE(SUM(%[4]s.cost_usd * %[4]s.prompt_tokens::float / NULLIF(%[4]s.total_tokens, 0)), 0.0) AS input_cost_usd,
				COALESCE(SUM(%[4]s.cost_usd * %[4]s.completion_tokens::float / NULLIF(%[4]s.total_tokens, 0)), 0.0) AS output_cost_usd,
				COALESCE(SUM(%[4]s.prompt_tokens), 0) AS prompt_tokens,
				COALESCE(SUM(%[4]s.completion_tokens), 0) AS completion_tokens,
				COALESCE(AVG(%[4]s.latency_ms), 0.0) AS avg_latency_ms,
				COALESCE(1.0 - AVG(CASE WHEN %[4]s.success THEN 1 ELSE 0 END), 0.0) AS error_rate
			%[2]s
			%[3]s
			GROUP BY dimension_value
		),
		total AS (
			SELECT SUM(total_cost_usd) AS total_cost FROM aggregated
		)
		SELECT
			a.dimension_value,
			a.request_count,
			a.total_cost_usd,
			a.input_cost_usd,
			a.output_cost_usd,
			a.prompt_tokens,
			a.completion_tokens,
			a.avg_latency_ms::int,
			a.error_rate,
			CASE WHEN t.total_cost > 0 THEN (a.total_cost_usd / t.total_cost * 100.0) ELSE 0.0 END AS percentage
		FROM aggregated a
		CROSS JOIN total t
		ORDER BY a.total_cost_usd DESC
	`, selectClause, fromClause, whereClause, baseAlias)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		if IsMissingRelationError(err) {
			ReportMissingRelation(slog.Default(), "usageCostTrend", err)
			writeJSON(w, http.StatusOK, CostTrendResponse{
				GroupBy:    groupBy,
				DateFrom:   startTime.Format("2006-01-02"),
				DateTo:     endTime.Format("2006-01-02"),
				Entries:    []CostTrendEntry{},
				OtherCost:  0,
				OtherCount: 0,
			})
			return
		}
		writeInternalErr(w, "cost-trend query failed", err)
		return
	}
	defer rows.Close()

	var entries []CostTrendEntry
	var totalCost float64
	var otherCost float64
	var otherCount int

	for rows.Next() {
		var entry CostTrendEntry
		if err := rows.Scan(
			&entry.DimensionValue,
			&entry.RequestCount,
			&entry.TotalCostUSD,
			&entry.InputCostUSD,
			&entry.OutputCostUSD,
			&entry.PromptTokens,
			&entry.CompletionTokens,
			&entry.AvgLatencyMs,
			&entry.ErrorRate,
			&entry.Percentage,
		); err != nil {
			warnRowSkip("usageCostTrend", err)
			continue
		}

		totalCost += entry.TotalCostUSD

		// 长尾处理：占比 <2% 的合并为"其他"
		if entry.Percentage < 2.0 && len(entries) >= 10 {
			otherCost += entry.TotalCostUSD
			otherCount++
		} else {
			entries = append(entries, entry)
		}
	}
	if writeAggRowsErr(w, "usageCostTrend", rows.Err()) {
		return
	}

	entries = append([]CostTrendEntry{}, entries...) // never serialize nil
	resp := CostTrendResponse{
		GroupBy:    groupBy,
		DateFrom:   startTime.Format("2006-01-02"),
		DateTo:     endTime.Format("2006-01-02"),
		TotalCost:  totalCost,
		Entries:    entries,
		OtherCost:  otherCost,
		OtherCount: otherCount,
	}

	writeJSON(w, http.StatusOK, resp)
}

// ──────────────────────────────────────────────────────────────────────────
// 2. GET /api/admin/usage/period-compare?current=2026-07&previous=2026-06
// ──────────────────────────────────────────────────────────────────────────

// PeriodStats 周期统计
type PeriodStats struct {
	Period        string  `json:"period"`           // 周期标识（如 "2026-07"）
	TotalCostUSD  float64 `json:"total_cost_usd"`   // 总成本
	TotalRequests int64   `json:"total_requests"`   // 总请求数
	TotalTokens   int64   `json:"total_tokens"`     // 总 token 数
	AvgCostPerReq float64 `json:"avg_cost_per_req"` // 平均每请求成本
	UniqueModels  int     `json:"unique_models"`    // 使用的模型数
	// UniqueSessions 曾在这里。它已于 2026-10-03 移除，理由三条，缺一不可：
	//
	//  1. usage_ledger 没有 gw_session_id（会话维度是 request_logs 的属性，
	//     从未投影进这张计费宽表）。原实现 COUNT(DISTINCT ul.gw_session_id)
	//     必然 42703，被 IsSchemaBehindError 吞掉 —— 于是 period-compare 整个
	//     查询失败却返回 200 + 全 0，把「本月花了 1139 美元」显示成「本月没花钱」。
	//  2. 换源修复它也救不回来：COUNT(DISTINCT gw_session_id) 在 request_logs
	//     上实测 30 天窗口 72s，经 ledger join 174s。交互式页面给不出这个预算。
	//  3. 它没有任何消费方（web/src 全库无渲染点；dashboard.ts 的同名字段是
	//     另一个结构）。
	//
	// 所以选择删字段而不是返回 0：一个无消费者的指标算不出来时，返回 0 与
	// 「真的是 0」在报告上无法区分，而那正是本轮要消灭的缺陷本身。
	// 真要会话数，请走 /api/admin/sessions 的既有聚合。
}

// PeriodCompareResponse 同比环比响应
type PeriodCompareResponse struct {
	Current     PeriodStats            `json:"current"`      // 当前周期
	Previous    PeriodStats            `json:"previous"`     // 对比周期
	ChangePct   float64                `json:"change_pct"`   // 变化百分比
	ChangeAbs   float64                `json:"change_abs"`   // 变化绝对值
	Trend       string                 `json:"trend"`        // up | down | flat
	Significant bool                   `json:"significant"`  // 是否显著（|变化| > 20%）
	ByDimension map[string][]DimChange `json:"by_dimension"` // 按维度细分变化

	// Degraded 标记本响应**不是**真实测量值，而是「schema 落后于代码」时的降级
	// 占位。恒发（不带 omitempty）：健康时显式 false，客户端才能断言
	// 「服务端确认过它是好的」；字段缺失与 false 在 API 语义上无法区分，
	// 那正是本字段要消灭的歧义。
	//
	// 为什么需要它：降级原本返回 200 + 全 0，与「这段时间真的没花钱」在
	// 页面上完全一样。2026-10-03 实测：2026-09 实际花费 1139.62 美元，
	// period-compare 却显示 0 —— 用户看到的是「本月没花钱」。false 会让下一个人
	// 再次以为「0 = 没有数据」。
	Degraded bool `json:"degraded"`
	// DegradedReason 运维口径的原因（缺哪个关系/列），仅降级时下发。
	DegradedReason string `json:"degraded_reason,omitempty"`
}

// DimChange 维度变化
type DimChange struct {
	DimensionValue string  `json:"dimension_value"` // 维度值
	CurrentCost    float64 `json:"current_cost"`    // 当前成本
	PreviousCost   float64 `json:"previous_cost"`   // 之前成本
	ChangePct      float64 `json:"change_pct"`      // 变化百分比
}

func (h *Handler) usagePeriodCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析周期参数（支持月份格式 YYYY-MM）
	currentPeriod := queryString(r, "current")
	previousPeriod := queryString(r, "previous")

	if currentPeriod == "" || previousPeriod == "" {
		writeError(w, http.StatusBadRequest, "both 'current' and 'previous' parameters are required (format: YYYY-MM)")
		return
	}

	// 解析并验证周期格式
	currentStart, currentEnd, err := parsePeriod(currentPeriod)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid current period: "+err.Error())
		return
	}

	previousStart, previousEnd, err := parsePeriod(previousPeriod)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid previous period: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tid := EffectiveTenantIDAll(r)

	// 查询当前周期统计
	currentStats, err := h.queryPeriodStats(ctx, tid, currentStart, currentEnd, currentPeriod)
	if err != nil {
		if IsSchemaBehindError(err) {
			// ReportSchemaBehind 本来就返回原因串，之前被丢弃了 ——
			// 于是降级载荷与「这段时间真的没花钱」完全同形。
			reason := ReportSchemaBehind(slog.Default(), "usagePeriodCompare:current", err)
			writeJSON(w, http.StatusOK, PeriodCompareResponse{
				Current:        PeriodStats{Period: currentPeriod},
				Previous:       PeriodStats{Period: previousPeriod},
				ByDimension:    map[string][]DimChange{},
				Degraded:       true,
				DegradedReason: reason,
			})
			return
		}
		writeInternalErr(w, "current period query failed", err)
		return
	}

	// 查询对比周期统计
	previousStats, err := h.queryPeriodStats(ctx, tid, previousStart, previousEnd, previousPeriod)
	if err != nil {
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "usagePeriodCompare:previous", err)
			writeJSON(w, http.StatusOK, PeriodCompareResponse{
				Current:        PeriodStats{Period: currentPeriod},
				Previous:       PeriodStats{Period: previousPeriod},
				ByDimension:    map[string][]DimChange{},
				Degraded:       true,
				DegradedReason: reason,
			})
			return
		}
		writeInternalErr(w, "previous period query failed", err)
		return
	}

	// 计算变化
	changeAbs := currentStats.TotalCostUSD - previousStats.TotalCostUSD
	var changePct float64
	if previousStats.TotalCostUSD > 0 {
		changePct = (changeAbs / previousStats.TotalCostUSD) * 100.0
	}

	trend := "flat"
	if changePct > 5.0 {
		trend = "up"
	} else if changePct < -5.0 {
		trend = "down"
	}

	significant := false
	if changePct > 20.0 || changePct < -20.0 {
		significant = true
	}

	// 按模型维度细分（可选，简化实现只返回模型维度）
	// R68 修正：原先丢弃错误，于是查询失败时 byDimension 静默少一个 key，
	// 响应里看不出「没查」与「查了但无变化」的差别。省略本身在契约内
	// （本来就可能只有模型维度），但必须留痕，否则「维度总是缺」会一直无人查。
	byDimension := make(map[string][]DimChange)
	modelChanges, dimErr := h.queryDimensionChanges(ctx, tid, currentStart, currentEnd, previousStart, previousEnd, "model")
	if dimErr != nil {
		slog.Warn("usage period compare: model-dimension query failed; dimension omitted",
			"tenant", tid, "error", dimErr)
	}
	if len(modelChanges) > 0 {
		byDimension["model"] = modelChanges
	}

	resp := PeriodCompareResponse{
		Current:     currentStats,
		Previous:    previousStats,
		ChangePct:   changePct,
		ChangeAbs:   changeAbs,
		Trend:       trend,
		Significant: significant,
		ByDimension: byDimension,
	}

	writeJSON(w, http.StatusOK, resp)
}

// parsePeriod 解析周期字符串（YYYY-MM）为时间范围
func parsePeriod(period string) (start, end time.Time, err error) {
	start, err = time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("expected format YYYY-MM")
	}
	// 月份结束 = 下月第一天
	end = start.AddDate(0, 1, 0)
	return start, end, nil
}

// queryPeriodStats 查询周期统计
func (h *Handler) queryPeriodStats(ctx context.Context, tenantID string, start, end time.Time, period string) (PeriodStats, error) {
	var stats PeriodStats
	stats.Period = period

	whereClause := "WHERE ul.ts >= $1 AND ul.ts < $2"
	args := []any{start, end}

	if tenantID != "" {
		whereClause += " AND ul.tenant_id = $3"
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(ul.cost_usd), 0.0) AS total_cost_usd,
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.total_tokens), 0) AS total_tokens,
			COUNT(DISTINCT ul.raw_model_name) AS unique_models
		FROM usage_ledger_with_current_month ul
		%s
	`, whereClause)

	// 只 SELECT 四个真实可算的指标。usage_ledger 是计费宽表，不含任何会话维度
	// 列（gw_session_id 只在 request_logs 上）；在这里引用它会让整条查询 42703，
	// 而 IsSchemaBehindError 会把失败降级成 200 + 全 0 —— 那不是降级，那是撒谎。
	// usage_ledger_sourceless_columns_test.go 把这条约束钉成回归门。
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.TotalCostUSD,
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.UniqueModels,
	)

	if err != nil {
		return stats, err
	}

	// 计算平均每请求成本
	if stats.TotalRequests > 0 {
		stats.AvgCostPerReq = stats.TotalCostUSD / float64(stats.TotalRequests)
	}

	return stats, nil
}

// queryDimensionChanges 查询维度变化（按模型）
func (h *Handler) queryDimensionChanges(ctx context.Context, tenantID string,
	currentStart, currentEnd, previousStart, previousEnd time.Time, dimension string) ([]DimChange, error) {

	whereClause := ""
	args := []any{currentStart, currentEnd, previousStart, previousEnd}

	if tenantID != "" {
		whereClause = "AND ul.tenant_id = $5"
		args = append(args, tenantID)
	}

	query := fmt.Sprintf(`
		WITH current_period AS (
			SELECT
				COALESCE(ul.raw_model_name, 'unknown') AS model,
				COALESCE(SUM(ul.cost_usd), 0.0) AS cost
			FROM usage_ledger_with_current_month ul
			WHERE ul.ts >= $1 AND ul.ts < $2 %s
			GROUP BY model
		),
		previous_period AS (
			SELECT
				COALESCE(ul.raw_model_name, 'unknown') AS model,
				COALESCE(SUM(ul.cost_usd), 0.0) AS cost
			FROM usage_ledger_with_current_month ul
			WHERE ul.ts >= $3 AND ul.ts < $4 %s
			GROUP BY model
		)
		SELECT
			COALESCE(c.model, p.model) AS model,
			COALESCE(c.cost, 0.0) AS current_cost,
			COALESCE(p.cost, 0.0) AS previous_cost,
			CASE 
				WHEN COALESCE(p.cost, 0) > 0 
				THEN ((COALESCE(c.cost, 0) - COALESCE(p.cost, 0)) / p.cost * 100.0)
				ELSE 0.0
			END AS change_pct
		FROM current_period c
		FULL OUTER JOIN previous_period p ON c.model = p.model
		WHERE COALESCE(c.cost, 0) > 0 OR COALESCE(p.cost, 0) > 0
		ORDER BY current_cost DESC
		LIMIT 10
	`, whereClause, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []DimChange
	for rows.Next() {
		var change DimChange
		if err := rows.Scan(&change.DimensionValue, &change.CurrentCost, &change.PreviousCost, &change.ChangePct); err != nil {
			warnRowSkip("queryDimensionChanges", err)
			continue
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dimension changes: %w", err)
	}

	return changes, nil
}

// ──────────────────────────────────────────────────────────────────────────
// 3. GET /api/admin/usage/cache-economics
// ──────────────────────────────────────────────────────────────────────────

// CacheEconomicsResponse 缓存经济学响应
type CacheEconomicsResponse struct {
	DateFrom           string  `json:"date_from"`            // 开始日期
	DateTo             string  `json:"date_to"`              // 结束日期
	TotalRequests      int64   `json:"total_requests"`       // 总请求数
	CacheReadTokens    int64   `json:"cache_read_tokens"`    // 缓存读取 tokens
	PromptTokens       int64   `json:"prompt_tokens"`        // 正常 prompt tokens
	CacheHitRatio      float64 `json:"cache_hit_ratio"`      // 缓存命中率
	DollarsSaved       float64 `json:"dollars_saved"`        // 节省金额（缓存）
	DollarsSpent       float64 `json:"dollars_spent"`        // 实际花费
	EffectiveCostRatio float64 `json:"effective_cost_ratio"` // 实际成本占比
	CompressedRequests int64   `json:"compressed_requests"`  // 压缩请求数
	CompressionSaved   float64 `json:"compression_saved"`    // 压缩节省（估算）
	TotalSaved         float64 `json:"total_saved"`          // 总节省
	SavingsRate        float64 `json:"savings_rate"`         // 综合节省率

	// Degraded / DegradedReason 语义见 PeriodCompareResponse 的同名字段。
	// 这个响应在 UsageCost.vue 上一次渲染 6 个指标，2026-10-03 实测降级时
	// 6 个全是 0 而页面上没有任何提示 —— 与「本月一点没花」无法区分。
	// 恒发 false，让「服务端确认过它是好的」与「字段不存在」不再同形。
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
}

func (h *Handler) usageCacheEconomics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析时间范围
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 30) // 默认 30 天
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tid := EffectiveTenantIDAll(r)

	whereClause := "WHERE ul.ts >= $1 AND ul.ts < $2"
	args := []any{startTime, endTime}

	if tid != "" {
		whereClause += " AND ul.tenant_id = $3"
		args = append(args, tid)
	}

	// 主聚合只碰 usage_ledger 真实拥有的四列。
	// 压缩请求数不在这里算：compression_strategy 只存在于 request_logs，
	// 在这里 FILTER 它必然 42703 → 整条查询失败 → IsSchemaBehindError 降级成
	// 200 + 全 0，而 UsageCost.vue 的缓存经济卡片 6 个指标全渲染 0。
	// 拿不到就降级整页，比拿错数字更糟：用户看到的是「本月没花钱」。
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(ul.prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(ul.cost_usd), 0.0) AS dollars_spent
		FROM usage_ledger_with_current_month ul
		%s
	`, whereClause)

	// compression_strategy 的原生归属是 request_logs。走 request_logs 单表而不是
	// join 回 ledger：ledger→request_logs 是 2M×2M 嵌套循环，30 天窗口实测 28s，
	// 而 request_logs 按月分区自带 ts 剪枝，同窗口实测 1.7s（2026-10-03 实测）。
	// 两表 cost 口径经核对一致（同窗口均 150.15 美元），因此这里只是换张表，
	// 不引入新的计费口径。
	compressedQuery := `
		SELECT COUNT(*)
		FROM request_logs rl
		WHERE rl.ts >= $1 AND rl.ts < $2
		  AND rl.compression_strategy IS NOT NULL
		  AND rl.compression_strategy <> ''
	`
	compressedArgs := []any{startTime, endTime}
	if tid != "" {
		compressedQuery += " AND rl.tenant_id = $3"
		compressedArgs = append(compressedArgs, tid)
	}

	var resp CacheEconomicsResponse
	var totalRequests int64
	var cacheReadTokens int64
	var promptTokens int64
	var dollarsSpent float64
	var compressedRequests int64

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&totalRequests,
		&cacheReadTokens,
		&promptTokens,
		&dollarsSpent,
	)

	// 压缩计数是附加信息，不参与主聚合。主聚合成功后才取；它失败不该让
	// dollars_spent / cache_hit_ratio 这些已经算对的指标一起作废。
	if err == nil {
		if cerr := h.db.QueryRow(ctx, compressedQuery, compressedArgs...).Scan(&compressedRequests); cerr != nil {
			slog.Warn("cache-economics: compressed request count unavailable",
				"tenant", tid, "err", cerr)
		}
	}

	if err != nil {
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "usageCacheEconomics", err)
			resp := CacheEconomicsResponse{
				DateFrom:       startTime.Format("2006-01-02"),
				DateTo:         endTime.Format("2006-01-02"),
				Degraded:       true,
				DegradedReason: reason,
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeInternalErr(w, "cache-economics query failed", err)
		return
	}

	// 计算缓存命中率
	// cache_hit_ratio = cache_read_tokens / (cache_read_tokens + prompt_tokens)
	totalCacheableTokens := cacheReadTokens + promptTokens
	cacheHitRatio := 0.0
	if totalCacheableTokens > 0 {
		cacheHitRatio = float64(cacheReadTokens) / float64(totalCacheableTokens)
	}

	// 计算节省金额
	// dollars_saved = cache_read_tokens × output_price × 0.1
	// 简化估算：假设平均 token 价格 = dollars_spent / total_tokens
	avgPricePerToken := 0.0
	if totalCacheableTokens > 0 {
		avgPricePerToken = dollarsSpent / float64(totalCacheableTokens)
	}

	// 缓存读取成本约为正常输入的 10%
	dollarsSaved := float64(cacheReadTokens) * avgPricePerToken * 0.9

	// 压缩节省估算（简化：假设每次压缩平均节省 8000 tokens）
	compressionSaved := 0.0
	if compressedRequests > 0 {
		avgSavedTokensPerCompression := 8000.0
		compressionSaved = float64(compressedRequests) * avgSavedTokensPerCompression * avgPricePerToken
	}

	// 总节省
	totalSaved := dollarsSaved + compressionSaved

	// 有效成本占比
	effectiveCostRatio := 1.0
	potentialCostWithoutOptimization := dollarsSpent + totalSaved
	if potentialCostWithoutOptimization > 0 {
		effectiveCostRatio = dollarsSpent / potentialCostWithoutOptimization
	}

	// 综合节省率
	savingsRate := 0.0
	if potentialCostWithoutOptimization > 0 {
		savingsRate = (totalSaved / potentialCostWithoutOptimization) * 100.0
	}

	resp = CacheEconomicsResponse{
		DateFrom:           startTime.Format("2006-01-02"),
		DateTo:             endTime.Format("2006-01-02"),
		TotalRequests:      totalRequests,
		CacheReadTokens:    cacheReadTokens,
		PromptTokens:       promptTokens,
		CacheHitRatio:      cacheHitRatio,
		DollarsSaved:       dollarsSaved,
		DollarsSpent:       dollarsSpent,
		EffectiveCostRatio: effectiveCostRatio,
		CompressedRequests: compressedRequests,
		CompressionSaved:   compressionSaved,
		TotalSaved:         totalSaved,
		SavingsRate:        savingsRate,
	}

	writeJSON(w, http.StatusOK, resp)
}
