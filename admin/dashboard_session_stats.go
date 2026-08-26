package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	// 防御：如果 session_dim 表不存在（350 迁移未执行），仍返回基础统计而非500错误
	totalArgs := make([]interface{}, len(baseArgs))
	copy(totalArgs, baseArgs)
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours')
		%s %s
	`, baseFrom, baseWhere), totalArgs...).Scan(&resp.TotalSessions, &resp.ActiveSessions)
	if err != nil {
		// session_dim 表不存在时，降级查询 session_summaries
		baseFromFallback := "FROM session_summaries ss"
		err2 := h.db.QueryRow(ctx, fmt.Sprintf(`
			SELECT COUNT(*),
			       COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours')
			%s %s
		`, baseFromFallback, baseWhere), totalArgs...).Scan(&resp.TotalSessions, &resp.ActiveSessions)
		if err2 != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("query total failed: %v", err))
			return
		}
		// 记录警告：session_dim 表缺失，建议执行350迁移
		slog.Warn("session_dim table not found, using fallback query", "original_error", err.Error())
	}

	// 2. 查询健康度分布
	// 防御：health_grade 在 session_summaries 中存在，但如果 session_dim JOIN 失败则降级
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
		// 降级查询：直接从 session_summaries 读取（不依赖 session_dim）
		baseFromFallback := "FROM session_summaries ss"
		err2 := h.db.QueryRow(ctx, fmt.Sprintf(`
			SELECT
				COUNT(*) FILTER (WHERE ss.health_grade = 'A') as a,
				COUNT(*) FILTER (WHERE ss.health_grade = 'B') as b,
				COUNT(*) FILTER (WHERE ss.health_grade = 'C') as c,
				COUNT(*) FILTER (WHERE ss.health_grade = 'D') as d,
				COUNT(*) FILTER (WHERE ss.health_grade = 'F') as f
			%s %s
		`, baseFromFallback, baseWhere), healthArgs...).Scan(&healthDist.A, &healthDist.B, &healthDist.C, &healthDist.D, &healthDist.F)
		if err2 != nil {
			slog.Debug("health distribution query failed", "error", err2.Error())
			healthDist.A = 0
			healthDist.B = 0
			healthDist.C = 0
			healthDist.D = 0
			healthDist.F = 0
		}
	}

	// 3. 成本趋势（最近N天）
	// 防御：如果 session_dim JOIN 失败则降级到 session_summaries
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
	if err != nil {
		// 降级查询：直接从 session_summaries 读取
		slog.Debug("cost trend query with session_dim failed, using fallback", "error", err.Error())
		baseFromFallback := "FROM session_summaries ss"
		rows, err = h.db.Query(ctx, fmt.Sprintf(`
			SELECT DATE(ss.first_request_at) as date,
			       SUM(ss.total_cost_usd) as cost,
			       COUNT(*) as sessions
			%s %s
			GROUP BY DATE(ss.first_request_at)
			ORDER BY date DESC
			LIMIT $%d
		`, baseFromFallback, trendWhere, trendParamIdx+1), append(trendArgs, days)...)
	}
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
	// 防御：如果 session_dim 不存在，从 client_models 数组提取
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
	} else {
		// 降级查询：从 client_models 数组提取（启发式：取第一个元素）
		slog.Debug("session_dim client query failed, using fallback", "error", err.Error())
		clientFallbackRows, err2 := h.db.Query(ctx, fmt.Sprintf(`
			SELECT COALESCE(NULLIF(ss.client_models[1], ''), 'unknown') as client_id,
			       COUNT(*) as session_count,
			       SUM(ss.total_cost_usd) as total_cost,
			       AVG(ss.health_score)::INT as avg_health
			FROM session_summaries ss
			%s
			GROUP BY ss.client_models[1]
			ORDER BY total_cost DESC
			LIMIT 5
		`, baseWhere), clientArgs...)
		if err2 == nil {
			defer clientFallbackRows.Close()
			for clientFallbackRows.Next() {
				var item ClientRankItem
				var avgHealth sql.NullInt64
				if err := clientFallbackRows.Scan(&item.ClientID, &item.SessionCount, &item.TotalCost, &avgHealth); err == nil {
					if avgHealth.Valid {
						val := int(avgHealth.Int64)
						item.AvgHealth = &val
					}
					resp.TopClients = append(resp.TopClients, item)
				}
			}
		}
	}
	if resp.TopClients == nil {
		resp.TopClients = []ClientRankItem{}
	}

	// 5. Top5 任务（按会话数）
	// 防御：如果 session_dim 不存在，返回空数组（task_id 在 session_summaries 中不存在）
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
	} else {
		// task_id 在 session_summaries 中不存在，无法降级，记录警告
		slog.Debug("session_dim task query failed, task ranking unavailable", "error", err.Error())
	}
	if resp.TopTasks == nil {
		resp.TopTasks = []TaskRankItem{}
	}

	writeJSON(w, http.StatusOK, resp)
}
