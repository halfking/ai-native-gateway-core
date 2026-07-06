package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
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
// 三层隔离：
//   super_admin/admin_key → 跨租户全部（或指定 tenant_id 参数）
//   tenant_admin          → 本租户全部
//   普通用户              → 本租户 + 仅自己名下 owner
func (h *Handler) handleDashboardSessionOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	days := queryInt(r, "days", 7)
	if days < 1 || days > 90 {
		days = 7
	}

	tenantID := effectiveScopeTenant(r)
	if IsSuperAdminOrLegacy(r) {
		if q := queryString(r, "tenant_id"); q != "" {
			tenantID = q
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp := &SessionOverviewResponse{LastUpdated: time.Now()}

	// --- 统一基础片段：FROM session_summaries ss JOIN session_dim sd + WHERE ---
	// 所有子查询共用这个模式，只在 select/group by 上不同
	baseFrom := "FROM session_summaries ss LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key"
	var baseWhere string
	var baseArgs []interface{}

	if tenantID != "" {
		baseWhere = "WHERE ss.tenant_id = $1"
		baseArgs = append(baseArgs, tenantID)
	}

	// 普通用户 owner 过滤（ownerScopeClause 检查角色）
	if IsRegularUser(r) {
		owner := GetAuthContext(r).Username
		paramIdx := len(baseArgs) + 1
		if baseWhere == "" {
			baseWhere = "WHERE sd.owner_user = $" + strconv.Itoa(paramIdx)
		} else {
			baseWhere += " AND sd.owner_user = $" + strconv.Itoa(paramIdx)
		}
		baseArgs = append(baseArgs, owner)
	}

	// 1. 总会话数 + 活跃会话数
	totalArgs := make([]interface{}, len(baseArgs))
	copy(totalArgs, baseArgs)
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours')
		%s %s
	`, baseFrom, baseWhere), totalArgs...).Scan(&resp.TotalSessions, &resp.ActiveSessions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query total failed: %v", err))
		return
	}

	// 2. 查询健康度分布
	healthDist := &resp.HealthDistribution
	healthArgs := make([]interface{}, len(baseArgs))
	copy(healthArgs, baseArgs)
	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE ss.health_grade = 'A') as a,
			COUNT(*) FILTER (WHERE ss.health_grade = 'B') as b,
			COUNT(*) FILTER (WHERE ss.health_grade = 'C') as c,
			COUNT(*) FILTER (WHERE ss.health_grade = 'D') as d,
			COUNT(*) FILTER (WHERE ss.health_grade = 'F') as f
		%s %s
	`, baseFrom, baseWhere), healthArgs...).Scan(&healthDist.A, &healthDist.B, &healthDist.C, &healthDist.D, &healthDist.F)
	if err != nil {
		healthDist.A = 0
		healthDist.B = 0
		healthDist.C = 0
		healthDist.D = 0
		healthDist.F = 0
	}

	// 3. 成本趋势（最近N天）
	trendWhere := baseWhere
	trendArgs := make([]interface{}, len(baseArgs))
	copy(trendArgs, baseArgs)
	trendParamIdx := len(baseArgs) + 1
	if trendWhere == "" {
		trendWhere = fmt.Sprintf("WHERE DATE(ss.first_request_at) >= CURRENT_DATE - INTERVAL '%d days'", days)
		trendArgs = []interface{}{days}
		trendParamIdx = 1
	} else {
		trendWhere += fmt.Sprintf(" AND DATE(ss.first_request_at) >= CURRENT_DATE - $%d::INT", trendParamIdx)
		trendArgs = append(trendArgs, days)
	}

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT DATE(ss.first_request_at) as date,
		       SUM(ss.total_cost_usd) as cost,
		       COUNT(*) as sessions
		%s %s
		GROUP BY DATE(ss.first_request_at)
		ORDER BY date DESC
		LIMIT $%d
	`, baseFrom, trendWhere, trendParamIdx+1), append(trendArgs, days)...)
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
	for i, j := 0, len(resp.CostTrend)-1; i < j; i, j = i+1, j-1 {
		resp.CostTrend[i], resp.CostTrend[j] = resp.CostTrend[j], resp.CostTrend[i]
	}
	if resp.CostTrend == nil {
		resp.CostTrend = []CostTrendPoint{}
	}

	// 4. Top5 客户端（按成本，取 sd.client_id 替代 client_models[1] 启发式）
	clientArgs := make([]interface{}, len(baseArgs))
	copy(clientArgs, baseArgs)
	clientRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(sd.client_id, 'unknown') as client_id,
		       COUNT(*) as session_count,
		       SUM(ss.total_cost_usd) as total_cost,
		       AVG(ss.health_score)::INT as avg_health
		%s %s
		GROUP BY sd.client_id
		ORDER BY total_cost DESC
		LIMIT 5
	`, baseFrom, baseWhere), clientArgs...)
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

	// 5. Top5 任务（按会话数）
	taskArgs := make([]interface{}, len(baseArgs))
	copy(taskArgs, baseArgs)
	taskRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(sd.task_id, 'unknown') as task_id,
		       COUNT(*) as session_count,
		       SUM(ss.total_cost_usd) as total_cost,
		       AVG(ss.health_score)::INT as avg_health
		%s %s
		GROUP BY sd.task_id
		ORDER BY session_count DESC
		LIMIT 5
	`, baseFrom, baseWhere), taskArgs...)
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
