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

	"github.com/kaixuan/llm-gateway-go/settings"
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
//
// 2026-11-20 会话分析闭环审计轮：锚表从 session_owners 换到
// user_session_aggregates（迁移 981 的会话级人员 MV，994 起 owner 键 =
// COALESCE(s.owner_user, sd.api_key_owner_user) 双源）。根因：session_owners
// 由 v1 _request_logs 触发器维护（358/563），v1 停写后行集冻结在 2026-10
// ——它是陈旧子集，活跃 owner 整行缺失、窗口用量被静默丢弃（projects
// 下钻 3745f26f9 已趟平同款换锚）。换锚后数字与 overview/user-ranking
// 端点同源同键（981 族 MV），两种页面不再各说各话。
// first_seen_at 语义订正：MV 按日桶聚合，此处为**日粒度**（桶内无首请求时刻）。
const userProfileBaseStatsSQL = `
		SELECT
			COALESCE(SUM(a.session_count), 0)::bigint AS session_count,
			COALESCE(SUM(a.request_count), 0)::bigint AS total_requests,
			COALESCE(SUM(a.internal_cost + a.external_cost), 0) AS total_cost_usd,
			COALESCE(CASE WHEN COALESCE(SUM(a.session_count), 0) > 0
				THEN SUM(a.internal_cost + a.external_cost) / SUM(a.session_count)
				ELSE 0 END, 0) AS avg_cost,
			COALESCE(SUM(a.success_count), 0)::bigint AS total_success,
			COALESCE(SUM(a.error_count), 0)::bigint AS total_errors,
			MIN(a.date)::timestamptz AS first_seen_at,
			MAX(a.last_active_at) AS last_seen_at,
			COALESCE(SUM(a.end_user_count), 0)::bigint AS end_user_count
		FROM user_session_aggregates a
		WHERE %s
	`

type UserProfileSummary struct {
	OwnerUser     string `json:"owner_user"`
	SessionCount  int    `json:"session_count"`
	TotalRequests int64  `json:"total_requests"`
	// TotalCost 混合单位：内部美元成本 + 外部积分的**跨单位相加**
	//（不是美元金额）。字段名 total_cost_usd 不能改（前端依赖）⇒ 口径只能
	// 靠**披露**纠偏，同时另给 internal_cost_usd / external_credits 两个
	// 分列数字（2026-11-24 修订轮：此前只 SELECT 了混合值，前端拿不到任何
	// 分列数字）。
	TotalCost float64 `json:"total_cost_usd"`
	// InternalCostUSD 内部成本（美元），与 ExternalCredits **分列**。
	InternalCostUSD float64 `json:"internal_cost_usd"`
	// ExternalCredits 外部成本（积分），与 InternalCostUSD **分列**。
	ExternalCredits int64 `json:"external_credits"`
	// AvgCostPerSession 混合单位：每会话均价同样是跨单位相加
	//（口径与 TotalCost 一致，字段名同样不能改）。
	AvgCostPerSession float64 `json:"avg_cost_per_session"`
	// AvgInternalCostUSDPerSession 每会话内部成本均价（美元）—— 分列对应。
	AvgInternalCostUSDPerSession float64 `json:"avg_internal_cost_usd_per_session"`
	// AvgExternalCreditsPerSession 每会话外部成本均价（积分）—— 分列对应。
	AvgExternalCreditsPerSession float64   `json:"avg_external_credits_per_session"`
	AvgHealthScore               *int      `json:"avg_health_score,omitempty"`
	TotalSuccess                 int64     `json:"total_success"`
	TotalErrors                  int64     `json:"total_errors"`
	FirstSeenAt                  time.Time `json:"first_seen_at"`
	LastSeenAt                   time.Time `json:"last_seen_at"`
	EndUserCount                 int       `json:"end_user_count"`
	ModelsUsed                   []string  `json:"models_used"`
	// WindowUsage 员工在选定时间窗口内的用量聚合（2026-11-20 数字出处轮）。
	// 出处 = request_logs_with_current_month（会话族 canonical 视图），与
	// /users/{owner}/models 端点同源同表达式 ⇒ 列表页数字与详情页模型卡
	// 天然不打架。窗口未选时后端默认近 30 天，真实窗口回传在
	// usage_window 字段 —— 前端不得自行脑补窗口。
	// nil = 窗口聚合不可用（canonical 视图缺席降级），与 false 语义分家。
	WindowUsage *UserWindowUsage `json:"window_usage,omitempty"`
}

// UserWindowUsage 单个员工在 [start, end) 窗口内的用量聚合。
// 成本口径与 handleUserModelUsage 逐字一致：
//
//	internal_cost_usd = SUM(cost_display)（内部核算价；NULL 回退 cost_usd）
//	external_cost_usd = SUM(cost_usd)（外部上游成本，USD）
//	total_tokens      = SUM(prompt_tokens + completion_tokens)
type UserWindowUsage struct {
	RequestCount    int64   `json:"request_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	InternalCostUSD float64 `json:"internal_cost_usd"`
	ExternalCostUSD float64 `json:"external_cost_usd"`
	SessionCount    int64   `json:"session_count"`
	ModelCount      int64   `json:"model_count"`
}

// userUsageMeta 响应内的数字出处块：前端帮助抽屉与列头提示直接引用，
// 不在页面里硬编码口径文案 ⇒ 口径改动时后端是唯一真源。
type userUsageMeta struct {
	SourceView  string `json:"source_view"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
}

// usageWindowBounds 解析列表端点的用量窗口：date_from/date_to（RFC3339 或
// YYYY-MM-DD），与 date_to 的「当日结束」语义与 handleUserModelUsage 一致；
// 两者都缺省时回退近 30 天；跨度上限 90 天（与 models 端点同闸）。
// 返回 (start, end, ok)；ok=false = 调用方传了非法格式（已写 400）。
func usageWindowBounds(w http.ResponseWriter, r *http.Request, dateFrom, dateTo string) (time.Time, time.Time, bool) {
	var startTime, endTime time.Time
	var err error
	if dateFrom == "" && dateTo == "" {
		endTime = time.Now()
		startTime = endTime.AddDate(0, 0, -30)
		return startTime, endTime, true
	}
	if dateFrom != "" {
		startTime, err = time.Parse(time.RFC3339, dateFrom)
		if err != nil {
			// 时区审计轮（2026-11-21）F4：YYYY-MM-DD 按系统时区零点解释
			//（RFC3339 带偏移的不受影响，仍按串内偏移）。
			startTime, err = settings.ParseSystemDate(dateFrom)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid date_from format, use ISO 8601 or YYYY-MM-DD")
				return time.Time{}, time.Time{}, false
			}
		}
	}
	if dateTo != "" {
		endTime, err = time.Parse(time.RFC3339, dateTo)
		if err != nil {
			endTime, err = settings.ParseSystemDate(dateTo)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid date_to format, use ISO 8601 or YYYY-MM-DD")
				return time.Time{}, time.Time{}, false
			}
			// YYYY-MM-DD 语义 = 当天结束（含当日全天），与 models 端点一致。
			endTime = endTime.Add(24 * time.Hour)
		}
	} else {
		endTime = time.Now()
	}
	if !startTime.IsZero() && endTime.Sub(startTime) > 90*24*time.Hour {
		writeError(w, http.StatusBadRequest, "time range cannot exceed 90 days")
		return time.Time{}, time.Time{}, false
	}
	return startTime, endTime, true
}

// usageAggregatesByOwner 按员工聚合窗口内用量（数字出处 =
// request_logs_with_current_month）。表达式与 handleUserModelUsage 逐字一致
// ⇒ 同一员工列表页数字 == 详情页模型卡合计，不需要第二条口径对表。
// owner 键 = COALESCE(owner_user, api_key_owner_user)（252 生产实测 owner_user
// 当前流量全 NULL；与生产演进版 981 视图同键）。
func usageAggregatesByOwner(ctx context.Context, db usageQuerier, startTime, endTime time.Time, tenantID, ownerFilter string) (map[string]*UserWindowUsage, error) {
	where := "COALESCE(rl.owner_user, rl.api_key_owner_user) IS NOT NULL AND COALESCE(rl.owner_user, rl.api_key_owner_user) <> '' AND rl.ts >= $1 AND rl.ts < $2"
	args := []interface{}{startTime, endTime}
	if tenantID != "" {
		where += fmt.Sprintf(" AND rl.tenant_id = $%d", len(args)+1)
		args = append(args, tenantID)
	}
	if ownerFilter != "" {
		where += fmt.Sprintf(" AND COALESCE(rl.owner_user, rl.api_key_owner_user) = $%d", len(args)+1)
		args = append(args, ownerFilter)
	}
	querySQL := fmt.Sprintf(`
		SELECT COALESCE(rl.owner_user, rl.api_key_owner_user) AS owner_user,
			COUNT(*) AS request_count,
			COALESCE(SUM(rl.prompt_tokens), 0) AS input_tokens,
			COALESCE(SUM(rl.completion_tokens), 0) AS output_tokens,
			COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) AS total_tokens,
			COALESCE(SUM(rl.cost_display), SUM(rl.cost_usd), 0) AS internal_cost_usd,
			COALESCE(SUM(rl.cost_usd), 0) AS external_cost_usd,
			COUNT(DISTINCT rl.gw_session_id) AS session_count,
			COUNT(DISTINCT COALESCE(
				NULLIF(NULLIF(TRIM(rl.outbound_model), ''), '<unknown>'),
				NULLIF(NULLIF(TRIM(rl.client_model), ''), '<unknown>'),
				'unknown')) AS model_count
		FROM request_logs_with_current_month rl
		WHERE %s
		GROUP BY COALESCE(rl.owner_user, rl.api_key_owner_user)
	`, where)
	rows, err := db.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*UserWindowUsage{}
	for rows.Next() {
		var owner string
		var u UserWindowUsage
		var internal, external sql.NullFloat64
		if err := rows.Scan(&owner, &u.RequestCount, &u.InputTokens, &u.OutputTokens,
			&u.TotalTokens, &internal, &external, &u.SessionCount, &u.ModelCount); err != nil {
			warnRowSkip("userProfile.usageWindow", err)
			continue
		}
		u.InternalCostUSD = internal.Float64
		u.ExternalCostUSD = external.Float64
		out[owner] = &u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

	// owner 为 NULL 的行不构成「用户画像」：它扫进 string 会 500
	// （pgx 不做隐式 NULL→""），且 COUNT(DISTINCT owner) 本就忽略
	// NULL ⇒ 不过滤会让 total 与列表口径分叉。这里在两处查询统一排掉。
	// 2026-10-14 全量 API 扫描轮实测抓到（真库 session_owners 含 NULL 属主行）。
	// 2026-11-20 换锚：锚表 session_owners（v1 停写后冻结的陈旧子集）→
	// user_session_aggregates（981 MV，994 起 owner 键=COALESCE 双源键，
	// 与 overview/user-ranking/详情/projects 下钻全部同源同键）。
	// MV 缺席（迁移未应用）时走下方 IsSchemaBehindError 降级，与旧行为同构。
	nullOwnerFilter := "a.owner IS NOT NULL"

	// 三层隔离
	if IsRegularUser(r) {
		// 普通用户只看到自己
		owner := GetAuthContext(r).Username
		if owner == "" {
			writeError(w, http.StatusForbidden, "no user identity")
			return
		}
		baseWhere = "WHERE " + nullOwnerFilter + " AND a.owner = $1"
		baseArgs = append(baseArgs, owner)
	} else if IsTenantAdmin(r) {
		tenantID := GetTenantID(r)
		if tenantID != "" && tenantID != "default" {
			baseWhere = "WHERE " + nullOwnerFilter + " AND a.tenant_id = $1"
			baseArgs = append(baseArgs, tenantID)
		} else {
			baseWhere = "WHERE " + nullOwnerFilter
		}
	} else {
		// super_admin/admin_key — 可指定 tenant_id
		if q := queryString(r, "tenant_id"); q != "" {
			baseWhere = "WHERE " + nullOwnerFilter + " AND a.tenant_id = $1"
			baseArgs = append(baseArgs, q)
		} else {
			baseWhere = "WHERE " + nullOwnerFilter
		}
	}

	// 搜索（按 owner 模糊匹配）
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		paramIdx := len(baseArgs) + 1
		if baseWhere == "" {
			baseWhere = fmt.Sprintf("WHERE a.owner ILIKE $%d", paramIdx)
		} else {
			baseWhere += fmt.Sprintf(" AND a.owner ILIKE $%d", paramIdx)
		}
		baseArgs = append(baseArgs, "%"+search+"%")
	}

	// 2026-11-19 时间范围统一轮：date_from/date_to 过滤窗口内有活动的用户。
	// 口径落在 a.last_active_at（MV 桶内最后活跃时刻）：窗口语义 =「该用户在
	// [date_from, date_to] 内有会话活动」。与列表 last_seen 展示列同源，不引入新表。
	rawDateFrom := r.URL.Query().Get("date_from")
	rawDateTo := r.URL.Query().Get("date_to")
	if v := parseQueryTime(r, "date_from", time.Time{}); !v.IsZero() {
		paramIdx := len(baseArgs) + 1
		if baseWhere == "" {
			baseWhere = fmt.Sprintf("WHERE a.last_active_at >= $%d", paramIdx)
		} else {
			baseWhere += fmt.Sprintf(" AND a.last_active_at >= $%d", paramIdx)
		}
		baseArgs = append(baseArgs, v)
	}
	if v := parseQueryTime(r, "date_to", time.Time{}); !v.IsZero() {
		paramIdx := len(baseArgs) + 1
		if baseWhere == "" {
			baseWhere = fmt.Sprintf("WHERE a.last_active_at <= $%d", paramIdx)
		} else {
			baseWhere += fmt.Sprintf(" AND a.last_active_at <= $%d", paramIdx)
		}
		baseArgs = append(baseArgs, v)
	}

	// 总数
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(DISTINCT a.owner) FROM user_session_aggregates a %s", baseWhere)
	_ = h.db.QueryRow(ctx, countSQL, baseArgs...).Scan(&total)

	// ── 2026-11-20 数字出处轮：窗口内用量聚合 ──────────────────────────
	// 窗口 = date_from/date_to（与上面的 last_seen 过滤同参数），缺省近 30 天；
	// 表达式与 /users/{owner}/models 端点逐字一致 ⇒ 列表页「Token/内部成本/
	// 外部成本」列与详情页模型卡天然同源。出处块 usage_meta 随响应回传，
	// 帮助抽屉引用它而不是硬编码口径文案。
	usageStart, usageEnd, usageOK := usageWindowBounds(w, r, rawDateFrom, rawDateTo)
	if !usageOK {
		return
	}
	usageTenant := ""
	if IsTenantAdmin(r) || IsRegularUser(r) {
		usageTenant = GetTenantID(r)
	}
	if IsSuperAdminOrLegacy(r) {
		if q := queryString(r, "tenant_id"); q != "" {
			usageTenant = q
		}
	}
	// 普通用户只聚合自己（列表也只看得到自己）。
	usageOwner := ""
	if IsRegularUser(r) {
		usageOwner = GetAuthContext(r).Username
	}

	// 列表：从 user_session_aggregates 聚合每个 owner（2026-11-20 换锚，
	// 见上方过滤常量处的注释）。成本口径 = internal_cost + external_cost
	//（session_summaries 的 total_cost_usd + total_credits_charged，与 981
	// overview/user-ranking 完全同源）。first_seen 为**日粒度**（MV 日桶）。
	listSQL := fmt.Sprintf(`
		SELECT a.owner,
			COALESCE(SUM(a.session_count), 0)::bigint AS session_count,
			COALESCE(SUM(a.request_count), 0)::bigint AS total_requests,
			-- ★ 混合单位（美元 + 积分跨单位相加）：internal_cost 是美元、
			-- external_cost 是积分，两列相加**不是**美元金额。字段名
			-- total_cost_usd 不能改（前端依赖）⇒ 这里保留混合值并披露口径，
			-- 分列数字另给 internal_cost_usd / external_credits。
			COALESCE(SUM(a.internal_cost + a.external_cost), 0) AS total_cost_usd,
			-- 分列：内部成本（美元）。
			COALESCE(SUM(a.internal_cost), 0) AS internal_cost_usd,
			-- 分列：外部成本（积分）。
			COALESCE(SUM(a.external_cost), 0)::bigint AS external_credits,
			-- 混合单位（美元 + 积分跨单位相加）的每会话均价。
			CASE WHEN COALESCE(SUM(a.session_count), 0) > 0
				THEN SUM(a.internal_cost + a.external_cost) / SUM(a.session_count)
				ELSE 0 END AS avg_cost_per_session,
			-- 分列均价：内部成本（美元）/ 会话。
			COALESCE(CASE WHEN COALESCE(SUM(a.session_count), 0) > 0
				THEN SUM(a.internal_cost) / SUM(a.session_count)
				ELSE 0 END, 0) AS avg_internal_cost_usd_per_session,
			-- 分列均价：外部成本（积分）/ 会话。
			COALESCE(CASE WHEN COALESCE(SUM(a.session_count), 0) > 0
				THEN SUM(a.external_cost) / SUM(a.session_count)
				ELSE 0 END, 0) AS avg_external_credits_per_session,
			COALESCE(SUM(a.end_user_count), 0)::bigint AS end_user_count,
			MIN(a.date)::timestamptz AS first_seen_at,
			MAX(a.last_active_at) AS last_seen_at
		FROM user_session_aggregates a
		%s
		GROUP BY a.owner
		ORDER BY SUM(a.internal_cost + a.external_cost) DESC
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
		// ★ Scan 实参与上面 listSQL 的 SELECT 列**逐字对齐**（顺序也一致）：
		// 少扫一个 ⇒ 该列恒 0（分列成本会永远显示 0）；多扫一个 ⇒ 运行期
		// 类型错或静默串列。列数一致性由门钉住（sessionanalytics 包的
		// user_profile_cost_split_gate_test.go）。
		if err := rows.Scan(
			&u.OwnerUser, &u.SessionCount, &u.TotalRequests, &u.TotalCost,
			&u.InternalCostUSD, &u.ExternalCredits,
			&avgCost, &u.AvgInternalCostUSDPerSession, &u.AvgExternalCreditsPerSession,
			&u.EndUserCount, &u.FirstSeenAt, &u.LastSeenAt,
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

	// 窗口内用量并入每个员工行。canonical 视图缺席时降级（200 + degraded
	// 标记），不清空列表 —— 身份列来自 session_owners，仍可用。
	usage, err := usageAggregatesByOwner(ctx, h.db, usageStart, usageEnd, usageTenant, usageOwner)
	if err != nil {
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userProfile.usageWindow", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"users":           users,
				"total":           total,
				"limit":           limit,
				"offset":          offset,
				"degraded":        true,
				"degraded_reason": reason,
			})
			return
		}
		writeInternalErr(w, "usage window query failed", err)
		return
	}
	for i := range users {
		if u, ok := usage[users[i].OwnerUser]; ok {
			users[i].WindowUsage = u
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users":  users,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		// 真实用量窗口（缺省时为近 30 天）—— 前端展示口径用，不得脑补。
		"usage_window": map[string]string{
			"start": usageStart.Format(time.RFC3339),
			"end":   usageEnd.Format(time.RFC3339),
		},
		// 数字出处块：帮助抽屉与列头提示引用；口径改动时后端是唯一真源。
		"usage_meta": userUsageMeta{
			SourceView:  "request_logs_with_current_month",
			WindowStart: usageStart.Format(time.RFC3339),
			WindowEnd:   usageEnd.Format(time.RFC3339),
		},
		// 身份/会话聚合列的出处（2026-11-20 换锚）：owner 键为 COALESCE 双源键，
		// first_seen 为日粒度（MV 日桶）。成本口径 = internal+external，
		// 与 981 overview/user-ranking 同源。
		"identity_meta": map[string]string{
			"source_view": "user_session_aggregates",
			"owner_key":   "COALESCE(sessions.owner_user, session_dim.api_key_owner_user)",
			"first_seen":  "day-granularity (MV date bucket)",
		},
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
	// 2026-11-20 逐层下钻轮：/projects 子路由（员工→项目→任务→会话 的项目层）
	if strings.HasSuffix(path, "/projects") {
		h.handleUserOwnerProjects(w, r)
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

	// 1. 基础统计（2026-11-20 换锚 user_session_aggregates；见包级常量注释）
	baseWhere := "a.owner = $1"
	baseArgs := []interface{}{ownerUser}
	if tenantID != "" && tenantID != "default" {
		baseWhere += " AND a.tenant_id = $2"
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
	// 时区标准化：使用UTC日边界（migration 991）
	trendWhere := baseWhere
	trendArgs := make([]interface{}, len(baseArgs))
	copy(trendArgs, baseArgs)
	trendParamIdx := len(baseArgs) + 1
	trendWhere += fmt.Sprintf(" AND ss.first_request_at >= cst_days_ago($%d::INT)", trendParamIdx)
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
				// 尝试 YYYY-MM-DD 格式（时区审计轮 F4：按系统时区解释）。
				startTime, err = settings.ParseSystemDate(startDate)
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
				endTime, err = settings.ParseSystemDate(endDate)
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
	// 2026-11-20 修活体 500：canonical 视图 request_logs_with_current_month 的
	// 时间列是 **ts**（890 全量列面里没有 created_at，252 生产 information_schema
	// 实测确认）。原写法 rl.created_at 在每次执行都报 42703 ⇒
	// /users/{owner}/models 恒 500（详情页「模型使用统计」卡恒空）。
	// IsSchemaBehindError 不认 42703 ⇒ 连降级都不触发，是真 500。
	// 2026-11-20 数字出处轮：owner 键 = COALESCE(owner_user, api_key_owner_user)
	// —— 252 生产实测 owner_user（会话主属主）当前流量全 NULL，真实用户身份在
	// api_key_owner_user（密钥持有人）上；生产演进版 981 视图同键
	// （COALESCE(owner_user, api_key_owner_user)）⇒ 三处查询与部署真值一致。
	whereClause := "COALESCE(rl.owner_user, rl.api_key_owner_user) = $1 AND rl.ts >= $2 AND rl.ts < $3"
	args := []interface{}{ownerUser, startTime, endTime}
	argIdx := 4

	if tenantID != "" {
		whereClause += fmt.Sprintf(" AND rl.tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}

	// 查询模型使用统计
	// 2026-11-19：使用 request_logs_with_current_month（会话族 canonical 视图）
	// 而不是直接读 request_logs（已退役的 v1 表）。
	// 注意：cost_display 是内部定价，cost_usd 是外部成本
	// prompt_tokens + completion_tokens = total_tokens
	sql := fmt.Sprintf(`
		SELECT 
			COALESCE(
				NULLIF(NULLIF(TRIM(rl.outbound_model), ''), '<unknown>'),
				NULLIF(NULLIF(TRIM(rl.client_model), ''), '<unknown>'),
				'unknown') as model_name,
			COALESCE(p.display_name, rl.provider_id::text, 'unknown') as provider,
			COUNT(*) as request_count,
			COALESCE(SUM(rl.prompt_tokens), 0) as input_tokens,
			COALESCE(SUM(rl.completion_tokens), 0) as output_tokens,
			COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) as total_tokens,
			COALESCE(SUM(rl.cost_display), SUM(rl.cost_usd), 0)::float8 as internal_cost_usd,
			COALESCE(SUM(rl.cost_usd), 0)::float8 as external_cost_usd,
			MIN(rl.ts) as first_used_at,
			MAX(rl.ts) as last_used_at
		FROM request_logs_with_current_month rl
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

// ── 2026-11-20 数字出处轮：员工×模型用量矩阵 ─────────────────────────────

// UserUsageMatrixRow 员工×模型一行。成本口径与 handleUserModelUsage /
// usageAggregatesByOwner 逐字一致（internal=cost_display 回退 cost_usd，
// external=cost_usd），模型名表达式 = COALESCE(outbound_model, client_model)。
type UserUsageMatrixRow struct {
	OwnerUser       string    `json:"owner_user"`
	ModelName       string    `json:"model_name"`
	Provider        string    `json:"provider"`
	RequestCount    int64     `json:"request_count"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
	InternalCostUSD float64   `json:"internal_cost_usd"`
	ExternalCostUSD float64   `json:"external_cost_usd"`
	FirstUsedAt     time.Time `json:"first_used_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
}

// usageQuerier 是窗口用量查询所需的最小 DB 面：*pgxpool.Pool 与
// pgxmock.PgxPoolIface 都满足它 ⇒ 钉测注入 mock 即可覆盖全查询路径，
// 不必为单测起真库。
type usageQuerier interface {
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
}

// usageMatrixParams usage-matrix 端点的已解析参数（含三层隔离结果）。
type usageMatrixParams struct {
	Start    time.Time
	End      time.Time
	TenantID string
	Owner    string
	Limit    int
}

// resolveUsageMatrixParams 解析 + 校验 usage-matrix 的参数与隔离边界。
// 不触 DB ⇒ 400/403 全部可单测。时间解析语义与 handleUserModelUsage 一致
// （RFC3339 / YYYY-MM-DD；YYYY-MM-DD 的 end = 当天结束；跨度 ≤90 天；
// 缺省近 30 天）。三层隔离：普通用户锁定 owner=自己（越权 403）；
// 租户管理员锁本租户；超管可选 tenant_id。
func resolveUsageMatrixParams(w http.ResponseWriter, r *http.Request) (usageMatrixParams, bool) {
	startDate := queryString(r, "start_date")
	endDate := queryString(r, "end_date")
	var p usageMatrixParams
	var err error
	if startDate == "" && endDate == "" {
		p.End = time.Now()
		p.Start = p.End.AddDate(0, 0, -30)
	} else {
		if startDate != "" {
			p.Start, err = time.Parse(time.RFC3339, startDate)
			if err != nil {
				p.Start, err = settings.ParseSystemDate(startDate) // F4：系统时区
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid start_date format, use ISO 8601 or YYYY-MM-DD")
					return p, false
				}
			}
		} else {
			p.Start = time.Now().AddDate(0, 0, -90)
		}
		if endDate != "" {
			p.End, err = time.Parse(time.RFC3339, endDate)
			if err != nil {
				p.End, err = settings.ParseSystemDate(endDate) // F4：系统时区
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid end_date format, use ISO 8601 or YYYY-MM-DD")
					return p, false
				}
				p.End = p.End.Add(24 * time.Hour)
			}
		} else {
			p.End = time.Now()
		}
	}
	if p.End.Sub(p.Start) > 90*24*time.Hour {
		writeError(w, http.StatusBadRequest, "time range cannot exceed 90 days")
		return p, false
	}

	p.Limit = queryInt(r, "limit", 500)
	if p.Limit < 1 || p.Limit > 2000 {
		p.Limit = 500
	}

	// 三层隔离（与列表/详情端点同构）。
	if IsTenantAdmin(r) || IsRegularUser(r) {
		p.TenantID = GetTenantID(r)
	}
	if IsSuperAdminOrLegacy(r) {
		if q := queryString(r, "tenant_id"); q != "" {
			p.TenantID = q
		}
	}
	p.Owner = strings.TrimSpace(r.URL.Query().Get("owner"))
	if IsRegularUser(r) {
		self := GetAuthContext(r).Username
		if p.Owner != "" && p.Owner != self {
			writeError(w, http.StatusForbidden, "you can only view your own usage")
			return p, false
		}
		p.Owner = self
	}
	return p, true
}

// queryUserUsageMatrix 执行员工×模型聚合查询。表达式与 handleUserModelUsage
// 逐字一致（同一出处，两处查询不各自造口径）。
func queryUserUsageMatrix(ctx context.Context, db usageQuerier, p usageMatrixParams) ([]UserUsageMatrixRow, error) {
	where := "COALESCE(rl.owner_user, rl.api_key_owner_user) IS NOT NULL AND COALESCE(rl.owner_user, rl.api_key_owner_user) <> '' AND rl.ts >= $1 AND rl.ts < $2"
	args := []interface{}{p.Start, p.End}
	if p.TenantID != "" {
		where += fmt.Sprintf(" AND rl.tenant_id = $%d", len(args)+1)
		args = append(args, p.TenantID)
	}
	if p.Owner != "" {
		where += fmt.Sprintf(" AND COALESCE(rl.owner_user, rl.api_key_owner_user) = $%d", len(args)+1)
		args = append(args, p.Owner)
	}
	querySQL := fmt.Sprintf(`
		SELECT
			COALESCE(rl.owner_user, rl.api_key_owner_user) as owner_user,
			COALESCE(
				NULLIF(NULLIF(TRIM(rl.outbound_model), ''), '<unknown>'),
				NULLIF(NULLIF(TRIM(rl.client_model), ''), '<unknown>'),
				'unknown') as model_name,
			COALESCE(p.display_name, rl.provider_id::text, 'unknown') as provider,
			COUNT(*) as request_count,
			COALESCE(SUM(rl.prompt_tokens), 0) as input_tokens,
			COALESCE(SUM(rl.completion_tokens), 0) as output_tokens,
			COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) as total_tokens,
			COALESCE(SUM(rl.cost_display), SUM(rl.cost_usd), 0)::float8 as internal_cost_usd,
			COALESCE(SUM(rl.cost_usd), 0)::float8 as external_cost_usd,
			MIN(rl.ts) as first_used_at,
			MAX(rl.ts) as last_used_at
		FROM request_logs_with_current_month rl
		LEFT JOIN providers p ON p.id = rl.provider_id
		WHERE %s
		GROUP BY COALESCE(rl.owner_user, rl.api_key_owner_user), model_name, provider
		ORDER BY owner_user ASC, external_cost_usd DESC
		LIMIT $%d
	`, where, len(args)+1)
	args = append(args, p.Limit)

	rows, err := db.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rowsOut := []UserUsageMatrixRow{}
	for rows.Next() {
		var item UserUsageMatrixRow
		var internal, external sql.NullFloat64
		if err := rows.Scan(
			&item.OwnerUser, &item.ModelName, &item.Provider,
			&item.RequestCount, &item.InputTokens, &item.OutputTokens, &item.TotalTokens,
			&internal, &external,
			&item.FirstUsedAt, &item.LastUsedAt,
		); err != nil {
			warnRowSkip("userUsageMatrix", err)
			continue
		}
		item.InternalCostUSD = internal.Float64
		item.ExternalCostUSD = external.Float64
		rowsOut = append(rowsOut, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rowsOut, nil
}

// handleUserUsageMatrix GET /api/admin/session-analytics/users/usage-matrix
//
// 「每个员工在什么时间段用了哪些模型、各多少 token / 内部成本 / 外部成本」
// 的数据端点：一行 = (owner_user, model)。列表页展开行按 owner 取本端点；
// 「分模型看每个员工」由前端对同一份数据透视，不另设第二口径。
// 三层隔离与列表端点一致（普通用户锁定 owner=自己；租户管理员锁本租户；
// 超管可选 tenant_id）。参数解析见 resolveUsageMatrixParams；查询见
// queryUserUsageMatrix（出处块 usage_meta 随响应回传）。
func (h *Handler) handleUserUsageMatrix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	p, ok := resolveUsageMatrixParams(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rowsOut, err := queryUserUsageMatrix(ctx, h.db, p)
	if err != nil {
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userUsageMatrix", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"rows":            []UserUsageMatrixRow{},
				"degraded":        true,
				"degraded_reason": reason,
			})
			return
		}
		writeInternalErr(w, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rows":       rowsOut,
		"start_date": p.Start.Format(time.RFC3339),
		"end_date":   p.End.Format(time.RFC3339),
		// 数字出处块：帮助抽屉引用；口径改动时后端是唯一真源。
		"usage_meta": userUsageMeta{
			SourceView:  "request_logs_with_current_month",
			WindowStart: p.Start.Format(time.RFC3339),
			WindowEnd:   p.End.Format(time.RFC3339),
		},
	})
}

// ── 2026-11-20 逐层下钻轮（L1 收口）：员工→项目 ─────────────────────────

// UserProjectRow 员工×项目一行。
//
// ★ 数字出处 = session_summaries（会话级聚合）× session_owners（归属），
//
//	成本口径是**会话级**的：total_cost_usd（USD 成本）与 total_credits_charged
//	（积分）——与请求级 usage 列的「内部成本=cost_display / 外部成本=cost_usd」
//	**不同源不同义** ⇒ 前端项目卡不得沿用「内部/外部成本」标签（页面双口径
//	并存时以列头与帮助抽屉的来源标注为准）。
type UserProjectRow struct {
	ProjectID      string    `json:"project_id"`
	SessionCount   int64     `json:"session_count"`
	TaskCount      int64     `json:"task_count"`
	RequestCount   int64     `json:"request_count"`
	TotalTokens    int64     `json:"total_tokens"`
	TotalCostUSD   float64   `json:"total_cost_usd"`
	TotalCredits   float64   `json:"total_credits_charged"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// queryUserProjects 员工→项目聚合：归属 = session_dim 的
// COALESCE(owner_user, api_key_owner_user) 双源键（与请求级 usage、生产演进版
// 981 视图同键同 join）。★ 不经 session_owners：252 生产实测其行集是**陈旧
// 子集**（末次活跃停在 10 月）⇒ 叠加时间窗后恒空（2026-11-20 活体打回：
// dim 直连同窗有 87,080 行现役数据，session_owners 路径 0 行）。
// 任务数取 session_dim.task_id（剔除 default/空，与 981 视图同表达式）；
// 窗口 = ss.first_request_at >= cst_days_ago(days)（与详情页成本趋势同日界函数）。
// 该查询不触 canonical 请求视图 ⇒ 不进 canonical 读点登记簿（无 mention）。
func queryUserProjects(ctx context.Context, db usageQuerier, ownerUser, tenantID string, days, limit int) ([]UserProjectRow, error) {
	where := "COALESCE(sd.owner_user, sd.api_key_owner_user) = $1"
	args := []interface{}{ownerUser}
	if tenantID != "" && tenantID != "default" {
		where += fmt.Sprintf(" AND sd.tenant_id = $%d", len(args)+1)
		args = append(args, tenantID)
	}
	where += fmt.Sprintf(" AND ss.first_request_at >= cst_days_ago($%d::INT)", len(args)+1)
	args = append(args, days)
	querySQL := fmt.Sprintf(`
		SELECT
			ss.gw_project_id AS project_id,
			COUNT(DISTINCT ss.session_key) AS session_count,
			COUNT(DISTINCT sd.task_id) FILTER (WHERE sd.task_id IS NOT NULL AND sd.task_id::text <> '' AND sd.task_id::text <> 'default') AS task_count,
			COALESCE(SUM(ss.request_count), 0) AS request_count,
			COALESCE(SUM(ss.total_tokens), 0) AS total_tokens,
			COALESCE(SUM(ss.total_cost_usd), 0) AS total_cost_usd,
			COALESCE(SUM(ss.total_credits_charged), 0) AS total_credits_charged,
			MAX(ss.last_request_at) AS last_activity_at
		FROM session_summaries ss
		INNER JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s
			AND ss.gw_project_id IS NOT NULL AND ss.gw_project_id <> ''
		GROUP BY ss.gw_project_id
		ORDER BY session_count DESC, total_tokens DESC
		LIMIT $%d
	`, where, len(args)+1)
	args = append(args, limit)

	rows, err := db.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []UserProjectRow{}
	for rows.Next() {
		var item UserProjectRow
		var cost, credits sql.NullFloat64
		var lastAt sql.NullTime
		if err := rows.Scan(
			&item.ProjectID, &item.SessionCount, &item.TaskCount, &item.RequestCount,
			&item.TotalTokens, &cost, &credits, &lastAt,
		); err != nil {
			warnRowSkip("userProjects", err)
			continue
		}
		item.TotalCostUSD = cost.Float64
		item.TotalCredits = credits.Float64
		if lastAt.Valid {
			item.LastActivityAt = lastAt.Time
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// handleUserOwnerProjects GET /api/admin/session-analytics/users/{owner}/projects
//
// 「员工→项目」下钻的数据端点（主档 L1 草图落地）：按 gw_project_id 聚合该
// 员工在窗口内的会话/任务/token/成本。归属 = session_dim 双源键
// COALESCE(owner_user, api_key_owner_user)（252 生产实测 session_owners 行集
// 陈旧，见 queryUserProjects 注记）；窗口/上限与 models 端点同闸（days 1..90，
// 缺省 30）。三层隔离复用详情端点的同构逻辑（普通用户只能看自己）。
func (h *Handler) handleUserOwnerProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/users/")
	ownerUser := strings.TrimSuffix(path, "/projects")
	ownerUser = strings.TrimSpace(ownerUser)
	if ownerUser == "" {
		writeError(w, http.StatusBadRequest, "owner_user is required")
		return
	}

	// 普通用户只允许看自己（与详情/models 同构）
	if IsRegularUser(r) {
		myOwner := GetAuthContext(r).Username
		if myOwner == "" || ownerUser != myOwner {
			writeError(w, http.StatusForbidden, "you can only view your own profile")
			return
		}
	}

	tenantID := ""
	if IsTenantAdmin(r) || IsRegularUser(r) {
		tenantID = GetTenantID(r)
	}
	if IsSuperAdminOrLegacy(r) {
		if q := queryString(r, "tenant_id"); q != "" {
			tenantID = q
		}
	}

	days := queryInt(r, "days", 30)
	if days < 1 || days > 90 {
		days = 30
	}
	limit := queryInt(r, "limit", 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	projects, err := queryUserProjects(ctx, h.db, ownerUser, tenantID, days, limit)
	if err != nil {
		if IsSchemaBehindError(err) {
			reason := ReportSchemaBehind(slog.Default(), "userProjects", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"projects":        []UserProjectRow{},
				"degraded":        true,
				"degraded_reason": reason,
			})
			return
		}
		writeInternalErr(w, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"projects":   projects,
		"owner_user": ownerUser,
		"days":       days,
		// 数字出处块：项目卡数字是**会话级**口径（session_summaries×session_owners），
		// 与请求级 usage 的内外成本不同源——帮助抽屉与卡片注记引用此块。
		"usage_meta": userUsageMeta{
			SourceView: "session_summaries × session_dim",
		},
	})
}
