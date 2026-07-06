package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Response types ────────────────────────────────────────────────────

// UserProfileSummary 用户画像摘要
type UserProfileSummary struct {
	OwnerUser         string    `json:"owner_user"`
	SessionCount      int       `json:"session_count"`
	TotalRequests     int64     `json:"total_requests"`
	TotalCost         float64   `json:"total_cost_usd"`
	AvgCostPerSession float64   `json:"avg_cost_per_session"`
	AvgHealthScore    *int      `json:"avg_health_score,omitempty"`
	TotalSuccess      int64     `json:"total_success"`
	TotalErrors       int64     `json:"total_errors"`
	FirstSeenAt       time.Time `json:"first_seen_at"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	EndUserCount      int       `json:"end_user_count"`
	ModelsUsed        []string  `json:"models_used"`
}

// UserProfileDetailResponse 用户画像详情
type UserProfileDetailResponse struct {
	UserProfileSummary
	DailyCostTrend    []DailyCostPoint       `json:"daily_cost_trend"`
	TopTasks          []TaskRankItem         `json:"top_tasks"`
	TopEndUsers       []EndUserRankItem      `json:"top_end_users"`
	RecentSessions    []RecentSessionItem    `json:"recent_sessions"`
	HealthDist        HealthDistribution     `json:"health_distribution"`
}

// EndUserRankItem 终端用户排行项
type EndUserRankItem struct {
	EndUserID    string  `json:"end_user_id"`
	SessionCount int     `json:"session_count"`
	TotalCost    float64 `json:"total_cost_usd"`
	AvgHealth    *int    `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// handleUserAnalyticsList GET /api/admin/session-analytics/users
// 返回当前租户（或调用者可见范围）内的 owner_user 列表。
// 三层隔离：
//   super_admin/admin_key → 全部 owner（可指定 tenant_id 参数）
//   tenant_admin          → 本租户全部 owner
//   普通用户              → 仅自己（返回单条）
func (h *Handler) handleUserAnalyticsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	limit := queryInt(r, "limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := queryInt(r, "offset", 0)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	baseWhere := ""
	baseArgs := []interface{}{}

	// 三层隔离
	if IsRegularUser(r) {
		// 普通用户只看到自己
		owner := GetAuthContext(r).Username
		if owner == "" {
			writeError(w, http.StatusForbidden, "no user identity")
			return
		}
		baseWhere = "WHERE so.owner_user = $1"
		baseArgs = append(baseArgs, owner)
	} else if IsTenantAdmin(r) {
		tenantID := GetTenantID(r)
		if tenantID != "" && tenantID != "default" {
			baseWhere = "WHERE so.tenant_id = $1"
			baseArgs = append(baseArgs, tenantID)
		}
	} else {
		// super_admin/admin_key — 可指定 tenant_id
		if q := queryString(r, "tenant_id"); q != "" {
			baseWhere = "WHERE so.tenant_id = $1"
			baseArgs = append(baseArgs, q)
		}
	}

	// 搜索（按 owner_user 模糊匹配）
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		paramIdx := len(baseArgs) + 1
		if baseWhere == "" {
			baseWhere = fmt.Sprintf("WHERE so.owner_user ILIKE $%d", paramIdx)
		} else {
			baseWhere += fmt.Sprintf(" AND so.owner_user ILIKE $%d", paramIdx)
		}
		baseArgs = append(baseArgs, "%"+search+"%")
	}

	// 总数
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(DISTINCT owner_user) FROM session_owners so %s", baseWhere)
	_ = h.db.QueryRow(ctx, countSQL, baseArgs...).Scan(&total)

	// 列表：从 session_owners 聚合每个 owner_user
	listSQL := fmt.Sprintf(`
		SELECT so.owner_user,
			COUNT(DISTINCT so.gw_session_id) AS session_count,
			SUM(so.request_count) AS total_requests,
			SUM(so.total_cost_usd) AS total_cost_usd,
			CASE WHEN COUNT(DISTINCT so.gw_session_id) > 0
				THEN SUM(so.total_cost_usd) / COUNT(DISTINCT so.gw_session_id)
				ELSE 0 END AS avg_cost_per_session,
			COUNT(DISTINCT so.end_user_id) FILTER (WHERE so.end_user_id IS NOT NULL AND so.end_user_id <> '') AS end_user_count,
			MIN(so.first_seen_at) AS first_seen_at,
			MAX(so.last_seen_at) AS last_seen_at
		FROM session_owners so
		%s
		GROUP BY so.owner_user
		ORDER BY SUM(so.total_cost_usd) DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, len(baseArgs)+1, len(baseArgs)+2)
	args := make([]interface{}, len(baseArgs))
	copy(args, baseArgs)
	args = append(args, limit, offset)

	rows, err := h.db.Query(ctx, listSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	users := []UserProfileSummary{}
	for rows.Next() {
		var u UserProfileSummary
		var avgCost sql.NullFloat64
		if err := rows.Scan(
			&u.OwnerUser, &u.SessionCount, &u.TotalRequests, &u.TotalCost,
			&avgCost, &u.EndUserCount, &u.FirstSeenAt, &u.LastSeenAt,
		); err != nil {
			continue
		}
		if avgCost.Valid {
			u.AvgCostPerSession = avgCost.Float64
		}
		users = append(users, u)
	}
	if users == nil {
		users = []UserProfileSummary{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users":  users,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleUserAnalyticsDetail GET /api/admin/session-analytics/users/:owner
// 用户画像详情。
// 三层隔离：super_admin/tenant_admin 可查任意本租户 owner；普通用户强制指向自己。
func (h *Handler) handleUserAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	// 从路径提取 owner_user
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/users/")
	ownerUser := strings.TrimSpace(path)
	if ownerUser == "" {
		writeError(w, http.StatusBadRequest, "owner_user is required")
		return
	}

	days := queryInt(r, "days", 30)
	if days < 1 || days > 90 {
		days = 30
	}

	// 普通用户只允许看自己
	if IsRegularUser(r) {
		myOwner := GetAuthContext(r).Username
		if myOwner == "" || ownerUser != myOwner {
			writeError(w, http.StatusForbidden, "you can only view your own profile")
			return
		}
	}

	// 租户范围
	tenantID := ""
	if IsTenantAdmin(r) || IsRegularUser(r) {
		tenantID = GetTenantID(r)
	}
	if IsSuperAdminOrLegacy(r) {
		if q := queryString(r, "tenant_id"); q != "" {
			tenantID = q
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	resp := &UserProfileDetailResponse{}

	// 1. 基础统计（从 session_owners 聚合 + session_summaries 外部 join）
	baseWhere := "so.owner_user = $1"
	baseArgs := []interface{}{ownerUser}
	if tenantID != "" && tenantID != "default" {
		baseWhere += " AND so.tenant_id = $2"
		baseArgs = append(baseArgs, tenantID)
	}

	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			COUNT(DISTINCT so.gw_session_id) AS session_count,
			SUM(so.request_count) AS total_requests,
			SUM(so.total_cost_usd) AS total_cost_usd,
			CASE WHEN COUNT(DISTINCT so.gw_session_id) > 0
				THEN SUM(so.total_cost_usd) / COUNT(DISTINCT so.gw_session_id)
				ELSE 0 END AS avg_cost,
			SUM(so.success_count) AS total_success,
			SUM(so.error_count) AS total_errors,
			MIN(so.first_seen_at) AS first_seen_at,
			MAX(so.last_seen_at) AS last_seen_at,
			COUNT(DISTINCT so.end_user_id) FILTER (WHERE so.end_user_id IS NOT NULL AND so.end_user_id <> '') AS end_user_count
		FROM session_owners so
		WHERE %s
	`, baseWhere), baseArgs...).Scan(
		&resp.SessionCount, &resp.TotalRequests, &resp.TotalCost,
		&resp.AvgCostPerSession,
		&resp.TotalSuccess, &resp.TotalErrors,
		&resp.FirstSeenAt, &resp.LastSeenAt, &resp.EndUserCount,
	)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "owner not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats query: "+err.Error())
		return
	}

	// 额外：models_used（通过 session_dim 关联 session_summaries）
	modelsWhere := "sd.owner_user = $1"
	modelsArgs := []interface{}{ownerUser}
	if tenantID != "" && tenantID != "default" {
		modelsWhere += " AND sd.tenant_id = $2"
		modelsArgs = append(modelsArgs, tenantID)
	}
	var modelsUsed []string
	_ = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT array_agg(DISTINCT m)
		FROM session_summaries ss
		INNER JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		CROSS JOIN LATERAL unnest(ss.models_used) AS m
		WHERE %s
	`, modelsWhere), modelsArgs...).Scan(&modelsUsed)
	resp.ModelsUsed = modelsUsed
	if resp.ModelsUsed == nil {
		resp.ModelsUsed = []string{}
	}

	// 2. 健康度分布（通过 session_dim 关联）
	healthWhere := baseWhere
	healthArgs := make([]interface{}, len(baseArgs))
	copy(healthArgs, baseArgs)
	healthDist := HealthDistribution{}
	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE ss.health_grade = 'A') AS a,
			COUNT(*) FILTER (WHERE ss.health_grade = 'B') AS b,
			COUNT(*) FILTER (WHERE ss.health_grade = 'C') AS c,
			COUNT(*) FILTER (WHERE ss.health_grade = 'D') AS d,
			COUNT(*) FILTER (WHERE ss.health_grade = 'F') AS f
		FROM session_owners so
		INNER JOIN session_summaries ss ON ss.session_key = so.gw_session_id
		WHERE %s
	`, healthWhere), healthArgs...).Scan(
		&healthDist.A, &healthDist.B, &healthDist.C, &healthDist.D, &healthDist.F,
	)
	if err == nil {
		resp.HealthDist = healthDist
	}
	// 6 sessions... Get health out of it
	if resp.SessionCount > 0 {
		avgScore := 0
		_ = h.db.QueryRow(ctx, fmt.Sprintf(`
			SELECT AVG(ss.health_score)::INT
			FROM session_owners so
			INNER JOIN session_summaries ss ON ss.session_key = so.gw_session_id
			WHERE %s AND ss.health_score IS NOT NULL
		`, healthWhere), healthArgs...).Scan(&avgScore)
		if avgScore > 0 {
			resp.AvgHealthScore = &avgScore
		}
	}

	// 3. 成本趋势
	trendWhere := baseWhere
	trendArgs := make([]interface{}, len(baseArgs))
	copy(trendArgs, baseArgs)
	trendParamIdx := len(baseArgs) + 1
	trendWhere += fmt.Sprintf(" AND ss.first_request_at >= CURRENT_DATE - $%d::INT", trendParamIdx)
	trendArgs = append(trendArgs, days)

	costRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT DATE(ss.first_request_at) AS date,
			SUM(ss.total_cost_usd) AS cost,
			COUNT(*) AS sessions
		FROM session_owners so
		INNER JOIN session_summaries ss ON ss.session_key = so.gw_session_id
		WHERE %s
		GROUP BY DATE(ss.first_request_at)
		ORDER BY date DESC
		LIMIT %d
	`, trendWhere, days+1), trendArgs...)
	if err == nil {
		defer costRows.Close()
		for costRows.Next() {
			var point DailyCostPoint
			var date time.Time
			if err := costRows.Scan(&date, &point.Cost, &point.Sessions); err == nil {
				point.Date = date.Format("2006-01-02")
				resp.DailyCostTrend = append(resp.DailyCostTrend, point)
			}
		}
	}
	// 反转
	for i, j := 0, len(resp.DailyCostTrend)-1; i < j; i, j = i+1, j-1 {
		resp.DailyCostTrend[i], resp.DailyCostTrend[j] = resp.DailyCostTrend[j], resp.DailyCostTrend[i]
	}
	if resp.DailyCostTrend == nil {
		resp.DailyCostTrend = []DailyCostPoint{}
	}

	// 4. Top tasks（按成本）
	taskWhere := baseWhere
	taskArgs := make([]interface{}, len(baseArgs))
	copy(taskArgs, baseArgs)
	taskRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(sd.task_id, 'unknown') AS task_id,
			COUNT(*) AS session_count,
			SUM(ss.total_cost_usd) AS total_cost_usd,
			AVG(ss.health_score)::INT AS avg_health,
			MAX(ss.last_request_at) AS last_activity
		FROM session_owners so
		INNER JOIN session_summaries ss ON ss.session_key = so.gw_session_id
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		WHERE %s
		GROUP BY sd.task_id
		ORDER BY total_cost_usd DESC
		LIMIT 10
	`, taskWhere), taskArgs...)
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

	// 5. Top end_users
	endUserWhere := "so.owner_user = $1"
	endUserArgs := []interface{}{ownerUser}
	if tenantID != "" && tenantID != "default" {
		endUserWhere += " AND so.tenant_id = $2"
		endUserArgs = append(endUserArgs, tenantID)
	}
	euRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(so.end_user_id, 'unknown') AS end_user_id,
			COUNT(DISTINCT so.gw_session_id) AS session_count,
			SUM(so.total_cost_usd) AS total_cost_usd,
			MAX(so.last_seen_at) AS last_activity
		FROM session_owners so
		WHERE %s AND so.end_user_id IS NOT NULL AND so.end_user_id <> ''
		GROUP BY so.end_user_id
		ORDER BY total_cost_usd DESC
		LIMIT 10
	`, endUserWhere), endUserArgs...)
	if err == nil {
		defer euRows.Close()
		for euRows.Next() {
			var item EndUserRankItem
			if err := euRows.Scan(&item.EndUserID, &item.SessionCount, &item.TotalCost, &item.LastActivity); err == nil {
				resp.TopEndUsers = append(resp.TopEndUsers, item)
			}
		}
	}
	if resp.TopEndUsers == nil {
		resp.TopEndUsers = []EndUserRankItem{}
	}

	// 6. Recent sessions
	recentWhere := baseWhere
	recentArgs := make([]interface{}, len(baseArgs))
	copy(recentArgs, baseArgs)
	recentRows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT so.gw_session_id, so.request_count, so.total_cost_usd,
			COALESCE(ss.health_score, 0), ss.health_grade, so.last_seen_at
		FROM session_owners so
		LEFT JOIN session_summaries ss ON ss.session_key = so.gw_session_id
		WHERE %s
		ORDER BY so.last_seen_at DESC
		LIMIT 20
	`, recentWhere), recentArgs...)
	if err == nil {
		defer recentRows.Close()
		for recentRows.Next() {
			var item RecentSessionItem
			var healthScore int
			var healthGrade *string
			if err := recentRows.Scan(&item.SessionID, &item.RequestCount, &item.Cost,
				&healthScore, &healthGrade, &item.CreatedAt); err == nil {
				if healthScore > 0 {
					item.HealthScore = &healthScore
				}
				item.HealthGrade = healthGrade
				resp.RecentSessions = append(resp.RecentSessions, item)
			}
		}
	}
	if resp.RecentSessions == nil {
		resp.RecentSessions = []RecentSessionItem{}
	}

	writeJSON(w, http.StatusOK, resp)
}
