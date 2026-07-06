// Package admin — Session Analytics Breakdown API
//
// 实现分析中心的 3 个分布归因 API 端点（Task T1.2）：
//   1. GET /api/admin/session-analytics/model-breakdown - 模型/提供商分解
//   2. GET /api/admin/session-analytics/session-shape    - 会话形态分布
//   3. GET /api/admin/session-analytics/health-distribution - 健康分布
//
// 参考文档：
//   - docs/session-management-analytics-plan.md §4.2.3（分布归因）
//   - docs/session-management-analytics-plan.md §11.2.5-11.2.7（详细规格）
//   - .agents/tasks/session-management-v2.1/task-planning.md T1.2
package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ── 响应类型 ────────────────────────────────────────────────────────

// ModelBreakdownResponse 模型/提供商分解响应
type ModelBreakdownResponse struct {
	ByModel    []ModelStats    `json:"by_model"`
	ByProvider []ProviderStats `json:"by_provider"`
}

// ModelStats 按模型聚合统计
type ModelStats struct {
	Model         string  `json:"model"`
	RequestCount  int     `json:"request_count"`
	SessionCount  int     `json:"session_count"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTokens   int64   `json:"total_tokens"`
	AvgLatencyMs  int     `json:"avg_latency_ms"`
	ErrorRate     float64 `json:"error_rate"`
}

// ProviderStats 按提供商聚合统计
type ProviderStats struct {
	Provider      string  `json:"provider"`
	RequestCount  int     `json:"request_count"`
	SessionCount  int     `json:"session_count"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTokens   int64   `json:"total_tokens"`
	AvgLatencyMs  int     `json:"avg_latency_ms"`
	ErrorRate     float64 `json:"error_rate"`
}

// SessionShapeResponse 会话形态分布响应
type SessionShapeResponse struct {
	RequestCountBuckets []ShapeBucket `json:"request_count_buckets"`
	DurationBuckets     []ShapeBucket `json:"duration_buckets"`
}

// ShapeBucket 形态分桶
type ShapeBucket struct {
	Range string `json:"range"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// HealthDistributionResponse 健康分布响应
type HealthDistributionResponse struct {
	GradeDistribution      map[string]int `json:"grade_distribution"`
	OutcomeDistribution    map[string]int `json:"outcome_distribution"`
	ComplianceDistribution map[string]int `json:"compliance_distribution"`
	LatencyBuckets         []ShapeBucket  `json:"latency_buckets"`
	AvgHealthScore         float64        `json:"avg_health_score"`
}

// ── 端点实现 ────────────────────────────────────────────────────────

// HandleModelBreakdown 处理 GET /api/admin/session-analytics/model-breakdown
//
// 按 outbound_model 和 provider_id 聚合请求/成本/token/延迟/错误率。
// 长尾处理：占比 <2% 的合并为"其他"。
func (h *Handler) HandleModelBreakdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// 解析通用过滤器参数
	filters, err := parseAnalyticsFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 查询按模型聚合
	byModel, err := h.queryModelBreakdown(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 查询按提供商聚合
	byProvider, err := h.queryProviderBreakdown(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 长尾合并（<2%）
	byModel = mergeLongTail(byModel, 0.02)
	byProvider = mergeProviderLongTail(byProvider, 0.02)

	writeJSON(w, http.StatusOK, ModelBreakdownResponse{
		ByModel:    byModel,
		ByProvider: byProvider,
	})
}

// HandleSessionShape 处理 GET /api/admin/session-analytics/session-shape
//
// 会话形态分布：按请求数（quick/standard/deep/marathon）和时长分桶。
func (h *Handler) HandleSessionShape(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	filters, err := parseAnalyticsFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 按请求数分桶
	requestBuckets, err := h.queryRequestCountBuckets(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 按时长分桶
	durationBuckets, err := h.queryDurationBuckets(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, SessionShapeResponse{
		RequestCountBuckets: requestBuckets,
		DurationBuckets:     durationBuckets,
	})
}

// HandleHealthDistribution 处理 GET /api/admin/session-analytics/health-distribution
//
// 健康分布：按等级（A-F）、结果（completed/error/abandoned）、合规状态、延迟分桶。
// 依赖 Phase 0 的 health_grade/outcome 列。
func (h *Handler) HandleHealthDistribution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	filters, err := parseAnalyticsFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 按等级分布
	gradeDistribution, err := h.queryGradeDistribution(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 按结果分类
	outcomeDistribution, err := h.queryOutcomeDistribution(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 按合规状态
	complianceDistribution, err := h.queryComplianceDistribution(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 延迟分桶
	latencyBuckets, err := h.queryLatencyBuckets(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 平均健康分
	avgHealthScore, err := h.queryAvgHealthScore(ctx, r, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, HealthDistributionResponse{
		GradeDistribution:      gradeDistribution,
		OutcomeDistribution:    outcomeDistribution,
		ComplianceDistribution: complianceDistribution,
		LatencyBuckets:         latencyBuckets,
		AvgHealthScore:         avgHealthScore,
	})
}

// ── 查询逻辑 ────────────────────────────────────────────────────────

// queryModelBreakdown 按模型聚合
func (h *Handler) queryModelBreakdown(ctx context.Context, r *http.Request, filters *analyticsFilters) ([]ModelStats, error) {
	where, args := buildWhereClause(r, filters, "rl")

	query := `
		WITH model_agg AS (
			SELECT 
				rl.outbound_model AS model,
				COUNT(*) AS request_count,
				COUNT(DISTINCT rl.gw_session_id) AS session_count,
				COALESCE(SUM(rl.cost_usd), 0) AS total_cost_usd,
				COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) AS total_tokens,
				COALESCE(AVG(rl.latency_ms), 0) AS avg_latency_ms,
				COUNT(*) FILTER (WHERE rl.request_status != 'success') AS error_count
			FROM request_logs_with_current_month rl
			` + where + `
			GROUP BY rl.outbound_model
		)
		SELECT 
			model,
			request_count,
			session_count,
			total_cost_usd,
			total_tokens,
			ROUND(avg_latency_ms)::int AS avg_latency_ms,
			CASE WHEN request_count > 0 THEN ROUND((error_count::numeric / request_count::numeric), 3) ELSE 0 END AS error_rate
		FROM model_agg
		ORDER BY total_cost_usd DESC
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ModelStats
	for rows.Next() {
		var s ModelStats
		if err := rows.Scan(&s.Model, &s.RequestCount, &s.SessionCount, &s.TotalCostUSD, &s.TotalTokens, &s.AvgLatencyMs, &s.ErrorRate); err != nil {
			return nil, err
		}
		result = append(result, s)
	}

	return result, rows.Err()
}

// queryProviderBreakdown 按提供商聚合
func (h *Handler) queryProviderBreakdown(ctx context.Context, r *http.Request, filters *analyticsFilters) ([]ProviderStats, error) {
	where, args := buildWhereClause(r, filters, "rl")

	query := `
		WITH provider_agg AS (
			SELECT 
				rl.provider_id AS provider,
				COUNT(*) AS request_count,
				COUNT(DISTINCT rl.gw_session_id) AS session_count,
				COALESCE(SUM(rl.cost_usd), 0) AS total_cost_usd,
				COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) AS total_tokens,
				COALESCE(AVG(rl.latency_ms), 0) AS avg_latency_ms,
				COUNT(*) FILTER (WHERE rl.request_status != 'success') AS error_count
			FROM request_logs_with_current_month rl
			` + where + `
			GROUP BY rl.provider_id
		)
		SELECT 
			provider,
			request_count,
			session_count,
			total_cost_usd,
			total_tokens,
			ROUND(avg_latency_ms)::int AS avg_latency_ms,
			CASE WHEN request_count > 0 THEN ROUND((error_count::numeric / request_count::numeric), 3) ELSE 0 END AS error_rate
		FROM provider_agg
		ORDER BY total_cost_usd DESC
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProviderStats
	for rows.Next() {
		var s ProviderStats
		if err := rows.Scan(&s.Provider, &s.RequestCount, &s.SessionCount, &s.TotalCostUSD, &s.TotalTokens, &s.AvgLatencyMs, &s.ErrorRate); err != nil {
			return nil, err
		}
		result = append(result, s)
	}

	return result, rows.Err()
}

// queryRequestCountBuckets 按请求数分桶
func (h *Handler) queryRequestCountBuckets(ctx context.Context, r *http.Request, filters *analyticsFilters) ([]ShapeBucket, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			CASE 
				WHEN request_count BETWEEN 1 AND 5 THEN '1-5'
				WHEN request_count BETWEEN 6 AND 20 THEN '6-20'
				WHEN request_count BETWEEN 21 AND 50 THEN '21-50'
				WHEN request_count > 50 THEN '>50'
				ELSE 'unknown'
			END AS range,
			CASE 
				WHEN request_count BETWEEN 1 AND 5 THEN 'quick'
				WHEN request_count BETWEEN 6 AND 20 THEN 'standard'
				WHEN request_count BETWEEN 21 AND 50 THEN 'deep'
				WHEN request_count > 50 THEN 'marathon'
				ELSE 'unknown'
			END AS label,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY range, label
		ORDER BY 
			CASE range
				WHEN '1-5' THEN 1
				WHEN '6-20' THEN 2
				WHEN '21-50' THEN 3
				WHEN '>50' THEN 4
				ELSE 5
			END
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ShapeBucket
	for rows.Next() {
		var b ShapeBucket
		if err := rows.Scan(&b.Range, &b.Label, &b.Count); err != nil {
			return nil, err
		}
		result = append(result, b)
	}

	return result, rows.Err()
}

// queryDurationBuckets 按时长分桶
func (h *Handler) queryDurationBuckets(ctx context.Context, r *http.Request, filters *analyticsFilters) ([]ShapeBucket, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			CASE 
				WHEN duration_seconds < 60 THEN '<1min'
				WHEN duration_seconds BETWEEN 60 AND 300 THEN '1-5min'
				WHEN duration_seconds BETWEEN 301 AND 1800 THEN '5-30min'
				WHEN duration_seconds BETWEEN 1801 AND 3600 THEN '30-60min'
				WHEN duration_seconds > 3600 THEN '>1h'
				ELSE 'unknown'
			END AS range,
			'' AS label,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY range
		ORDER BY 
			CASE range
				WHEN '<1min' THEN 1
				WHEN '1-5min' THEN 2
				WHEN '5-30min' THEN 3
				WHEN '30-60min' THEN 4
				WHEN '>1h' THEN 5
				ELSE 6
			END
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ShapeBucket
	for rows.Next() {
		var b ShapeBucket
		if err := rows.Scan(&b.Range, &b.Label, &b.Count); err != nil {
			return nil, err
		}
		result = append(result, b)
	}

	return result, rows.Err()
}

// queryGradeDistribution 按健康等级分布
func (h *Handler) queryGradeDistribution(ctx context.Context, r *http.Request, filters *analyticsFilters) (map[string]int, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			COALESCE(health_grade, 'unknown') AS grade,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY health_grade
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var grade string
		var count int
		if err := rows.Scan(&grade, &count); err != nil {
			return nil, err
		}
		result[grade] = count
	}

	return result, rows.Err()
}

// queryOutcomeDistribution 按结果分类分布
func (h *Handler) queryOutcomeDistribution(ctx context.Context, r *http.Request, filters *analyticsFilters) (map[string]int, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			COALESCE(outcome, 'unknown') AS outcome,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY outcome
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var outcome string
		var count int
		if err := rows.Scan(&outcome, &count); err != nil {
			return nil, err
		}
		result[outcome] = count
	}

	return result, rows.Err()
}

// queryComplianceDistribution 按合规状态分布
func (h *Handler) queryComplianceDistribution(ctx context.Context, r *http.Request, filters *analyticsFilters) (map[string]int, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			compliance_status,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY compliance_status
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}

	return result, rows.Err()
}

// queryLatencyBuckets 按延迟分桶
func (h *Handler) queryLatencyBuckets(ctx context.Context, r *http.Request, filters *analyticsFilters) ([]ShapeBucket, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT 
			CASE 
				WHEN avg_latency_ms < 1000 THEN '<1s'
				WHEN avg_latency_ms BETWEEN 1000 AND 3000 THEN '1-3s'
				WHEN avg_latency_ms BETWEEN 3001 AND 5000 THEN '3-5s'
				WHEN avg_latency_ms BETWEEN 5001 AND 10000 THEN '5-10s'
				WHEN avg_latency_ms > 10000 THEN '>10s'
				ELSE 'unknown'
			END AS range,
			'' AS label,
			COUNT(*) AS count
		FROM session_summaries ss
		` + where + `
		GROUP BY range
		ORDER BY 
			CASE range
				WHEN '<1s' THEN 1
				WHEN '1-3s' THEN 2
				WHEN '3-5s' THEN 3
				WHEN '5-10s' THEN 4
				WHEN '>10s' THEN 5
				ELSE 6
			END
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ShapeBucket
	for rows.Next() {
		var b ShapeBucket
		if err := rows.Scan(&b.Range, &b.Label, &b.Count); err != nil {
			return nil, err
		}
		result = append(result, b)
	}

	return result, rows.Err()
}

// queryAvgHealthScore 平均健康分
func (h *Handler) queryAvgHealthScore(ctx context.Context, r *http.Request, filters *analyticsFilters) (float64, error) {
	where, args := buildSessionSummariesWhereClause(r, filters)

	query := `
		SELECT COALESCE(AVG(health_score), 0)
		FROM session_summaries ss
		` + where + ` AND health_score IS NOT NULL
	`

	var avg float64
	err := h.db.QueryRow(ctx, query, args...).Scan(&avg)
	return avg, err
}

// ── 辅助函数 ────────────────────────────────────────────────────────

// analyticsFilters 通用过滤器
type analyticsFilters struct {
	dateFrom         time.Time
	dateTo           time.Time
	model            string
	provider         string
	complianceStatus string
	userIntent       string
}

// parseAnalyticsFilters 解析查询参数
func parseAnalyticsFilters(r *http.Request) (*analyticsFilters, error) {
	q := r.URL.Query()

	// 默认 7 天
	dateTo := time.Now()
	dateFrom := dateTo.AddDate(0, 0, -7)

	if df := q.Get("date_from"); df != "" {
		parsed, err := time.Parse("2006-01-02", df)
		if err != nil {
			return nil, fmt.Errorf("invalid date_from format")
		}
		dateFrom = parsed
	}

	if dt := q.Get("date_to"); dt != "" {
		parsed, err := time.Parse("2006-01-02", dt)
		if err != nil {
			return nil, fmt.Errorf("invalid date_to format")
		}
		dateTo = parsed
	}

	// 校验范围
	if dateFrom.After(dateTo) {
		return nil, fmt.Errorf("date_from must be <= date_to")
	}
	if dateTo.Sub(dateFrom).Hours()/24 > 90 {
		return nil, fmt.Errorf("date range cannot exceed 90 days")
	}

	return &analyticsFilters{
		dateFrom:         dateFrom,
		dateTo:           dateTo,
		model:            q.Get("model"),
		provider:         q.Get("provider"),
		complianceStatus: q.Get("compliance_status"),
		userIntent:       q.Get("user_intent"),
	}, nil
}

// buildWhereClause 构建 request_logs WHERE 子句
func buildWhereClause(r *http.Request, filters *analyticsFilters, alias string) (string, []interface{}) {
	where := " WHERE " + alias + ".ts >= $1 AND " + alias + ".ts < $2"
	args := []interface{}{filters.dateFrom, filters.dateTo.AddDate(0, 0, 1)}

	// 租户隔离
	tenantFrag, tenantArgs, nextIdx := tenantLogsClause(r, 3)
	where += tenantFrag
	args = append(args, tenantArgs...)

	// 模型过滤
	if filters.model != "" {
		where += fmt.Sprintf(" AND %s.outbound_model = $%d", alias, nextIdx)
		args = append(args, filters.model)
		nextIdx++
	}

	// 提供商过滤
	if filters.provider != "" {
		where += fmt.Sprintf(" AND %s.provider_id = $%d", alias, nextIdx)
		args = append(args, filters.provider)
		nextIdx++
	}

	return where, args
}

// buildSessionSummariesWhereClause 构建 session_summaries WHERE 子句
func buildSessionSummariesWhereClause(r *http.Request, filters *analyticsFilters) (string, []interface{}) {
	where := " WHERE ss.first_request_at >= $1 AND ss.first_request_at < $2"
	args := []interface{}{filters.dateFrom, filters.dateTo.AddDate(0, 0, 1)}

	// 租户隔离
	tenantFrag, tenantArgs, nextIdx := tenantSummariesClause(r, 3)
	where += tenantFrag
	args = append(args, tenantArgs...)

	// 合规状态过滤
	if filters.complianceStatus != "" {
		where += fmt.Sprintf(" AND ss.compliance_status = $%d", nextIdx)
		args = append(args, filters.complianceStatus)
		nextIdx++
	}

	// 意图过滤
	if filters.userIntent != "" {
		where += fmt.Sprintf(" AND ss.user_intent = $%d", nextIdx)
		args = append(args, filters.userIntent)
		nextIdx++
	}

	return where, args
}

// tenantSummariesClause 生成 session_summaries 租户隔离子句
func tenantSummariesClause(r *http.Request, startIdx int) (string, []interface{}, int) {
	tenantID := effectiveScopeTenant(r)
	if tenantID == "" {
		// super_admin 查全租户
		return "", nil, startIdx
	}
	return fmt.Sprintf(" AND ss.tenant_id = $%d", startIdx), []interface{}{tenantID}, startIdx + 1
}

// mergeLongTail 合并占比 <threshold 的模型为"其他"
func mergeLongTail(stats []ModelStats, threshold float64) []ModelStats {
	if len(stats) == 0 {
		return stats
	}

	// 计算总请求数
	var total int
	for _, s := range stats {
		total += s.RequestCount
	}

	if total == 0 {
		return stats
	}

	var result []ModelStats
	var othersCount int
	var othersCost float64
	var othersTokens int64

	for _, s := range stats {
		ratio := float64(s.RequestCount) / float64(total)
		if ratio >= threshold {
			result = append(result, s)
		} else {
			othersCount += s.RequestCount
			othersCost += s.TotalCostUSD
			othersTokens += s.TotalTokens
		}
	}

	// 如果有长尾，追加"其他"
	if othersCount > 0 {
		result = append(result, ModelStats{
			Model:        "其他",
			RequestCount: othersCount,
			TotalCostUSD: othersCost,
			TotalTokens:  othersTokens,
		})
	}

	return result
}

// mergeProviderLongTail 合并占比 <threshold 的提供商为"其他"
func mergeProviderLongTail(stats []ProviderStats, threshold float64) []ProviderStats {
	if len(stats) == 0 {
		return stats
	}

	var total int
	for _, s := range stats {
		total += s.RequestCount
	}

	if total == 0 {
		return stats
	}

	var result []ProviderStats
	var othersCount int
	var othersCost float64
	var othersTokens int64

	for _, s := range stats {
		ratio := float64(s.RequestCount) / float64(total)
		if ratio >= threshold {
			result = append(result, s)
		} else {
			othersCount += s.RequestCount
			othersCost += s.TotalCostUSD
			othersTokens += s.TotalTokens
		}
	}

	if othersCount > 0 {
		result = append(result, ProviderStats{
			Provider:     "其他",
			RequestCount: othersCount,
			TotalCostUSD: othersCost,
			TotalTokens:  othersTokens,
		})
	}

	return result
}
