// Package dashboardapi - session_overview.go
// 会话总览 API：提供首页 Dashboard 所需的完整会话数据
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionOverviewHandler 会话总览 Handler
type SessionOverviewHandler struct {
	db        *pgxpool.Pool
	executor  interface{} // 可选：moduleexec.Executor（用于缓存）
}

// NewSessionOverviewHandler 创建 Handler
func NewSessionOverviewHandler(db *pgxpool.Pool, executor interface{}) *SessionOverviewHandler {
	return &SessionOverviewHandler{
		db:       db,
		executor: executor,
	}
}

// SessionOverviewResponse 会话总览响应数据
type SessionOverviewResponse struct {
	// 核心指标
	TotalSessions       int                  `json:"total_sessions"`
	ActiveSessions      int                  `json:"active_sessions"`
	NewSessions24h      int                  `json:"new_sessions_24h"`
	ClosedSessions24h   int                  `json:"closed_sessions_24h"`

	// 健康度分布
	HealthDistribution  HealthDistribution   `json:"health_distribution"`

	// 合规状态
	ComplianceStats     ComplianceStats      `json:"compliance_stats"`

	// 成本统计
	CostStats           CostStats            `json:"cost_stats"`

	// 模型使用
	ModelUsage          []ModelUsageItem     `json:"model_usage"`

	// Top 排行
	TopClients          []ClientRankItem     `json:"top_clients"`
	TopTasks            []TaskRankItem       `json:"top_tasks"`

	// 趋势数据
	CostTrend           []CostTrendPoint     `json:"cost_trend"`
	SessionTrend        []SessionTrendPoint  `json:"session_trend"`

	// 时间戳
	GeneratedAt         time.Time            `json:"generated_at"`
	PeriodStart         time.Time            `json:"period_start"`
	PeriodEnd           time.Time            `json:"period_end"`
}

// HealthDistribution 健康度分布
type HealthDistribution struct {
	Total int `json:"total"`
	A     int `json:"a"` // 90-100
	B     int `json:"b"` // 75-89
	C     int `json:"c"` // 60-74
	D     int `json:"d"` // 40-59
	F     int `json:"f"` // 0-39

	// 百分比（自动计算）
	APercent float64 `json:"a_percent"`
	BPercent float64 `json:"b_percent"`
	CPercent float64 `json:"c_percent"`
	DPercent float64 `json:"d_percent"`
	FPercent float64 `json:"f_percent"`

	AvgScore float64 `json:"avg_score"`
}

// ComplianceStats 合规统计
type ComplianceStats struct {
	Total              int     `json:"total"`
	Compliant          int     `json:"compliant"`
	Warning            int     `json:"warning"`
	Violation          int     `json:"violation"`
	PromptInjection    int     `json:"prompt_injection_detected"`
	PIIDetected        int     `json:"pii_detected"`
	ToxicOutput        int     `json:"toxic_output_detected"`
	ComplianceRate     float64 `json:"compliance_rate"`
}

// CostStats 成本统计
type CostStats struct {
	TotalCostUSD       float64 `json:"total_cost_usd"`
	AvgCostPerSession  float64 `json:"avg_cost_per_session"`
	AvgCostPerRequest  float64 `json:"avg_cost_per_request"`
	MaxCostSession     float64 `json:"max_cost_session"`
	InputCostUSD       float64 `json:"input_cost_usd"`
	OutputCostUSD      float64 `json:"output_cost_usd"`
	CostGrowthPct      float64 `json:"cost_growth_pct"` // 相比上一周期
}

// ModelUsageItem 模型使用项
type ModelUsageItem struct {
	Model            string  `json:"model"`
	SessionCount     int     `json:"session_count"`
	RequestCount     int     `json:"request_count"`
	TotalCost        float64 `json:"total_cost"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	SuccessRate      float64 `json:"success_rate"`
}

// ClientRankItem 客户端排行
type ClientRankItem struct {
	ClientID      string  `json:"client_id"`
	SessionCount  int     `json:"session_count"`
	TotalCost     float64 `json:"total_cost"`
	AvgHealth     *int    `json:"avg_health,omitempty"`
	LastActivity  time.Time `json:"last_activity"`
}

// TaskRankItem 任务排行
type TaskRankItem struct {
	TaskID        string  `json:"task_id"`
	SessionCount  int     `json:"session_count"`
	TotalCost     float64 `json:"total_cost"`
	AvgHealth     *int    `json:"avg_health,omitempty"`
	LastActivity  time.Time `json:"last_activity"`
}

// CostTrendPoint 成本趋势点
type CostTrendPoint struct {
	Date      string  `json:"date"`      // YYYY-MM-DD
	Cost      float64 `json:"cost"`
	Sessions  int     `json:"sessions"`
	Requests  int     `json:"requests"`
}

// SessionTrendPoint 会话趋势点
type SessionTrendPoint struct {
	Date         string `json:"date"`
	NewSessions  int    `json:"new_sessions"`
	ActiveCount  int    `json:"active_count"`
	ClosedCount  int    `json:"closed_count"`
}

// HandleSessionOverview 处理会话总览请求
//
// GET /api/admin/dashboard/session-overview
//
// 查询参数：
//   - days: 时间范围（1/7/30/90，默认 7）
//   - tenant_id: 租户 ID（可选，超级管理员可用）
//   - refresh: 是否强制刷新缓存（可选）
func (h *SessionOverviewHandler) HandleSessionOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-overview", apiStatus, time.Since(startTime))
	}()
	params := ParseQueryParams(r)

	ctx, cancel := GetRequestContext(r, 30*time.Second)
	defer cancel()

	// 1. 查询总会话数和活跃会话数
	totalStats, err := h.queryTotalStats(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query total stats", err.Error())
		return
	}

	// 2. 查询健康度分布
	healthDist, err := h.queryHealthDistribution(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query health distribution", err.Error())
		return
	}

	// 3. 查询合规统计
	compliance, err := h.queryComplianceStats(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query compliance stats", err.Error())
		return
	}

	// 4. 查询成本统计
	costStats, err := h.queryCostStats(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query cost stats", err.Error())
		return
	}

	// 5. 查询模型使用
	modelUsage, err := h.queryModelUsage(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query model usage", err.Error())
		return
	}

	// 6. 查询 Top 客户端和任务
	topClients, err := h.queryTopClients(ctx, params, 5)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query top clients", err.Error())
		return
	}

	topTasks, err := h.queryTopTasks(ctx, params, 5)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query top tasks", err.Error())
		return
	}

	// 7. 查询趋势数据
	costTrend, err := h.queryCostTrend(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query cost trend", err.Error())
		return
	}

	sessionTrend, err := h.querySessionTrend(ctx, params)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query session trend", err.Error())
		return
	}

	// 构建响应
	now := time.Now()
	periodEnd := now
	periodStart := now.AddDate(0, 0, -params.Days)

	resp := SessionOverviewResponse{
		TotalSessions:      totalStats.Total,
		ActiveSessions:     totalStats.Active,
		NewSessions24h:     totalStats.New24h,
		ClosedSessions24h:  totalStats.Closed24h,
		HealthDistribution: *healthDist,
		ComplianceStats:    *compliance,
		CostStats:          *costStats,
		ModelUsage:         modelUsage,
		TopClients:         topClients,
		TopTasks:           topTasks,
		CostTrend:          costTrend,
		SessionTrend:       sessionTrend,
		GeneratedAt:        now,
		PeriodStart:        periodStart,
		PeriodEnd:          periodEnd,
	}

	metadata := &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
		CacheHit:    false,
	}

	// 写入响应
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60") // 客户端缓存 60 秒
	w.WriteHeader(http.StatusOK)
	writeSuccessJSON(w, resp, metadata)
}

// ────────────────────────────────────────────────────────────────
// 内部数据结构
// ────────────────────────────────────────────────────────────────

type totalStatsInternal struct {
	Total    int
	Active   int
	New24h   int
	Closed24h int
}

func (h *SessionOverviewHandler) queryTotalStats(ctx context.Context, params QueryParams) (*totalStatsInternal, error) {
	stats := &totalStatsInternal{}

	// 构建查询条件
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + joinStrings(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active,
			COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '24 hours') as new_24h,
			COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_24h
		FROM session_summaries
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.Total,
		&stats.Active,
		&stats.New24h,
		&stats.Closed24h,
	)
	return stats, err
}

func (h *SessionOverviewHandler) queryHealthDistribution(ctx context.Context, params QueryParams) (*HealthDistribution, error) {
	dist := &HealthDistribution{}

	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + joinStrings(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE health_grade = 'A') as a,
			COUNT(*) FILTER (WHERE health_grade = 'B') as b,
			COUNT(*) FILTER (WHERE health_grade = 'C') as c,
			COUNT(*) FILTER (WHERE health_grade = 'D') as d,
			COUNT(*) FILTER (WHERE health_grade = 'F') as f,
			COALESCE(AVG(health_score), 0) as avg_score
		FROM session_summaries
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&dist.Total,
		&dist.A, &dist.B, &dist.C, &dist.D, &dist.F,
		&dist.AvgScore,
	)
	if err != nil {
		return nil, err
	}

	// 计算百分比
	if dist.Total > 0 {
		dist.APercent = float64(dist.A) * 100 / float64(dist.Total)
		dist.BPercent = float64(dist.B) * 100 / float64(dist.Total)
		dist.CPercent = float64(dist.C) * 100 / float64(dist.Total)
		dist.DPercent = float64(dist.D) * 100 / float64(dist.Total)
		dist.FPercent = float64(dist.F) * 100 / float64(dist.Total)
	}

	return dist, nil
}

func (h *SessionOverviewHandler) queryComplianceStats(ctx context.Context, params QueryParams) (*ComplianceStats, error) {
	stats := &ComplianceStats{}

	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + joinStrings(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE compliance_status = 'compliant' OR compliance_status IS NULL) as compliant,
			COUNT(*) FILTER (WHERE compliance_status = 'warning') as warning,
			COUNT(*) FILTER (WHERE compliance_status = 'violation') as violation,
			COUNT(*) FILTER (WHERE prompt_injection_detected = true) as prompt_injection,
			COUNT(*) FILTER (WHERE pii_detected = true) as pii,
			COUNT(*) FILTER (WHERE toxic_output_detected = true) as toxic
		FROM session_summaries
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.Total,
		&stats.Compliant,
		&stats.Warning,
		&stats.Violation,
		&stats.PromptInjection,
		&stats.PIIDetected,
		&stats.ToxicOutput,
	)
	if err != nil {
		return nil, err
	}

	if stats.Total > 0 {
		stats.ComplianceRate = float64(stats.Compliant) * 100 / float64(stats.Total)
	}

	return stats, nil
}

func (h *SessionOverviewHandler) queryCostStats(ctx context.Context, params QueryParams) (*CostStats, error) {
	stats := &CostStats{}

	// 当前周期
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COALESCE(AVG(total_cost_usd), 0) as avg_cost,
			COALESCE(MAX(total_cost_usd), 0) as max_cost,
			COALESCE(SUM(input_cost_usd), 0) as input_cost,
			COALESCE(SUM(output_cost_usd), 0) as output_cost,
			COALESCE(SUM(request_count), 0) as total_requests
		FROM session_summaries
		%s
	`, whereClause)

	var totalRequests int64
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.TotalCostUSD,
		&stats.AvgCostPerSession,
		&stats.MaxCostSession,
		&stats.InputCostUSD,
		&stats.OutputCostUSD,
		&totalRequests,
	)
	if err != nil {
		return nil, err
	}

	if totalRequests > 0 {
		stats.AvgCostPerRequest = stats.TotalCostUSD / float64(totalRequests)
	}

	// 计算成本增长率（与上一周期对比）
	prevWhere := append([]string{}, where...)
	prevWhere[len(prevWhere)-1] = fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days' AND first_request_at < NOW() - INTERVAL '%d days'", params.Days*2, params.Days)
	prevWhereClause := "WHERE " + joinStrings(prevWhere, " AND ")

	var prevTotalCost float64
	prevQuery := fmt.Sprintf(`
		SELECT COALESCE(SUM(total_cost_usd), 0)
		FROM session_summaries
		%s
	`, prevWhereClause)

	_ = h.db.QueryRow(ctx, prevQuery, args...).Scan(&prevTotalCost)

	if prevTotalCost > 0 {
		stats.CostGrowthPct = (stats.TotalCostUSD - prevTotalCost) * 100 / prevTotalCost
	}

	return stats, nil
}

func (h *SessionOverviewHandler) queryModelUsage(ctx context.Context, params QueryParams) ([]ModelUsageItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			model,
			COUNT(*) as session_count,
			COALESCE(SUM(request_count), 0) as request_count,
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COALESCE(AVG(avg_latency_ms), 0) as avg_latency,
			CASE WHEN SUM(request_count) > 0 
			     THEN SUM(success_count)::FLOAT / SUM(request_count) 
			     ELSE 0 END as success_rate
		FROM (
			SELECT 
				unnest(models_used) as model,
				tenant_id,
				first_request_at,
				request_count,
				total_cost_usd,
				avg_latency_ms,
				success_count
			FROM session_summaries
		) t
		%s
		GROUP BY model
		ORDER BY total_cost DESC
		LIMIT 10
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ModelUsageItem, 0)
	for rows.Next() {
		var item ModelUsageItem
		if err := rows.Scan(
			&item.Model,
			&item.SessionCount,
			&item.RequestCount,
			&item.TotalCost,
			&item.AvgLatencyMs,
			&item.SuccessRate,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, nil
}

func (h *SessionOverviewHandler) queryTopClients(ctx context.Context, params QueryParams, limit int) ([]ClientRankItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(client_id, 'unknown') as client_id,
			COUNT(*) as session_count,
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			MAX(last_request_at) as last_activity
		FROM session_summaries
		%s
		GROUP BY client_id
		ORDER BY total_cost DESC
		LIMIT $%d
	`, whereClause, argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ClientRankItem, 0)
	for rows.Next() {
		var item ClientRankItem
		if err := rows.Scan(&item.ClientID, &item.SessionCount, &item.TotalCost, &item.LastActivity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *SessionOverviewHandler) queryTopTasks(ctx context.Context, params QueryParams, limit int) ([]TaskRankItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(task_id, 'unknown') as task_id,
			COUNT(*) as session_count,
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			MAX(last_request_at) as last_activity
		FROM session_summaries
		%s
		GROUP BY task_id
		ORDER BY session_count DESC
		LIMIT $%d
	`, whereClause, argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]TaskRankItem, 0)
	for rows.Next() {
		var item TaskRankItem
		if err := rows.Scan(&item.TaskID, &item.SessionCount, &item.TotalCost, &item.LastActivity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *SessionOverviewHandler) queryCostTrend(ctx context.Context, params QueryParams) ([]CostTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			DATE(first_request_at) as date,
			COALESCE(SUM(total_cost_usd), 0) as cost,
			COUNT(*) as sessions,
			COALESCE(SUM(request_count), 0) as requests
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]CostTrendPoint, 0)
	for rows.Next() {
		var item CostTrendPoint
		if err := rows.Scan(&item.Date, &item.Cost, &item.Sessions, &item.Requests); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *SessionOverviewHandler) querySessionTrend(ctx context.Context, params QueryParams) ([]SessionTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			DATE(first_request_at) as date,
			COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '%d days') as new_sessions,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active_count,
			COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_count
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date
	`, params.Days, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SessionTrendPoint, 0)
	for rows.Next() {
		var item SessionTrendPoint
		if err := rows.Scan(&item.Date, &item.NewSessions, &item.ActiveCount, &item.ClosedCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// 注：joinStrings、writeSuccessJSON、writeErrorJSON 已定义在 types.go