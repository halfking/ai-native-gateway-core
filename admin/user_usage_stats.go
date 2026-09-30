package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// user_usage_stats.go — /users 页面用户级统计端点（2026-09-30 统计 UI 优化轮）。
//
// 数据面选型（本地 8782 库实证，2026-09-30）：
//   - 读面 = usage_facts（report_rollup 快照同源的事实表，occurred_at 有索引、
//     实时流入、自带 credits_charged/latency_ms/ttft_ms/status/raw_model_name），
//     owner 维度经 api_keys.owner_user（= 平台账号 username）关联；
//     口径与对账快照一致（窗口内全部事实行）。
//   - 放弃 request_logs_with_current_month：全 owner GROUP BY 本地实测
//     22–35s（视图外层 LATERAL request_class 富化 + 中间层 NOT EXISTS
//     相关子查询 542K 次/行索引扫描），页面端点不可用。
//   - request_logs.owner_user 本地全空；api_key_owner_user 虽有值但读面过重。
//   - 不依赖 session_owners 视图（仓内 schema 与本地库均无定义，独立遗留项）。
//
// 权限与 /api/users 同口径：super_admin/admin_key 全量；tenant_admin 限本租户
// （usage_facts.tenant_id 过滤 + 目标用户须属本租户）。
//
// usage_facts 多行可共享 request_id（修订/种子复用），rollup 的口径即
// 「窗口内全部事实行」，本组端点保持一致，不做 per-request 去重。

// userUsageRow — usage-summary 单行。
type userUsageRow struct {
	OwnerUser  string     `json:"username"`
	Requests   int64      `json:"requests"`
	Tokens     int64      `json:"tokens"`
	Credits    int64      `json:"credits"`
	LastActive *time.Time `json:"last_active_at"`
}

// userUsageSummaryResponse — GET /api/admin/users/usage-summary 响应。
type userUsageSummaryResponse struct {
	Days  int            `json:"days"`
	Items []userUsageRow `json:"items"`
}

// handleUserUsageSummary GET /api/admin/users/usage-summary?days=30
// 一次 GROUP BY 出全量账号维度聚合，供 /users 列表用量列与顶部统计条；
// 顶部统计（活跃账号数等）由前端拿 items 对用户列表 reduce 得出。
func (h *Handler) handleUserUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	days := queryInt(r, "days", 30)
	if days < 1 || days > 365 {
		days = 30
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// tenant_admin 限本租户（usage_facts.tenant_id 直接过滤）；super/admin_key 全量。
	where := ""
	args := []any{days}
	if IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r) {
		tenantID := GetTenantID(r)
		if tenantID != "" && tenantID != "default" {
			where = " AND f.tenant_id = $2"
			args = append(args, tenantID)
		}
	}

	rows, err := h.db.Query(ctx, `
		SELECT agg.owner, agg.requests, agg.tokens, agg.credits, agg.last_active
		FROM (
			SELECT k.owner_user AS owner,
			       COUNT(*)::bigint AS requests,
			       COALESCE(SUM(f.total_tokens), 0)::bigint AS tokens,
			       COALESCE(SUM(f.credits_charged), 0)::bigint AS credits,
			       MAX(f.occurred_at) AS last_active
			FROM usage_facts f
			JOIN api_keys k ON k.id = f.api_key_id
			WHERE f.occurred_at >= now() - ($1 * INTERVAL '1 day')
			  AND COALESCE(k.owner_user, '') <> ''`+where+`
			GROUP BY 1
		) agg
		JOIN users u ON u.username = agg.owner
		ORDER BY agg.requests DESC
	`, args...)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	resp := userUsageSummaryResponse{Days: days, Items: []userUsageRow{}}
	for rows.Next() {
		var row userUsageRow
		if err := rows.Scan(&row.OwnerUser, &row.Requests, &row.Tokens, &row.Credits, &row.LastActive); err != nil {
			continue
		}
		resp.Items = append(resp.Items, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// userStatsKpi — 单用户画像 KPI。
type userStatsKpi struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Credits      int64   `json:"credits"`
	Errors       int64   `json:"errors"`
	ErrorRate    float64 `json:"error_rate"`
	LatencyP95Ms *int64  `json:"latency_p95_ms"`
}

// userStatsBucket — Top 模型/应用/密钥行。
type userStatsBucket struct {
	Name     string  `json:"name"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

// userStatsRecentRequest — 最近请求行（ttft=首字延迟，latency=总耗时）。
type userStatsRecentRequest struct {
	Ts           time.Time `json:"ts"`
	Model        string    `json:"model"`
	FirstChunkMs *int64    `json:"first_chunk_ms"`
	TotalMs      *int64    `json:"total_ms"`
	Credits      int64     `json:"credits"`
	Status       string    `json:"status"`
}

// userStatsResponse — GET /api/admin/users/{id}/stats 响应。
type userStatsResponse struct {
	UserID    int                      `json:"user_id"`
	Username  string                   `json:"username"`
	Days      int                      `json:"days"`
	Kpi       userStatsKpi             `json:"kpi"`
	Daily     []tenantDailyStat        `json:"daily"`
	TopModels []userStatsBucket        `json:"top_models"`
	TopApps   []userStatsBucket        `json:"top_apps"`
	TopKeys   []userStatsBucket        `json:"top_keys"`
	Recent    []userStatsRecentRequest `json:"recent_requests"`
}

// handleUserStats GET /api/admin/users/{id}/stats?days=30
// users.id → users.username → api_keys.owner_user = username 关联
// usage_facts 聚合；无用量用户返回零值 KPI + 全零 daily（前端空态）。
func (h *Handler) handleUserStats(w http.ResponseWriter, r *http.Request, id int) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	days := queryInt(r, "days", 30)
	if days < 1 || days > 90 {
		days = 30
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 解析用户 + 权限（tenant_admin 只能看本租户用户）。
	// 仅 ErrNoRows 映射 404；基础设施错误（DB 不可达/超时）走 500，
	// 不与「用户不存在」混淆（R36-A2 审计修复，先例 analytics.go R46 F9）。
	var username, tenantID string
	err := h.db.QueryRow(ctx, `SELECT username, tenant_id FROM users WHERE id = $1`, id).Scan(&username, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		slog.Warn("user_stats: lookup user failed", "user_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "lookup user failed")
		return
	}
	if IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r) {
		myTenant := GetTenantID(r)
		if myTenant != "" && myTenant != "default" && myTenant != tenantID {
			// 跨租户目标与不存在统一 404 掩蔽（R36-A4，对齐兄弟实现
			// requireSessionTaskAccess 的口径）：先 403 会构成跨租户用户
			// ID 存在性 oracle。
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
	}

	resp := userStatsResponse{
		UserID: id, Username: username, Days: days,
		Kpi:       userStatsKpi{},
		Daily:     []tenantDailyStat{},
		TopModels: []userStatsBucket{},
		TopApps:   []userStatsBucket{},
		TopKeys:   []userStatsBucket{},
		Recent:    []userStatsRecentRequest{},
	}

	// 事实行统一过滤片：该账号名下密钥 + 租户 + 窗口。
	const factFilter = ` f.api_key_id IN (SELECT id FROM api_keys WHERE owner_user = $1)
		AND f.tenant_id = $2 AND f.occurred_at >= now() - ($3 * INTERVAL '1 day')`

	// KPI（单行聚合；窗口内无事实行时全部零值）。Scan 错误（超时/连接）
	// 必须 500，绝不静默降级成「该用户无用量」。
	if err := h.db.QueryRow(ctx, `
		SELECT COUNT(*)::bigint,
		       COALESCE(SUM(f.total_tokens), 0)::bigint,
		       COALESCE(SUM(f.credits_charged), 0)::bigint,
		       COUNT(*) FILTER (WHERE f.status <> 'success')::bigint,
		       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY f.latency_ms), 0)::bigint
		FROM usage_facts f
		WHERE `+factFilter, username, tenantID, days).Scan(
		&resp.Kpi.Requests, &resp.Kpi.Tokens, &resp.Kpi.Credits, &resp.Kpi.Errors, &resp.Kpi.LatencyP95Ms); err != nil {
		writeInternalErr(w, "kpi query failed", err)
		return
	}
	if resp.Kpi.Requests > 0 {
		resp.Kpi.ErrorRate = float64(resp.Kpi.Errors) / float64(resp.Kpi.Requests)
	}

	// 按天序列（generate_series 零填充，与租户统计同形状）。日切显式钉
	// Asia/Shanghai（R36-A3）：与 usage_facts 日分区边界（迁移 750/751）及
	// 租户统计 daily 同口径；不随会话时区漂移（UTC 服务器上否则与分区
	// 归属差最多 8h）。对账页保持显式 UTC 日（结算口径，有意分叉）。
	if dailyRows, derr := h.db.Query(ctx, `
		WITH days AS (
			SELECT generate_series(
				date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') - (($3::int - 1) * INTERVAL '1 day'),
				date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai'),
				INTERVAL '1 day')::date AS d
		), agg AS (
			SELECT date_trunc('day', f.occurred_at AT TIME ZONE 'Asia/Shanghai')::date AS d,
			       COUNT(*)::bigint AS requests,
			       COUNT(*) FILTER (WHERE f.status = 'success')::bigint AS success,
			       COUNT(*) FILTER (WHERE f.status <> 'success')::bigint AS errors,
			       COALESCE(SUM(f.total_tokens), 0)::bigint AS tokens,
			       COALESCE(SUM(f.credits_charged), 0)::bigint AS credits,
			       COALESCE(SUM(f.cost_amount), 0)::float8 AS cost
			FROM usage_facts f
			WHERE `+factFilter+`
			GROUP BY 1
		)
		SELECT to_char(days.d, 'YYYY-MM-DD'),
		       COALESCE(agg.requests, 0), COALESCE(agg.success, 0), COALESCE(agg.errors, 0),
		       COALESCE(agg.tokens, 0), COALESCE(agg.credits, 0), COALESCE(agg.cost, 0)
		FROM days LEFT JOIN agg ON agg.d = days.d
		ORDER BY days.d
	`, username, tenantID, days); derr == nil {
		for dailyRows.Next() {
			var d tenantDailyStat
			if serr := dailyRows.Scan(&d.Date, &d.Requests, &d.Success, &d.Errors, &d.Tokens, &d.Credits, &d.Cost); serr == nil {
				resp.Daily = append(resp.Daily, d)
			}
		}
		if rowsErr := dailyRows.Err(); rowsErr != nil {
			dailyRows.Close()
			writeInternalErr(w, "daily query failed", rowsErr)
			return
		}
		dailyRows.Close()
	}

	// Top 模型 / 应用 / 密钥（各 5 行）。
	bucketQueries := []struct {
		target *[]userStatsBucket
		sql    string
	}{
		{&resp.TopModels, `
			SELECT COALESCE(NULLIF(f.raw_model_name, ''), '<unknown>'),
			       COUNT(*)::bigint,
			       COALESCE(SUM(f.total_tokens), 0)::bigint,
			       COALESCE(SUM(f.credits_charged), 0)::bigint,
			       COALESCE(SUM(f.cost_amount), 0)::float8
			FROM usage_facts f
			WHERE ` + factFilter + `
			GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 5`},
		{&resp.TopApps, `
			SELECT COALESCE(app.code, '<none>'),
			       COUNT(*)::bigint,
			       COALESCE(SUM(f.total_tokens), 0)::bigint,
			       COALESCE(SUM(f.credits_charged), 0)::bigint,
			       COALESCE(SUM(f.cost_amount), 0)::float8
			FROM usage_facts f
			LEFT JOIN applications app ON app.id = f.application_id
			WHERE ` + factFilter + `
			GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 5`},
		{&resp.TopKeys, `
			SELECT COALESCE(NULLIF(k.key_alias, ''), COALESCE(NULLIF(k.key_prefix, ''), '<unknown>')),
			       COUNT(*)::bigint,
			       COALESCE(SUM(f.total_tokens), 0)::bigint,
			       COALESCE(SUM(f.credits_charged), 0)::bigint,
			       COALESCE(SUM(f.cost_amount), 0)::float8
			FROM usage_facts f
			JOIN api_keys k ON k.id = f.api_key_id
			WHERE ` + factFilter + `
			GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 5`},
	}
	for _, bq := range bucketQueries {
		// 桶查询失败/中断只降级该桶（fail-open），但必须留日志并可感知
		// rows.Err()——15s ctx 中途超时会把截断的 Top 列表当完整数据
		// 返回（R36-A2 审计修复）。
		bRows, berr := h.db.Query(ctx, bq.sql, username, tenantID, days)
		if berr != nil {
			slog.Warn("user_stats: bucket query failed", "user_id", id, "err", berr)
			continue
		}
		for bRows.Next() {
			var b userStatsBucket
			if serr := bRows.Scan(&b.Name, &b.Requests, &b.Tokens, &b.Credits, &b.CostUSD); serr != nil {
				slog.Warn("user_stats: bucket scan failed", "user_id", id, "err", serr)
				continue
			}
			*bq.target = append(*bq.target, b)
		}
		if rerr := bRows.Err(); rerr != nil {
			slog.Warn("user_stats: bucket rows aborted", "user_id", id, "err", rerr)
		}
		bRows.Close()
	}

	// 最近请求（10 行，窗口内）。
	rRows, rerr := h.db.Query(ctx, `
		SELECT f.occurred_at,
		       COALESCE(NULLIF(f.raw_model_name, ''), '-'),
		       f.ttft_ms, f.latency_ms,
		       COALESCE(f.credits_charged, 0),
		       COALESCE(NULLIF(f.status, ''), 'unknown')
		FROM usage_facts f
		WHERE `+factFilter+`
		ORDER BY f.occurred_at DESC LIMIT 10
	`, username, tenantID, days)
	// 同上：失败/中断降级留日志，不把截断列表当完整数据（R36-A2）。
	if rerr != nil {
		slog.Warn("user_stats: recent query failed", "user_id", id, "err", rerr)
	} else {
		for rRows.Next() {
			var rr userStatsRecentRequest
			if serr := rRows.Scan(&rr.Ts, &rr.Model, &rr.FirstChunkMs, &rr.TotalMs, &rr.Credits, &rr.Status); serr != nil {
				slog.Warn("user_stats: recent scan failed", "user_id", id, "err", serr)
				continue
			}
			resp.Recent = append(resp.Recent, rr)
		}
		if rerr2 := rRows.Err(); rerr2 != nil {
			slog.Warn("user_stats: recent rows aborted", "user_id", id, "err", rerr2)
		}
		rRows.Close()
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleUserStatsDispatcher — /api/admin/users/{id}/stats 的路径分发
// （{id} 为数字，stats 为子动作）。
func (h *Handler) handleUserStatsDispatcher(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "stats" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	h.handleUserStats(w, r, id)
}
