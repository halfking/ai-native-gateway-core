package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

// SessionOverviewResponse 会话统计概览响应
type SessionOverviewResponse struct {
	TotalSessions      int                      `json:"total_sessions"`
	ActiveSessions     int                      `json:"active_sessions"`
	HealthDistribution HealthDistribution       `json:"health_distribution"`
	CostTrend          []CostTrendPoint         `json:"cost_trend"`
	TopClients         []ClientRankItem         `json:"top_clients"`
	TopTasks           []TaskRankItem           `json:"top_tasks"`
	LastUpdated        time.Time                `json:"last_updated"`
}

// HealthDistribution 健康度分布
type HealthDistribution struct {
	A int `json:"a"` // 90-100
	B int `json:"b"` // 75-89
	C int `json:"c"` // 60-74
	D int `json:"d"` // 40-59
	F int `json:"f"` // 0-39
}

// CostTrendPoint 成本趋势点
type CostTrendPoint struct {
	Date     string  `json:"date"`      // YYYY-MM-DD
	Cost     float64 `json:"cost"`      // 当日总成本
	Sessions int     `json:"sessions"`  // 当日会话数
}

// ClientRankItem 客户端排行项
type ClientRankItem struct {
	ClientID     string  `json:"client_id"`
	SessionCount int     `json:"session_count"`
	TotalCost    float64 `json:"total_cost"`
	AvgHealth    *int    `json:"avg_health,omitempty"`
}

// TaskRankItem 任务排行项
type TaskRankItem struct {
	TaskID       string  `json:"task_id"`
	SessionCount int     `json:"session_count"`
	TotalCost    float64 `json:"total_cost"`
	AvgHealth    *int    `json:"avg_health,omitempty"`
}

// handleDashboardSessionOverview 处理首页会话统计请求
//
// GET /api/admin/dashboard/session-overview?days=7
//
// 租户隔离：tenant_admin 只能看自己租户，super_admin 可看全局或指定租户
func (h *Handler) handleDashboardSessionOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析参数
	days := queryInt(r, "days", 7)
	if days < 1 || days > 90 {
		days = 7
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp := &SessionOverviewResponse{
		LastUpdated: time.Now(),
	}

	// 构造 WHERE 子句
	whereClause := ""
	args := []interface{}{}
	if tenantID != "" {
		whereClause = "WHERE ss.tenant_id = $1"
		args = append(args, tenantID)
	}

	// 1. 查询总会话数和活跃会话数
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active
		FROM session_summaries ss
		%s
	`, whereClause), args...).Scan(&resp.TotalSessions, &resp.ActiveSessions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query total failed: %v", err))
		return
	}

	// 2. 查询健康度分布
	healthDist := &resp.HealthDistribution
	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT 
			COUNT(*) FILTER (WHERE health_grade = 'A') as a,
			COUNT(*) FILTER (WHERE health_grade = 'B') as b,
			COUNT(*) FILTER (WHERE health_grade = 'C') as c,
			COUNT(*) FILTER (WHERE health_grade = 'D') as d,
			COUNT(*) FILTER (WHERE health_grade = 'F') as f
		FROM session_summaries ss
		%s
	`, whereClause), args...).Scan(&healthDist.A, &healthDist.B, &healthDist.C, &healthDist.D, &healthDist.F)
	if err != nil {
		// 非阻塞错误，继续
		healthDist.A = 0
		healthDist.B = 0
		healthDist.C = 0
		healthDist.D = 0
		healthDist.F = 0
	}

	// 3. 查询成本趋势（最近N天）
	trendArgs := make([]interface{}, len(args)+1)
	copy(trendArgs, args)
	trendArgs[len(trendArgs)-1] = days
	trendWhereClause := whereClause
	trendParamIdx := len(args) + 1
	if whereClause == "" {
		trendWhereClause = fmt.Sprintf("WHERE DATE(ss.first_request_at) >= CURRENT_DATE - INTERVAL '%d days'", days)
		trendArgs = []interface{}{days}
		trendParamIdx = 1
	} else {
		trendWhereClause = fmt.Sprintf("%s AND DATE(ss.first_request_at) >= CURRENT_DATE - $%d::INT", whereClause, trendParamIdx)
	}

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT 
			DATE(ss.first_request_at) as date,
			SUM(ss.total_cost_usd) as cost,
			COUNT(*) as sessions
		FROM session_summaries ss
		%s
		GROUP BY DATE(ss.first_request_at)
		ORDER BY date DESC
		LIMIT $%d
	`, trendWhereClause, trendParamIdx+1), append(trendArgs, days)...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var point CostTrendPoint
			var date time.Time
			if err := rows.Scan(&date, &point.Cost, &point.Sessions); err == nil {
				point.Date = date.Format("2006-01-02")
				resp.CostTrend = append(resp.CostTrend, point)
			}
		}
	}
	// 反转趋势数据（从旧到新）
	for i, j := 0, len(resp.CostTrend)-1; i < j; i, j = i+1, j-1 {
		resp.CostTrend[i], resp.CostTrend[j] = resp.CostTrend[j], resp.CostTrend[i]
	}
	if resp.CostTrend == nil {
		resp.CostTrend = []CostTrendPoint{}
	}

	// 4. 查询Top5客户端（按成本）
	// 使用 session_summaries 的 client_models 字段（TEXT[] 类型）
	// 注意：client_models 可能为空或包含多个值，这里取第一个作为 client_id
	clientRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT 
			COALESCE(client_models[1], 'unknown') as client_id,
			COUNT(*) as session_count,
			SUM(total_cost_usd) as total_cost,
			AVG(health_score)::INT as avg_health
		FROM session_summaries ss
		%s
		GROUP BY client_models[1]
		ORDER BY total_cost DESC
		LIMIT 5
	`, whereClause), args...)
	if err == nil {
		defer clientRows.Close()
		for clientRows.Next() {
			var item ClientRankItem
			var avgHealth sql.NullInt64
			if err := clientRows.Scan(&item.ClientID, &item.SessionCount, &item.TotalCost, &avgHealth); err == nil {
				if avgHealth.Valid {
					val := int(avgHealth.Int64)
					item.AvgHealth = &val
				}
				resp.TopClients = append(resp.TopClients, item)
			}
		}
	}
	if resp.TopClients == nil {
		resp.TopClients = []ClientRankItem{}
	}

	// 5. 查询Top5任务（按会话数）
	// 需要关联 session_dim 表获取 task_id
	taskJoin := ""
	if tenantID != "" {
		taskJoin = `
		LEFT JOIN session_dim sd ON ss.session_key = sd.session_id AND sd.tenant_id = $1
		`
	} else {
		taskJoin = `
		LEFT JOIN session_dim sd ON ss.session_key = sd.session_id
		`
	}

	taskRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT 
			COALESCE(sd.task_id, 'unknown') as task_id,
			COUNT(*) as session_count,
			SUM(ss.total_cost_usd) as total_cost,
			AVG(ss.health_score)::INT as avg_health
		FROM session_summaries ss
		%s
		%s
		GROUP BY sd.task_id
		ORDER BY session_count DESC
		LIMIT 5
	`, taskJoin, whereClause), args...)
	if err == nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var item TaskRankItem
			var avgHealth sql.NullInt64
			if err := taskRows.Scan(&item.TaskID, &item.SessionCount, &item.TotalCost, &avgHealth); err == nil {
				if avgHealth.Valid {
					val := int(avgHealth.Int64)
					item.AvgHealth = &val
				}
				resp.TopTasks = append(resp.TopTasks, item)
			}
		}
	}
	if resp.TopTasks == nil {
		resp.TopTasks = []TaskRankItem{}
	}

	writeJSON(w, http.StatusOK, resp)
}
