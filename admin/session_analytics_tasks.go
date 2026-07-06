package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TaskAnalyticsListResponse 任务分析列表响应
type TaskAnalyticsListResponse struct {
	Tasks       []TaskAnalyticsSummary `json:"tasks"`
	Total       int                    `json:"total"`
	Limit       int                    `json:"limit"`
	Offset      int                    `json:"offset"`
	RefreshedAt time.Time              `json:"refreshed_at"`
}

// TaskAnalyticsSummary 任务分析摘要
type TaskAnalyticsSummary struct {
	TaskID             string             `json:"task_id"`
	SessionCount       int                `json:"session_count"`
	ActiveSessions24h  int                `json:"active_sessions_24h"`
	TotalRequests      int64              `json:"total_requests"`
	TotalCost          float64            `json:"total_cost_usd"`
	AvgCostPerSession  float64            `json:"avg_cost_per_session"`
	AvgHealthScore     *int               `json:"avg_health_score,omitempty"`
	HealthDistribution HealthDistribution `json:"health_distribution"`
	TotalSuccess       int64              `json:"total_success"`
	TotalErrors        int64              `json:"total_errors"`
	AvgLatencyMs       *int               `json:"avg_latency_ms,omitempty"`
	FirstSeenAt        time.Time          `json:"first_seen_at"`
	LastSeenAt         time.Time          `json:"last_seen_at"`
	ModelsUsed         []string           `json:"models_used"`
	ClientsUsed        []string           `json:"clients_used"`
}

// TaskAnalyticsDetailResponse 任务详情响应
type TaskAnalyticsDetailResponse struct {
	TaskAnalyticsSummary
	RelatedClients []RelatedClientItem   `json:"related_clients"`
	DailyCostTrend []DailyCostPoint      `json:"daily_cost_trend"`
	RecentSessions []RecentSessionItem   `json:"recent_sessions"`
}

// RelatedClientItem 关联客户端项
type RelatedClientItem struct {
	ClientID     string    `json:"client_id"`
	SessionCount int       `json:"session_count"`
	TotalCost    float64   `json:"total_cost_usd"`
	AvgHealth    *int      `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// handleTaskAnalyticsList 任务分析列表
//
// GET /api/admin/session-analytics/tasks?limit=50&offset=0&order_by=sessions
func (h *Handler) handleTaskAnalyticsList(w http.ResponseWriter, r *http.Request) {
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
		orderBy = "sessions"
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
	orderClause := "ORDER BY session_count DESC"
	switch orderBy {
	case "cost":
		orderClause = "ORDER BY total_cost_usd DESC"
	case "health":
		orderClause = "ORDER BY avg_health_score DESC NULLS LAST"
	}

	// 查询总数
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM session_task_stats %s", whereClause)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("count failed: %v", err))
		return
	}

	// 查询列表
	listSQL := fmt.Sprintf(`
		SELECT 
			task_id, session_count, active_sessions_24h, total_requests,
			total_cost_usd, avg_cost_per_session, avg_health_score,
			health_grade_a_count, health_grade_b_count, health_grade_c_count,
			health_grade_d_count, health_grade_f_count,
			total_success, total_errors, avg_latency_ms,
			first_seen_at, last_seen_at, models_used, clients_used, refreshed_at
		FROM session_task_stats
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

	tasks := []TaskAnalyticsSummary{}
	var refreshedAt time.Time

	for rows.Next() {
		var t TaskAnalyticsSummary
		var avgHealth, avgLatency sql.NullInt64
		var modelsUsed, clientsUsed []string

		err := rows.Scan(
			&t.TaskID, &t.SessionCount, &t.ActiveSessions24h, &t.TotalRequests,
			&t.TotalCost, &t.AvgCostPerSession, &avgHealth,
			&t.HealthDistribution.A, &t.HealthDistribution.B, &t.HealthDistribution.C,
			&t.HealthDistribution.D, &t.HealthDistribution.F,
			&t.TotalSuccess, &t.TotalErrors, &avgLatency,
			&t.FirstSeenAt, &t.LastSeenAt, &modelsUsed, &clientsUsed, &refreshedAt,
		)
		if err != nil {
			continue
		}

		if avgHealth.Valid {
			val := int(avgHealth.Int64)
			t.AvgHealthScore = &val
		}
		if avgLatency.Valid {
			val := int(avgLatency.Int64)
			t.AvgLatencyMs = &val
		}
		t.ModelsUsed = modelsUsed
		t.ClientsUsed = clientsUsed
		if t.ModelsUsed == nil {
			t.ModelsUsed = []string{}
		}
		if t.ClientsUsed == nil {
			t.ClientsUsed = []string{}
		}

		tasks = append(tasks, t)
	}

	writeJSON(w, http.StatusOK, &TaskAnalyticsListResponse{
		Tasks:       tasks,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		RefreshedAt: refreshedAt,
	})
}

// handleTaskAnalyticsDetail 任务详情
//
// GET /api/admin/session-analytics/tasks/:task_id?days=30
func (h *Handler) handleTaskAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从路径提取 task_id
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/tasks/")
	taskID := strings.TrimSpace(path)
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}

	days := queryInt(r, "days", 30)
	if days < 1 || days > 90 {
		days = 30
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

	resp := &TaskAnalyticsDetailResponse{}

	// 1. 查询基本统计（从物化视图）
	whereClause := "WHERE task_id = $1"
	args := []interface{}{taskID}
	if tenantID != "" {
		whereClause += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	var avgHealth, avgLatency sql.NullInt64
	var modelsUsed, clientsUsed []string
	var refreshedAt time.Time

	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT 
			task_id, session_count, active_sessions_24h, total_requests,
			total_cost_usd, avg_cost_per_session, avg_health_score,
			health_grade_a_count, health_grade_b_count, health_grade_c_count,
			health_grade_d_count, health_grade_f_count,
			total_success, total_errors, avg_latency_ms,
			first_seen_at, last_seen_at, models_used, clients_used, refreshed_at
		FROM session_task_stats
		%s
	`, whereClause), args...).Scan(
		&resp.TaskID, &resp.SessionCount, &resp.ActiveSessions24h, &resp.TotalRequests,
		&resp.TotalCost, &resp.AvgCostPerSession, &avgHealth,
		&resp.HealthDistribution.A, &resp.HealthDistribution.B, &resp.HealthDistribution.C,
		&resp.HealthDistribution.D, &resp.HealthDistribution.F,
		&resp.TotalSuccess, &resp.TotalErrors, &avgLatency,
		&resp.FirstSeenAt, &resp.LastSeenAt, &modelsUsed, &clientsUsed, &refreshedAt,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
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
	resp.ClientsUsed = clientsUsed
	if resp.ModelsUsed == nil {
		resp.ModelsUsed = []string{}
	}
	if resp.ClientsUsed == nil {
		resp.ClientsUsed = []string{}
	}

	// 2. 查询关联客户端（从客户端-任务矩阵）
	clientWhereClause := "WHERE task_id = $1"
	if tenantID != "" {
		clientWhereClause += " AND tenant_id = $2"
	}
	clientRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT client_id, session_count, total_cost_usd, avg_health_score, last_activity_at
		FROM session_client_task_matrix
		%s
		ORDER BY total_cost_usd DESC
		LIMIT 10
	`, clientWhereClause), args...)
	if err == nil {
		defer clientRows.Close()
		resp.RelatedClients = []RelatedClientItem{}
		for clientRows.Next() {
			var item RelatedClientItem
			var avgHealth sql.NullInt64
			if err := clientRows.Scan(&item.ClientID, &item.SessionCount, &item.TotalCost, &avgHealth, &item.LastActivity); err == nil {
				if avgHealth.Valid {
					val := int(avgHealth.Int64)
					item.AvgHealth = &val
				}
				resp.RelatedClients = append(resp.RelatedClients, item)
			}
		}
	}
	if resp.RelatedClients == nil {
		resp.RelatedClients = []RelatedClientItem{}
	}

	// 3. 查询每日成本趋势（从session_summaries + session_dim实时计算）
	trendArgs := []interface{}{taskID, days}
	trendWhereClause := "WHERE sd.task_id = $1 AND DATE(ss.first_request_at) >= CURRENT_DATE - $2::INT"
	if tenantID != "" {
		trendWhereClause += " AND ss.tenant_id = $3"
		trendArgs = append(trendArgs, tenantID)
	}

	trendRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT 
			DATE(ss.first_request_at) as date,
			SUM(ss.total_cost_usd) as cost,
			COUNT(*) as sessions
		FROM session_summaries ss
		INNER JOIN session_dim sd ON ss.session_key = sd.session_id AND ss.tenant_id = sd.tenant_id
		%s
		GROUP BY DATE(ss.first_request_at)
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

	// 4. 查询最近会话（从session_summaries + session_dim）
	recentArgs := args
	recentWhereClause := "WHERE sd.task_id = $1"
	if tenantID != "" {
		recentWhereClause += " AND ss.tenant_id = $2"
	}

	recentRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT ss.session_key, ss.request_count, ss.total_cost_usd, ss.health_score, ss.health_grade, ss.first_request_at
		FROM session_summaries ss
		INNER JOIN session_dim sd ON ss.session_key = sd.session_id AND ss.tenant_id = sd.tenant_id
		%s
		ORDER BY ss.last_request_at DESC
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
