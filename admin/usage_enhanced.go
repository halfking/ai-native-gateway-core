// Package admin — Usage Enhanced API (T1.4)
//
// 用量成本增强端点：
//   GET /api/admin/usage/cost-trend?group_by=model|provider|intent|work_type|api_key
//   GET /api/admin/usage/period-compare?current=2026-07&previous=2026-06
//   GET /api/admin/usage/cache-economics?date_from=&date_to=
//
// 参考文档：docs/session-management-analytics-plan.md 第 4.6 节
package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
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
	GroupBy     string            `json:"group_by"`      // 分组维度
	DateFrom    string            `json:"date_from"`     // 开始日期
	DateTo      string            `json:"date_to"`       // 结束日期
	TotalCost   float64           `json:"total_cost"`    // 总成本
	Entries     []CostTrendEntry  `json:"entries"`       // 分组条目
	OtherCost   float64           `json:"other_cost"`    // 其他（占比<2%合并）
	OtherCount  int               `json:"other_count"`   // 其他条目数量
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
	
	// 验证 group_by 参数
	validGroupBy := map[string]string{
		"model":     "ul.raw_model_name",
		"provider":  "p.code",
		"intent":    "ss.user_intent",
		"work_type": "ul.work_type",
		"api_key":   "ak.key_prefix",
	}
	
	groupColumn, ok := validGroupBy[groupBy]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group_by parameter, must be one of: model, provider, intent, work_type, api_key")
		return
	}

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
	
	// 基础 FROM 子句
	fromClause := "FROM usage_ledger_with_current_month ul"
	
	// 根据 group_by 添加必要的 JOIN
	switch groupBy {
	case "provider":
		fromClause += " LEFT JOIN providers p ON p.id = ul.provider_id"
	case "intent", "work_type":
		fromClause += " LEFT JOIN session_summaries ss ON ss.session_key = ul.gw_session_id"
	case "api_key":
		fromClause += " LEFT JOIN api_keys ak ON ak.id = ul.api_key_id"
	}

	whereClause := "WHERE ul.ts >= $1 AND ul.ts < $2"
	if tid != "" {
		whereClause += " AND ul.tenant_id = $3"
		args = append(args, tid)
	}

	query = fmt.Sprintf(`
		WITH aggregated AS (
			SELECT
				%s,
				COUNT(*) AS request_count,
				COALESCE(SUM(ul.cost_usd), 0.0) AS total_cost_usd,
				COALESCE(SUM(ul.cost_usd * ul.prompt_tokens::float / NULLIF(ul.total_tokens, 0)), 0.0) AS input_cost_usd,
				COALESCE(SUM(ul.cost_usd * ul.completion_tokens::float / NULLIF(ul.total_tokens, 0)), 0.0) AS output_cost_usd,
				COALESCE(SUM(ul.prompt_tokens), 0) AS prompt_tokens,
				COALESCE(SUM(ul.completion_tokens), 0) AS completion_tokens,
				COALESCE(AVG(ul.latency_ms), 0.0) AS avg_latency_ms,
				COALESCE(1.0 - AVG(CASE WHEN ul.success THEN 1 ELSE 0 END), 0.0) AS error_rate
			%s
			%s
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
	`, selectClause, fromClause, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cost-trend query failed: "+err.Error())
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
	Period           string  `json:"period"`            // 周期标识（如 "2026-07"）
	TotalCostUSD     float64 `json:"total_cost_usd"`    // 总成本
	TotalRequests    int64   `json:"total_requests"`    // 总请求数
	TotalTokens      int64   `json:"total_tokens"`      // 总 token 数
	AvgCostPerReq    float64 `json:"avg_cost_per_req"`  // 平均每请求成本
	UniqueModels     int     `json:"unique_models"`     // 使用的模型数
	UniqueSessions   int     `json:"unique_sessions"`   // 会话数
}

// PeriodCompareResponse 同比环比响应
type PeriodCompareResponse struct {
	Current       PeriodStats            `json:"current"`        // 当前周期
	Previous      PeriodStats            `json:"previous"`       // 对比周期
	ChangePct     float64                `json:"change_pct"`     // 变化百分比
	ChangeAbs     float64                `json:"change_abs"`     // 变化绝对值
	Trend         string                 `json:"trend"`          // up | down | flat
	Significant   bool                   `json:"significant"`    // 是否显著（|变化| > 20%）
	ByDimension   map[string][]DimChange `json:"by_dimension"`   // 按维度细分变化
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
		writeError(w, http.StatusInternalServerError, "current period query failed: "+err.Error())
		return
	}

	// 查询对比周期统计
	previousStats, err := h.queryPeriodStats(ctx, tid, previousStart, previousEnd, previousPeriod)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "previous period query failed: "+err.Error())
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
	byDimension := make(map[string][]DimChange)
	modelChanges, _ := h.queryDimensionChanges(ctx, tid, currentStart, currentEnd, previousStart, previousEnd, "model")
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
			COUNT(DISTINCT ul.raw_model_name) AS unique_models,
			COUNT(DISTINCT ul.gw_session_id) AS unique_sessions
		FROM usage_ledger_with_current_month ul
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.TotalCostUSD,
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.UniqueModels,
		&stats.UniqueSessions,
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
			continue
		}
		changes = append(changes, change)
	}

	return changes, nil
}

// ──────────────────────────────────────────────────────────────────────────
// 3. GET /api/admin/usage/cache-economics
// ──────────────────────────────────────────────────────────────────────────

// CacheEconomicsResponse 缓存经济学响应
type CacheEconomicsResponse struct {
	DateFrom             string  `json:"date_from"`               // 开始日期
	DateTo               string  `json:"date_to"`                 // 结束日期
	TotalRequests        int64   `json:"total_requests"`          // 总请求数
	CacheReadTokens      int64   `json:"cache_read_tokens"`       // 缓存读取 tokens
	PromptTokens         int64   `json:"prompt_tokens"`           // 正常 prompt tokens
	CacheHitRatio        float64 `json:"cache_hit_ratio"`         // 缓存命中率
	DollarsSaved         float64 `json:"dollars_saved"`           // 节省金额（缓存）
	DollarsSpent         float64 `json:"dollars_spent"`           // 实际花费
	EffectiveCostRatio   float64 `json:"effective_cost_ratio"`    // 实际成本占比
	CompressedRequests   int     `json:"compressed_requests"`     // 压缩请求数
	CompressionSaved     float64 `json:"compression_saved"`       // 压缩节省（估算）
	TotalSaved           float64 `json:"total_saved"`             // 总节省
	SavingsRate          float64 `json:"savings_rate"`            // 综合节省率
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

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(ul.prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(ul.cost_usd), 0.0) AS dollars_spent,
			COUNT(*) FILTER (WHERE ul.compression_strategy IS NOT NULL AND ul.compression_strategy <> '') AS compressed_requests
		FROM usage_ledger_with_current_month ul
		%s
	`, whereClause)

	var resp CacheEconomicsResponse
	var totalRequests int64
	var cacheReadTokens int64
	var promptTokens int64
	var dollarsSpent float64
	var compressedRequests int

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&totalRequests,
		&cacheReadTokens,
		&promptTokens,
		&dollarsSpent,
		&compressedRequests,
	)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "cache-economics query failed: "+err.Error())
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
