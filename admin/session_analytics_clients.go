package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ClientAnalyticsListResponse 客户端分析列表响应
type ClientAnalyticsListResponse struct {
	Clients    []ClientAnalyticsSummary `json:"clients"`
	Total      int                      `json:"total"`
	Limit      int                      `json:"limit"`
	Offset     int                      `json:"offset"`
	RefreshedAt time.Time               `json:"refreshed_at"`
}

// ClientAnalyticsSummary 客户端分析摘要
type ClientAnalyticsSummary struct {
	ClientID           string    `json:"client_id"`
	SessionCount       int       `json:"session_count"`
	ActiveSessions24h  int       `json:"active_sessions_24h"`
	TotalRequests      int64     `json:"total_requests"`
	TotalCost          float64   `json:"total_cost_usd"`
	AvgCostPerSession  float64   `json:"avg_cost_per_session"`
	AvgHealthScore     *int      `json:"avg_health_score,omitempty"`
	HealthDistribution HealthDistribution `json:"health_distribution"`
	TotalSuccess       int64     `json:"total_success"`
	TotalErrors        int64     `json:"total_errors"`
	AvgLatencyMs       *int      `json:"avg_latency_ms,omitempty"`
	FirstSeenAt        time.Time `json:"first_seen_at"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	ModelsUsed         []string  `json:"models_used"`
}

// ClientAnalyticsDetailResponse 客户端详情响应
type ClientAnalyticsDetailResponse struct {
	ClientAnalyticsSummary
	RelatedTasks    []RelatedTaskItem     `json:"related_tasks"`
	DailyCostTrend  []DailyCostPoint      `json:"daily_cost_trend"`
	RecentSessions  []RecentSessionItem   `json:"recent_sessions"`
}

// RelatedTaskItem 关联任务项
type RelatedTaskItem struct {
	TaskID       string  `json:"task_id"`
	SessionCount int     `json:"session_count"`
	TotalCost    float64 `json:"total_cost_usd"`
	AvgHealth    *int    `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// DailyCostPoint 每日成本点
type DailyCostPoint struct {
	Date     string  `json:"date"`
	Cost     float64 `json:"cost"`
	Sessions int     `json:"sessions"`
}

// RecentSessionItem 最近会话项
type RecentSessionItem struct {
	SessionID   string    `json:"session_id"`
	RequestCount int      `json:"request_count"`
	Cost        float64   `json:"cost_usd"`
	HealthScore *int      `json:"health_score,omitempty"`
	HealthGrade *string   `json:"health_grade,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// handleClientAnalyticsList 客户端分析列表
//
// GET /api/admin/session-analytics/clients?limit=50&offset=0&order_by=cost
func (h *Handler) handleClientAnalyticsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := queryInt(r, "limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := queryInt(r, "offset", 0)
	orderBy := queryString(r, "order_by") // cost | sessions | health
	if orderBy == "" {
		orderBy = "cost"
	}

	// 普通用户暂不可见（物化视图无 owner_user 列，Stage 7 修复）
	if IsRegularUser(r) {
		writeError(w, http.StatusForbidden, "client analytics requires admin access")
		return
	}

	tenantID := queryString(r, "tenant_id")
	callerTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)

	// 租户访问控制
	if !isSuper && tenantID != "" && tenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	if !isSuper && tenantID == "" {
		tenantID = callerTenant
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 构造查询
	whereClause := ""
	args := []interface{}{}
	if tenantID != "" {
		whereClause = "WHERE tenant_id = $1"
		args = append(args, tenantID)
	}

	// 排序字段
	orderClause := "ORDER BY total_cost_usd DESC"
	switch orderBy {
	case "sessions":
		orderClause = "ORDER BY session_count DESC"
	case "health":
		orderClause = "ORDER BY avg_health_score DESC NULLS LAST"
	}

	// 查询总数
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM session_client_stats %s", whereClause)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("count failed: %v", err))
		return
	}

	// 查询列表
	listSQL := fmt.Sprintf(`
		SELECT 
			client_id, session_count, active_sessions_24h, total_requests,
			total_cost_usd, avg_cost_per_session, avg_health_score,
			health_grade_a_count, health_grade_b_count, health_grade_c_count,
			health_grade_d_count, health_grade_f_count,
			total_success, total_errors, avg_latency_ms,
			first_seen_at, last_seen_at, models_used, refreshed_at
		FROM session_client_stats
		%s
		%s
		LIMIT $%d OFFSET $%d
	`, whereClause, orderClause, len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := h.db.Query(ctx, listSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
		return
	}
	defer rows.Close()

	clients := []ClientAnalyticsSummary{}
	var refreshedAt time.Time

	for rows.Next() {
		var c ClientAnalyticsSummary
		var avgHealth, avgLatency sql.NullInt64
		var modelsUsed []string

		err := rows.Scan(
			&c.ClientID, &c.SessionCount, &c.ActiveSessions24h, &c.TotalRequests,
			&c.TotalCost, &c.AvgCostPerSession, &avgHealth,
			&c.HealthDistribution.A, &c.HealthDistribution.B, &c.HealthDistribution.C,
			&c.HealthDistribution.D, &c.HealthDistribution.F,
			&c.TotalSuccess, &c.TotalErrors, &avgLatency,
			&c.FirstSeenAt, &c.LastSeenAt, &modelsUsed, &refreshedAt,
		)
		if err != nil {
			continue
		}

		if avgHealth.Valid {
			val := int(avgHealth.Int64)
			c.AvgHealthScore = &val
		}
		if avgLatency.Valid {
			val := int(avgLatency.Int64)
			c.AvgLatencyMs = &val
		}
		c.ModelsUsed = modelsUsed
		if c.ModelsUsed == nil {
			c.ModelsUsed = []string{}
		}

		clients = append(clients, c)
	}

	writeJSON(w, http.StatusOK, &ClientAnalyticsListResponse{
		Clients:     clients,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		RefreshedAt: refreshedAt,
	})
}

// handleClientAnalyticsDetail 客户端详情
//
// GET /api/admin/session-analytics/clients/:client_id?days=30
func (h *Handler) handleClientAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从路径提取 client_id
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/clients/")
	clientID := strings.TrimSpace(path)
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "client_id required")
		return
	}

		days := queryInt(r, "days", 30)
		if days < 1 || days > 90 {
			days = 30
		}

		// 普通用户暂不可见
		if IsRegularUser(r) {
			writeError(w, http.StatusForbidden, "client analytics requires admin access")
			return
		}

		tenantID := queryString(r, "tenant_id")
		callerTenant := GetTenantID(r)
		isSuper := IsSuperAdminOrLegacy(r)

	if !isSuper && tenantID != "" && tenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	if !isSuper && tenantID == "" {
		tenantID = callerTenant
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	resp := &ClientAnalyticsDetailResponse{}

	// 1. 查询基本统计（从物化视图）
	whereClause := "WHERE client_id = $1"
	args := []interface{}{clientID}
	if tenantID != "" {
		whereClause += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	var avgHealth, avgLatency sql.NullInt64
	var modelsUsed []string
	var refreshedAt time.Time

	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT 
			client_id, session_count, active_sessions_24h, total_requests,
			total_cost_usd, avg_cost_per_session, avg_health_score,
			health_grade_a_count, health_grade_b_count, health_grade_c_count,
			health_grade_d_count, health_grade_f_count,
			total_success, total_errors, avg_latency_ms,
			first_seen_at, last_seen_at, models_used, refreshed_at
		FROM session_client_stats
		%s
	`, whereClause), args...).Scan(
		&resp.ClientID, &resp.SessionCount, &resp.ActiveSessions24h, &resp.TotalRequests,
		&resp.TotalCost, &resp.AvgCostPerSession, &avgHealth,
		&resp.HealthDistribution.A, &resp.HealthDistribution.B, &resp.HealthDistribution.C,
		&resp.HealthDistribution.D, &resp.HealthDistribution.F,
		&resp.TotalSuccess, &resp.TotalErrors, &avgLatency,
		&resp.FirstSeenAt, &resp.LastSeenAt, &modelsUsed, &refreshedAt,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "client not found")
		return
	}

	if avgHealth.Valid {
		val := int(avgHealth.Int64)
		resp.AvgHealthScore = &val
	}
	if avgLatency.Valid {
		val := int(avgLatency.Int64)
		resp.AvgLatencyMs = &val
	}
	resp.ModelsUsed = modelsUsed
	if resp.ModelsUsed == nil {
		resp.ModelsUsed = []string{}
	}

	// 2. 查询关联任务（从客户端-任务矩阵）
	taskWhereClause := "WHERE client_id = $1"
	if tenantID != "" {
		taskWhereClause += " AND tenant_id = $2"
	}
	taskRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT task_id, session_count, total_cost_usd, avg_health_score, last_activity_at
		FROM session_client_task_matrix
		%s
		ORDER BY total_cost_usd DESC
		LIMIT 10
	`, taskWhereClause), args...)
	if err == nil {
		defer taskRows.Close()
		resp.RelatedTasks = []RelatedTaskItem{}
		for taskRows.Next() {
			var item RelatedTaskItem
			var avgHealth sql.NullInt64
			if err := taskRows.Scan(&item.TaskID, &item.SessionCount, &item.TotalCost, &avgHealth, &item.LastActivity); err == nil {
				if avgHealth.Valid {
					val := int(avgHealth.Int64)
					item.AvgHealth = &val
				}
				resp.RelatedTasks = append(resp.RelatedTasks, item)
			}
		}
	}
	if resp.RelatedTasks == nil {
		resp.RelatedTasks = []RelatedTaskItem{}
	}

	// 3. 查询每日成本趋势（从session_summaries实时计算）
	trendArgs := append(args, days)
	trendWhereClause := fmt.Sprintf("WHERE COALESCE(client_models[1], 'unknown') = $1 AND DATE(first_request_at) >= CURRENT_DATE - $%d::INT", len(trendArgs))
	if tenantID != "" {
		trendWhereClause += " AND tenant_id = $2"
	}

	trendRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT 
			DATE(first_request_at) as date,
			SUM(total_cost_usd) as cost,
			COUNT(*) as sessions
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date ASC
	`, trendWhereClause), trendArgs...)
	if err == nil {
		defer trendRows.Close()
		resp.DailyCostTrend = []DailyCostPoint{}
		for trendRows.Next() {
			var point DailyCostPoint
			var date time.Time
			if err := trendRows.Scan(&date, &point.Cost, &point.Sessions); err == nil {
				point.Date = date.Format("2006-01-02")
				resp.DailyCostTrend = append(resp.DailyCostTrend, point)
			}
		}
	}
	if resp.DailyCostTrend == nil {
		resp.DailyCostTrend = []DailyCostPoint{}
	}

	// 4. 查询最近会话（从session_summaries）
	recentArgs := args
	recentWhereClause := "WHERE COALESCE(client_models[1], 'unknown') = $1"
	if tenantID != "" {
		recentWhereClause += " AND tenant_id = $2"
	}

	recentRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT session_key, request_count, total_cost_usd, health_score, health_grade, first_request_at
		FROM session_summaries
		%s
		ORDER BY last_request_at DESC
		LIMIT 20
	`, recentWhereClause), recentArgs...)
	if err == nil {
		defer recentRows.Close()
		resp.RecentSessions = []RecentSessionItem{}
		for recentRows.Next() {
			var item RecentSessionItem
			var healthScore sql.NullInt64
			var healthGrade sql.NullString
			if err := recentRows.Scan(&item.SessionID, &item.RequestCount, &item.Cost, &healthScore, &healthGrade, &item.CreatedAt); err == nil {
				if healthScore.Valid {
					val := int(healthScore.Int64)
					item.HealthScore = &val
				}
				if healthGrade.Valid {
					item.HealthGrade = &healthGrade.String
				}
				resp.RecentSessions = append(resp.RecentSessions, item)
			}
		}
	}
	if resp.RecentSessions == nil {
		resp.RecentSessions = []RecentSessionItem{}
	}

	writeJSON(w, http.StatusOK, resp)
}
