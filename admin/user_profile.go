package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Response types ────────────────────────────────────────────────────

// UserProfileSummary 用户画像摘要
// userProfileBaseStatsSQL 是用户画像的基础聚合（%s = WHERE 子句）。
//
// 2026-10-20 复核轮：这段聚合原先用裸 SUM/MIN/MAX，于是**该 owner 一行都
// 没有**时返回全 NULL ⇒ Scan 到非空的 Go 标量（int64 / float64 / time.Time）
// 报 "cannot scan NULL into *int64" ⇒ GET
// /api/admin/session-analytics/users/{owner} 恒 500（实测）。
// 「该 owner 没有会话」是**正常状态**，不是错误 ⇒ 聚合值一律 COALESCE 到 0；
// 时间戳用可空指针接收后落成零值（零值时间 = 从未出现，不是 1970 事实）。
//
// 提为包级常量是为了让真库判据能**逐字**跑它
// （见 user_profile_null_aggregate_test.go），而不是让判据自己抄一份。
const userProfileBaseStatsSQL = `
		SELECT
			COUNT(DISTINCT so.gw_session_id) AS session_count,
			COALESCE(SUM(so.request_count), 0) AS total_requests,
			COALESCE(SUM(so.total_cost_usd), 0) AS total_cost_usd,
			COALESCE(CASE WHEN COUNT(DISTINCT so.gw_session_id) > 0
				THEN SUM(so.total_cost_usd) / COUNT(DISTINCT so.gw_session_id)
				ELSE 0 END, 0) AS avg_cost,
			COALESCE(SUM(so.success_count), 0) AS total_success,
			COALESCE(SUM(so.error_count), 0) AS total_errors,
			MIN(so.first_seen_at) AS first_seen_at,
			MAX(so.last_seen_at) AS last_seen_at,
			COUNT(DISTINCT so.end_user_id) FILTER (WHERE so.end_user_id IS NOT NULL AND so.end_user_id <> '') AS end_user_count
		FROM session_owners so
		WHERE %s
	`

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
	DailyCostTrend []DailyCostPoint `json:"daily_cost_trend"`
	// Degraded 恒发（2026-10-20 部署后复验轮）：字段缺失与 false 在 API 语义上
	// **无法区分**（前端读到 undefined 与 false 都会当「服务端确认过是好的」）
	// ⇒ 不能用 omitempty，否则这个标记自己废掉一半。
	//
	// 什么时候会 true：schema 落后于代码（生产共享库缺 session_owners）⇒
	// 降级成 200 + 空载荷，而不是整页 500。口径与同文件的列表端点一致。
	Degraded       bool                `json:"degraded"`
	DegradedReason string              `json:"degraded_reason,omitempty"`
	TopTasks       []TaskRankItem      `json:"top_tasks"`
	TopEndUsers    []EndUserRankItem   `json:"top_end_users"`
	RecentSessions []RecentSessionItem `json:"recent_sessions"`
	HealthDist     HealthDistribution  `json:"health_distribution"`
}

// EndUserRankItem 终端用户排行项
type EndUserRankItem struct {
	EndUserID    string    `json:"end_user_id"`
	SessionCount int       `json:"session_count"`
	TotalCost    float64   `json:"total_cost_usd"`
	AvgHealth    *int      `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// handleUserAnalyticsList GET /api/admin/session-analytics/users
// 返回当前租户（或调用者可见范围）内的 owner_user 列表。
// 三层隔离：
//
//	super_admin/admin_key → 全部 owner（可指定 tenant_id 参数）
//	tenant_admin          → 本租户全部 owner
//	普通用户              → 仅自己（返回单条）
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

	// owner_user 为 NULL 的行不构成「用户画像」：它扫进 string 会 500
	// （pgx 不做隐式 NULL→""），且 COUNT(DISTINCT owner_user) 本就忽略
	// NULL ⇒ 不过滤会让 total 与列表口径分叉。这里在两处查询统一排掉。
	// 2026-10-14 全量 API 扫描轮实测抓到（真库 session_owners 含 NULL 属主行）。
	nullOwnerFilter := "so.owner_user IS NOT NULL"

	// 三层隔离
	if IsRegularUser(r) {
		// 普通用户只看到自己
		owner := GetAuthContext(r).Username
		if owner == "" {
			writeError(w, http.StatusForbidden, "no user identity")
			return
		}
		baseWhere = "WHERE " + nullOwnerFilter + " AND so.owner_user = $1"
		baseArgs = append(baseArgs, owner)
	} else if IsTenantAdmin(r) {
		tenantID := GetTenantID(r)
		if tenantID != "" && tenantID != "default" {
			baseWhere = "WHERE " + nullOwnerFilter + " AND so.tenant_id = $1"
			baseArgs = append(baseArgs, tenantID)
		} else {
			baseWhere = "WHERE " + nullOwnerFilter
		}
	} else {
		// super_admin/admin_key — 可指定 tenant_id
		if q := queryString(r, "tenant_id"); q != "" {
			baseWhere = "WHERE " + nullOwnerFilter + " AND so.tenant_id = $1"
			baseArgs = append(baseArgs, q)
		} else {
			baseWhere = "WHERE " + nullOwnerFilter
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
		// 2026-10-03：session_owners 属于可选聚合表（安装器迁移 563/683 建），
		// 目标库没应用时这里原本直接 500，页面 /admin/session-analytics/users
		// 每次打开都报 console 错误。与 usage_* 的降级口径保持一致：
		// schema 落后于代码时返回空列表 + 200，并把原因记进服务端日志。
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userProfile.list", err)
			// degraded 恒发：空列表在页面上与「真的没有用户」同形，
			// 而 2026-10-03 的 /api/logs 扫描里这条正是「13 条动态路由
			// 因取不到 ID 而未纳入」的三条之一。至少让调用方知道
			// 「这是降级，不是没有数据」。
			writeJSON(w, http.StatusOK, map[string]any{
				"users":           []UserProfileSummary{},
				"total":           0,
				"limit":           limit,
				"offset":          offset,
				"degraded":        true,
				"degraded_reason": reason,
			})
			return
		}
		writeInternalErr(w, "query failed", err)
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
			warnRowSkip("userProfile.list", err)
			continue
		}
		if avgCost.Valid {
			u.AvgCostPerSession = avgCost.Float64
		}
		users = append(users, u)
	}
	// 用户画像榜少一截 = 按成本排序的用户名单少了几个人，total 与列表
	// 对不上（前端翻页会以为到底了）。
	if writeAggRowsErr(w, "userProfile.list", rows.Err()) {
		return
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
// 
// 路由：如果路径以 /models 结尾，则转发到 handleUserModelUsage
func (h *Handler) handleUserAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	// 从路径提取 owner_user，并检查是否是 /models 子路由
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/users/")
	
	// 检查是否是 /models 端点
	if strings.HasSuffix(path, "/models") {
		h.handleUserModelUsage(w, r)
		return
	}
	
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

	// 2026-10-20 复核轮：这个聚合在**该 owner 一行都没有**时返回全 NULL 的
	// SUM / MIN / MAX ⇒ Scan 到非空的 Go 标量（int64 / float64 / time.Time）
	// 报 "cannot scan NULL into *int64" ⇒ 端点 500。
	// 「该 owner 没有会话」是**正常状态**，不是错误：聚合值一律 COALESCE 到 0，
	// 时间戳用可空指针接收后落成零值（零值时间 = 从未出现，不是 1970 事实）。
	var firstSeenAt, lastSeenAt *time.Time
	err := h.db.QueryRow(ctx, fmt.Sprintf(userProfileBaseStatsSQL, baseWhere), baseArgs...).Scan(
		&resp.SessionCount, &resp.TotalRequests, &resp.TotalCost,
		&resp.AvgCostPerSession,
		&resp.TotalSuccess, &resp.TotalErrors,
		&firstSeenAt, &lastSeenAt, &resp.EndUserCount,
	)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "owner not found")
		return
	}
	if err != nil {
		// 2026-10-20 部署后复验轮（245/154 生产）：详情端点恒 500，根因是
		// **生产共享库缺 session_owners**（迁移 358 的版本号低于部署通道的
		// 账本对账下限 412 ⇒ 存量库永远拿不到它）⇒ 基础聚合直接 42P01。
		//
		// 口径与同文件的**列表端点**一致（见上方 handleUserAnalyticsList）：
		// schema 落后于代码 ⇒ 降级成 200 + 空载荷 + 自报家门的 degraded 标记，
		// 而不是整页 500。理由是「没有会话」与「聚合表没迁移」在页面上都长成
		// 一个空列表 ⇒ 不自报家门就是撒谎。
		//
		// ⚠ 降级载荷用**同一个结构体**构造 ⇒ 正常路径与降级路径的 JSON 形状
		// 由编译器保证一致（不会出现「降级时少几个字段」的前端崩溃）。
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userProfile.detail", err)
			writeJSON(w, http.StatusOK, &UserProfileDetailResponse{
				UserProfileSummary: UserProfileSummary{
					OwnerUser:   ownerUser,
					ModelsUsed:  []string{},
					FirstSeenAt: time.Time{},
					LastSeenAt:  time.Time{},
				},
				DailyCostTrend: []DailyCostPoint{},
				TopTasks:       []TaskRankItem{},
				TopEndUsers:    []EndUserRankItem{},
				RecentSessions: []RecentSessionItem{},
				HealthDist:     HealthDistribution{},
				// ★ 与同文件的列表端点（:208 `"degraded": true`）**同口径**：
				// 降级路径必须**自报家门** —— 只写 reason 不写标志 ⇒ 前端按
				// `degraded:false` 当正常数据处理 ⇒ 比不报更坏（谎报降级状态）。
				Degraded:       true,
				DegradedReason: reason,
			})
			return
		}
		writeInternalErr(w, "stats query", err)
		return
	}
	if firstSeenAt != nil {
		resp.FirstSeenAt = *firstSeenAt
	}
	if lastSeenAt != nil {
		resp.LastSeenAt = *lastSeenAt
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
			} else {
				warnRowSkip("userProfile.detail.costTrend", err)
			}
		}
		// 成本趋势是详情页的补充区块（查询失败本身已是 non-fatal），
		// 迭代中断同样只降级不 500，但必须留痕。
		if err := costRows.Err(); err != nil {
			slog.Warn("user profile detail: cost trend rows iteration aborted; section truncated", "owner", ownerUser, "error", err)
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
			var lastActivity time.Time
			if err := taskRows.Scan(&item.TaskID, &item.SessionCount, &item.TotalCost, &avgHealth, &lastActivity); err == nil {
				if avgHealth.Valid {
					val := int(avgHealth.Int64)
					item.AvgHealth = &val
				}
				resp.TopTasks = append(resp.TopTasks, item)
			} else {
				warnRowSkip("userProfile.detail.topTasks", err)
			}
		}
		if err := taskRows.Err(); err != nil {
			slog.Warn("user profile detail: top tasks rows iteration aborted; section truncated", "owner", ownerUser, "error", err)
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
			} else {
				warnRowSkip("userProfile.detail.topEndUsers", err)
			}
		}
		if err := euRows.Err(); err != nil {
			slog.Warn("user profile detail: top end users rows iteration aborted; section truncated", "owner", ownerUser, "error", err)
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
			} else {
				warnRowSkip("userProfile.detail.recentSessions", err)
			}
		}
		if err := recentRows.Err(); err != nil {
			slog.Warn("user profile detail: recent sessions rows iteration aborted; section truncated", "owner", ownerUser, "error", err)
		}
	}
	if resp.RecentSessions == nil {
		resp.RecentSessions = []RecentSessionItem{}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── User Model Usage Statistics ───────────────────────────────────────

// UserModelUsageItem 用户模型使用统计项
type UserModelUsageItem struct {
	ModelName       string    `json:"model_name"`
	Provider        string    `json:"provider"`
	RequestCount    int64     `json:"request_count"`
	TotalTokens     int64     `json:"total_tokens"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	InternalCostUSD float64   `json:"internal_cost_usd"`
	ExternalCostUSD float64   `json:"external_cost_usd"`
	FirstUsedAt     time.Time `json:"first_used_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
}

// handleUserModelUsage GET /api/admin/session-analytics/users/:owner/models
// 查询用户在指定时间范围内的模型使用统计。
// 权限：继承 handleUserAnalyticsDetail 的三层隔离机制。
func (h *Handler) handleUserModelUsage(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "models" {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	ownerUser := strings.TrimSpace(parts[0])
	if ownerUser == "" {
		writeError(w, http.StatusBadRequest, "owner_user is required")
		return
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

	// 时间范围参数
	startDate := queryString(r, "start_date")
	endDate := queryString(r, "end_date")

	// 默认最近 30 天
	var startTime, endTime time.Time
	if startDate == "" && endDate == "" {
		endTime = time.Now()
		startTime = endTime.AddDate(0, 0, -30)
	} else {
		var err error
		if startDate != "" {
			startTime, err = time.Parse(time.RFC3339, startDate)
			if err != nil {
				// 尝试 YYYY-MM-DD 格式
				startTime, err = time.Parse("2006-01-02", startDate)
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid start_date format, use ISO 8601 or YYYY-MM-DD")
					return
				}
			}
		} else {
			startTime = time.Now().AddDate(0, 0, -90) // 最多 90 天
		}

		if endDate != "" {
			endTime, err = time.Parse(time.RFC3339, endDate)
			if err != nil {
				endTime, err = time.Parse("2006-01-02", endDate)
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid end_date format, use ISO 8601 or YYYY-MM-DD")
					return
				}
				// 对于 YYYY-MM-DD 格式，设置为当天结束
				endTime = endTime.Add(24 * time.Hour)
			}
		} else {
			endTime = time.Now()
		}
	}

	// 时间范围限制：最多 90 天
	if endTime.Sub(startTime) > 90*24*time.Hour {
		writeError(w, http.StatusBadRequest, "time range cannot exceed 90 days")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	// 构建查询
	whereClause := "rl.owner_user = $1 AND rl.created_at >= $2 AND rl.created_at < $3"
	args := []interface{}{ownerUser, startTime, endTime}
	argIdx := 4

	if tenantID != "" {
		whereClause += fmt.Sprintf(" AND rl.tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	// 查询模型使用统计
	// 注意：cost_display 是内部定价，cost_usd 是外部成本
	// prompt_tokens + completion_tokens = total_tokens
	sql := fmt.Sprintf(`
		SELECT 
			COALESCE(rl.outbound_model, rl.client_model, 'unknown') as model_name,
			COALESCE(p.display_name, rl.provider_id::text, 'unknown') as provider,
			COUNT(*) as request_count,
			COALESCE(SUM(rl.prompt_tokens), 0) as input_tokens,
			COALESCE(SUM(rl.completion_tokens), 0) as output_tokens,
			COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) as total_tokens,
			COALESCE(SUM(rl.cost_display), SUM(rl.cost_usd), 0)::float8 as internal_cost_usd,
			COALESCE(SUM(rl.cost_usd), 0)::float8 as external_cost_usd,
			MIN(rl.created_at) as first_used_at,
			MAX(rl.created_at) as last_used_at
		FROM request_logs rl
		LEFT JOIN providers p ON p.id = rl.provider_id
		WHERE %s
		GROUP BY model_name, provider
		ORDER BY external_cost_usd DESC
		LIMIT 100
	`, whereClause)

	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		// 降级处理：request_logs 可能不存在或查询失败
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userModelUsage", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"models":          []UserModelUsageItem{},
				"degraded":        true,
				"degraded_reason": reason,
			})
			return
		}
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	models := []UserModelUsageItem{}
	for rows.Next() {
		var item UserModelUsageItem
		if err := rows.Scan(
			&item.ModelName,
			&item.Provider,
			&item.RequestCount,
			&item.InputTokens,
			&item.OutputTokens,
			&item.TotalTokens,
			&item.InternalCostUSD,
			&item.ExternalCostUSD,
			&item.FirstUsedAt,
			&item.LastUsedAt,
		); err != nil {
			warnRowSkip("userModelUsage", err)
			continue
		}
		models = append(models, item)
	}

	if writeAggRowsErr(w, "userModelUsage", rows.Err()) {
		return
	}

	if models == nil {
		models = []UserModelUsageItem{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models":     models,
		"start_date": startTime.Format(time.RFC3339),
		"end_date":   endTime.Format(time.RFC3339),
	})
}
