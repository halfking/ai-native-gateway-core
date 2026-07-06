// Package admin — Session Analytics Timeseries API
// Task T1.1: 实现 4 个时间序列分析端点
// Ref: docs/session-management-analytics-plan.md 第 4.2.2 节
// Ref: .agents/tasks/session-management-v2.1/T1.1-timeseries-api.md

package admin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// timeseriesFilters 时间序列专用过滤器（扩展支持 granularity 和数组过滤）
type timeseriesFilters struct {
	dateFrom    time.Time
	dateTo      time.Time
	granularity string   // day/week/month
	model       []string
	provider    []string
}

// ActivityDataPoint 活动趋势数据点
type ActivityDataPoint struct {
	Date          string  `json:"date"`
	SessionCount  int     `json:"session_count"`
	RequestCount  int     `json:"request_count"`
	SuccessCount  int     `json:"success_count"`
	ErrorCount    int     `json:"error_count"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTokens   int64   `json:"total_tokens"`
	DistinctUsers int     `json:"distinct_users"`
}

// ActivityResponse 活动趋势响应
type ActivityResponse struct {
	Granularity string              `json:"granularity"`
	Series      []ActivityDataPoint `json:"series"`
	Summary     ActivitySummary     `json:"summary"`
}

// ActivitySummary 活动趋势汇总
type ActivitySummary struct {
	TotalSessions     int     `json:"total_sessions"`
	TotalRequests     int     `json:"total_requests"`
	AvgDailySessions  float64 `json:"avg_daily_sessions"`
	PeakDate          string  `json:"peak_date"`
	PeakSessions      int     `json:"peak_sessions"`
}

// CostDataPoint 成本趋势数据点
type CostDataPoint struct {
	Date             string  `json:"date"`
	InputCostUSD     float64 `json:"input_cost_usd"`
	OutputCostUSD    float64 `json:"output_cost_usd"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
}

// CostResponse 成本趋势响应
type CostResponse struct {
	Granularity string          `json:"granularity"`
	Series      []CostDataPoint `json:"series"`
	Summary     CostSummary     `json:"summary"`
}

// CostSummary 成本趋势汇总
type CostSummary struct {
	TotalCostUSD float64 `json:"total_cost_usd"`
	AvgDailyCost float64 `json:"avg_daily_cost"`
	CostTrend    string  `json:"cost_trend"` // up/down/flat
	TrendPct     float64 `json:"trend_pct"`
}

// LatencyDataPoint 延迟趋势数据点
type LatencyDataPoint struct {
	Date                  string `json:"date"`
	P50LatencyMs          int    `json:"p50_latency_ms"`
	P90LatencyMs          int    `json:"p90_latency_ms"`
	P99LatencyMs          int    `json:"p99_latency_ms"`
	MaxLatencyMs          int    `json:"max_latency_ms"`
	AvgLatencyMs          int    `json:"avg_latency_ms"`
	StreamFirstChunkP50Ms int    `json:"stream_first_chunk_p50_ms"`
}

// LatencyResponse 延迟趋势响应
type LatencyResponse struct {
	Granularity string             `json:"granularity"`
	Series      []LatencyDataPoint `json:"series"`
}

// HealthDataPoint 健康趋势数据点
type HealthDataPoint struct {
	Date                string         `json:"date"`
	AvgHealthScore      float64        `json:"avg_health_score"`
	GradeDistribution   map[string]int `json:"grade_distribution"`
	OutcomeDistribution map[string]int `json:"outcome_distribution"`
}

// HealthResponse 健康趋势响应
type HealthResponse struct {
	Granularity string            `json:"granularity"`
	Series      []HealthDataPoint `json:"series"`
}

// HandleActivityTrend GET /api/admin/session-analytics/activity
func (h *Handler) HandleActivityTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	filters, err := parseTimeseriesFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	// 构建查询
	query := `
		SELECT 
			date_trunc($1, ts)::date AS date,
			COUNT(DISTINCT gw_session_id) AS session_count,
			COUNT(*) AS request_count,
			SUM(CASE WHEN success THEN 1 ELSE 0 END) AS success_count,
			SUM(CASE WHEN NOT success THEN 1 ELSE 0 END) AS error_count,
			SUM(COALESCE(cost_usd, 0)) AS total_cost_usd,
			SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)) AS total_tokens,
			COUNT(DISTINCT end_user_id) AS distinct_users
		FROM request_logs
		WHERE ts >= $2 AND ts < $3`

	args := []interface{}{filters.granularity, filters.dateFrom, filters.dateTo}
	argIdx := 4

	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	if len(filters.model) > 0 {
		query += fmt.Sprintf(" AND upstream_model = ANY($%d)", argIdx)
		args = append(args, filters.model)
		argIdx++
	}

	if len(filters.provider) > 0 {
		query += fmt.Sprintf(" AND provider = ANY($%d)", argIdx)
		args = append(args, filters.provider)
		argIdx++
	}

	query += ` GROUP BY date_trunc($1, ts) ORDER BY date ASC`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	var series []ActivityDataPoint
	for rows.Next() {
		var dp ActivityDataPoint
		var date time.Time
		if err := rows.Scan(&date, &dp.SessionCount, &dp.RequestCount, &dp.SuccessCount,
			&dp.ErrorCount, &dp.TotalCostUSD, &dp.TotalTokens, &dp.DistinctUsers); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		dp.Date = date.Format("2006-01-02")
		series = append(series, dp)
	}

	// 缺日补零
	series = fillMissingActivityDates(series, filters.dateFrom, filters.dateTo, filters.granularity)

	// 计算汇总
	summary := calculateActivitySummary(series)

	writeJSON(w, http.StatusOK, ActivityResponse{
		Granularity: filters.granularity,
		Series:      series,
		Summary:     summary,
	})
}

// HandleCostTrend GET /api/admin/session-analytics/cost-trend
func (h *Handler) HandleCostTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	filters, err := parseTimeseriesFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	query := `
		SELECT 
			date_trunc($1, ts)::date AS date,
			SUM(COALESCE(input_cost_usd, 0)) AS input_cost_usd,
			SUM(COALESCE(output_cost_usd, 0)) AS output_cost_usd,
			SUM(COALESCE(cost_usd, 0)) AS total_cost_usd,
			SUM(COALESCE(cache_read_tokens, 0)) AS cache_read_tokens,
			SUM(COALESCE(cache_creation_tokens, 0)) AS cache_write_tokens
		FROM request_logs
		WHERE ts >= $2 AND ts < $3`

	args := []interface{}{filters.granularity, filters.dateFrom, filters.dateTo}
	argIdx := 4

	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	if len(filters.model) > 0 {
		query += fmt.Sprintf(" AND upstream_model = ANY($%d)", argIdx)
		args = append(args, filters.model)
		argIdx++
	}

	if len(filters.provider) > 0 {
		query += fmt.Sprintf(" AND provider = ANY($%d)", argIdx)
		args = append(args, filters.provider)
		argIdx++
	}

	query += ` GROUP BY date_trunc($1, ts) ORDER BY date ASC`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	var series []CostDataPoint
	for rows.Next() {
		var dp CostDataPoint
		var date time.Time
		if err := rows.Scan(&date, &dp.InputCostUSD, &dp.OutputCostUSD, &dp.TotalCostUSD,
			&dp.CacheReadTokens, &dp.CacheWriteTokens); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		dp.Date = date.Format("2006-01-02")
		series = append(series, dp)
	}

	// 缺日补零
	series = fillMissingCostDates(series, filters.dateFrom, filters.dateTo, filters.granularity)

	// 计算汇总与趋势
	summary := calculateCostSummary(series)

	writeJSON(w, http.StatusOK, CostResponse{
		Granularity: filters.granularity,
		Series:      series,
		Summary:     summary,
	})
}

// HandleLatencyTrend GET /api/admin/session-analytics/latency-trend
func (h *Handler) HandleLatencyTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	filters, err := parseTimeseriesFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	query := `
		SELECT 
			date_trunc($1, ts)::date AS date,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms)::int AS p50_latency_ms,
			percentile_cont(0.9) WITHIN GROUP (ORDER BY latency_ms)::int AS p90_latency_ms,
			percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms)::int AS p99_latency_ms,
			MAX(latency_ms) AS max_latency_ms,
			AVG(latency_ms)::int AS avg_latency_ms,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY stream_first_chunk_ms)::int, 0) AS stream_first_chunk_p50_ms
		FROM request_logs
		WHERE ts >= $2 AND ts < $3 AND latency_ms IS NOT NULL`

	args := []interface{}{filters.granularity, filters.dateFrom, filters.dateTo}
	argIdx := 4

	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	if len(filters.model) > 0 {
		query += fmt.Sprintf(" AND upstream_model = ANY($%d)", argIdx)
		args = append(args, filters.model)
		argIdx++
	}

	if len(filters.provider) > 0 {
		query += fmt.Sprintf(" AND provider = ANY($%d)", argIdx)
		args = append(args, filters.provider)
		argIdx++
	}

	query += ` GROUP BY date_trunc($1, ts) ORDER BY date ASC`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	var series []LatencyDataPoint
	for rows.Next() {
		var dp LatencyDataPoint
		var date time.Time
		if err := rows.Scan(&date, &dp.P50LatencyMs, &dp.P90LatencyMs, &dp.P99LatencyMs,
			&dp.MaxLatencyMs, &dp.AvgLatencyMs, &dp.StreamFirstChunkP50Ms); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		dp.Date = date.Format("2006-01-02")
		series = append(series, dp)
	}

	// 缺日补零
	series = fillMissingLatencyDates(series, filters.dateFrom, filters.dateTo, filters.granularity)

	writeJSON(w, http.StatusOK, LatencyResponse{
		Granularity: filters.granularity,
		Series:      series,
	})
}

// HandleHealthTrend GET /api/admin/session-analytics/health-trend
func (h *Handler) HandleHealthTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	filters, err := parseTimeseriesFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	query := `
		SELECT 
			date_trunc($1, first_request_at)::date AS date,
			AVG(health_score) AS avg_health_score,
			COUNT(*) FILTER (WHERE health_grade = 'A') AS grade_a,
			COUNT(*) FILTER (WHERE health_grade = 'B') AS grade_b,
			COUNT(*) FILTER (WHERE health_grade = 'C') AS grade_c,
			COUNT(*) FILTER (WHERE health_grade = 'D') AS grade_d,
			COUNT(*) FILTER (WHERE health_grade = 'F') AS grade_f,
			COUNT(*) FILTER (WHERE outcome = 'completed') AS outcome_completed,
			COUNT(*) FILTER (WHERE outcome = 'error') AS outcome_error,
			COUNT(*) FILTER (WHERE outcome = 'abandoned') AS outcome_abandoned
		FROM session_summaries
		WHERE first_request_at >= $2 AND first_request_at < $3 AND health_score IS NOT NULL`

	args := []interface{}{filters.granularity, filters.dateFrom, filters.dateTo}
	argIdx := 4

	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	query += ` GROUP BY date_trunc($1, first_request_at) ORDER BY date ASC`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	var series []HealthDataPoint
	for rows.Next() {
		var dp HealthDataPoint
		var date time.Time
		var avgScore *float64
		var gradeA, gradeB, gradeC, gradeD, gradeF int
		var outcomeCompleted, outcomeError, outcomeAbandoned int

		if err := rows.Scan(&date, &avgScore, &gradeA, &gradeB, &gradeC, &gradeD, &gradeF,
			&outcomeCompleted, &outcomeError, &outcomeAbandoned); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}

		dp.Date = date.Format("2006-01-02")
		if avgScore != nil {
			dp.AvgHealthScore = *avgScore
		}
		dp.GradeDistribution = map[string]int{
			"A": gradeA,
			"B": gradeB,
			"C": gradeC,
			"D": gradeD,
			"F": gradeF,
		}
		dp.OutcomeDistribution = map[string]int{
			"completed": outcomeCompleted,
			"error":     outcomeError,
			"abandoned": outcomeAbandoned,
		}
		series = append(series, dp)
	}

	// 缺日补零
	series = fillMissingHealthDates(series, filters.dateFrom, filters.dateTo, filters.granularity)

	writeJSON(w, http.StatusOK, HealthResponse{
		Granularity: filters.granularity,
		Series:      series,
	})
}

// parseTimeseriesFilters 解析时间序列专用过滤器
func parseTimeseriesFilters(r *http.Request) (*timeseriesFilters, error) {
	q := r.URL.Query()

	// 日期必填
	dateFromStr := q.Get("date_from")
	dateToStr := q.Get("date_to")

	if dateFromStr == "" || dateToStr == "" {
		return nil, fmt.Errorf("date_from and date_to are required")
	}

	dateFrom, err := time.Parse("2006-01-02", dateFromStr)
	if err != nil {
		return nil, fmt.Errorf("invalid date_from format: %w", err)
	}

	dateTo, err := time.Parse("2006-01-02", dateToStr)
	if err != nil {
		return nil, fmt.Errorf("invalid date_to format: %w", err)
	}

	// date_to 设为当天结束
	dateTo = dateTo.Add(24 * time.Hour)

	// 校验日期范围
	if dateFrom.After(dateTo) {
		return nil, fmt.Errorf("date_from must be before date_to")
	}

	// 限制最大 90 天
	if dateTo.Sub(dateFrom) > 90*24*time.Hour {
		return nil, fmt.Errorf("date range cannot exceed 90 days")
	}

	// 解析 granularity
	granularity := q.Get("granularity")
	if granularity == "" || granularity == "auto" {
		// 自动选择粒度
		days := int(dateTo.Sub(dateFrom).Hours() / 24)
		if days <= 30 {
			granularity = "day"
		} else if days <= 180 {
			granularity = "week"
		} else {
			granularity = "month"
		}
	}

	if granularity != "day" && granularity != "week" && granularity != "month" {
		return nil, fmt.Errorf("invalid granularity: must be day, week, or month")
	}

	filters := &timeseriesFilters{
		dateFrom:    dateFrom,
		dateTo:      dateTo,
		granularity: granularity,
	}

	// 解析可选过滤器（数组形式）
	if models := q.Get("model"); models != "" {
		filters.model = strings.Split(models, ",")
	}

	if providers := q.Get("provider"); providers != "" {
		filters.provider = strings.Split(providers, ",")
	}

	return filters, nil
}

// fillMissingActivityDates 填充缺失的日期（活动趋势）
func fillMissingActivityDates(series []ActivityDataPoint, dateFrom, dateTo time.Time, granularity string) []ActivityDataPoint {
	if len(series) == 0 {
		return series
	}

	existing := make(map[string]ActivityDataPoint)
	for _, dp := range series {
		existing[dp.Date] = dp
	}

	var result []ActivityDataPoint
	current := dateFrom

	for current.Before(dateTo) {
		dateStr := current.Format("2006-01-02")
		if dp, ok := existing[dateStr]; ok {
			result = append(result, dp)
		} else {
			result = append(result, ActivityDataPoint{Date: dateStr})
		}

		// 按粒度递增
		switch granularity {
		case "day":
			current = current.Add(24 * time.Hour)
		case "week":
			current = current.Add(7 * 24 * time.Hour)
		case "month":
			current = current.AddDate(0, 1, 0)
		}
	}

	return result
}

// fillMissingCostDates 填充缺失的日期（成本趋势）
func fillMissingCostDates(series []CostDataPoint, dateFrom, dateTo time.Time, granularity string) []CostDataPoint {
	if len(series) == 0 {
		return series
	}

	existing := make(map[string]CostDataPoint)
	for _, dp := range series {
		existing[dp.Date] = dp
	}

	var result []CostDataPoint
	current := dateFrom

	for current.Before(dateTo) {
		dateStr := current.Format("2006-01-02")
		if dp, ok := existing[dateStr]; ok {
			result = append(result, dp)
		} else {
			result = append(result, CostDataPoint{Date: dateStr})
		}

		switch granularity {
		case "day":
			current = current.Add(24 * time.Hour)
		case "week":
			current = current.Add(7 * 24 * time.Hour)
		case "month":
			current = current.AddDate(0, 1, 0)
		}
	}

	return result
}

// fillMissingLatencyDates 填充缺失的日期（延迟趋势）
func fillMissingLatencyDates(series []LatencyDataPoint, dateFrom, dateTo time.Time, granularity string) []LatencyDataPoint {
	if len(series) == 0 {
		return series
	}

	existing := make(map[string]LatencyDataPoint)
	for _, dp := range series {
		existing[dp.Date] = dp
	}

	var result []LatencyDataPoint
	current := dateFrom

	for current.Before(dateTo) {
		dateStr := current.Format("2006-01-02")
		if dp, ok := existing[dateStr]; ok {
			result = append(result, dp)
		} else {
			result = append(result, LatencyDataPoint{Date: dateStr})
		}

		switch granularity {
		case "day":
			current = current.Add(24 * time.Hour)
		case "week":
			current = current.Add(7 * 24 * time.Hour)
		case "month":
			current = current.AddDate(0, 1, 0)
		}
	}

	return result
}

// fillMissingHealthDates 填充缺失的日期（健康趋势）
func fillMissingHealthDates(series []HealthDataPoint, dateFrom, dateTo time.Time, granularity string) []HealthDataPoint {
	if len(series) == 0 {
		return series
	}

	existing := make(map[string]HealthDataPoint)
	for _, dp := range series {
		existing[dp.Date] = dp
	}

	var result []HealthDataPoint
	current := dateFrom

	for current.Before(dateTo) {
		dateStr := current.Format("2006-01-02")
		if dp, ok := existing[dateStr]; ok {
			result = append(result, dp)
		} else {
			result = append(result, HealthDataPoint{
				Date:                dateStr,
				GradeDistribution:   make(map[string]int),
				OutcomeDistribution: make(map[string]int),
			})
		}

		switch granularity {
		case "day":
			current = current.Add(24 * time.Hour)
		case "week":
			current = current.Add(7 * 24 * time.Hour)
		case "month":
			current = current.AddDate(0, 1, 0)
		}
	}

	return result
}

// calculateActivitySummary 计算活动趋势汇总
func calculateActivitySummary(series []ActivityDataPoint) ActivitySummary {
	var summary ActivitySummary

	for _, dp := range series {
		summary.TotalSessions += dp.SessionCount
		summary.TotalRequests += dp.RequestCount

		if dp.SessionCount > summary.PeakSessions {
			summary.PeakSessions = dp.SessionCount
			summary.PeakDate = dp.Date
		}
	}

	if len(series) > 0 {
		summary.AvgDailySessions = float64(summary.TotalSessions) / float64(len(series))
	}

	return summary
}

// calculateCostSummary 计算成本趋势汇总
func calculateCostSummary(series []CostDataPoint) CostSummary {
	var summary CostSummary

	for _, dp := range series {
		summary.TotalCostUSD += dp.TotalCostUSD
	}

	if len(series) > 0 {
		summary.AvgDailyCost = summary.TotalCostUSD / float64(len(series))

		// 计算趋势：对比前50%与后50%
		if len(series) >= 4 {
			mid := len(series) / 2
			firstHalf := series[:mid]
			secondHalf := series[mid:]

			var firstAvg, secondAvg float64
			for _, dp := range firstHalf {
				firstAvg += dp.TotalCostUSD
			}
			firstAvg /= float64(len(firstHalf))

			for _, dp := range secondHalf {
				secondAvg += dp.TotalCostUSD
			}
			secondAvg /= float64(len(secondHalf))

			if firstAvg > 0 {
				changePct := (secondAvg - firstAvg) / firstAvg * 100
				summary.TrendPct = changePct

				if math.Abs(changePct) < 5 {
					summary.CostTrend = "flat"
				} else if changePct > 0 {
					summary.CostTrend = "up"
				} else {
					summary.CostTrend = "down"
				}
			} else {
				summary.CostTrend = "flat"
			}
		} else {
			summary.CostTrend = "flat"
		}
	}

	return summary
}
