// Package sessionanalytics — 物化视图新鲜度门
//
// ── 为什么必须有这道门（2026-10-07 仓内已踩过的坑）────────────────────
//
// 357 视图族建完时刷新过一次，之后**没有任何刷新排程** ⇒ 245 上实测到：
// 端点从 503 变成 200，返回的是 9h37m 前的数字，**没有错误、没有提示** ——
// 陈旧被伪装成了「就是这个数」。
//
// 仓内既有的两道门（admin/session_analytics_mv_guard.go）：
//
//	① 42P01（视图不在场）→ 503 analytics_view_missing + 迁移引导
//	② 陈旧度（视图在但数据停在上次刷新）→ 503 analytics_view_stale
//
// 本包读的是 981 建的 4 个视图，因此 ② 是必需的：没有它，视图一建好就
// 永远返回「建视图那一刻」的快照，而源表 session_summaries 一直在写。
//
// ★ 为什么在这里自己实现而不复用 admin.mvFreshWithinBudgetFor：
//
//	仓内那个函数的签名绑定 *pgxpool.Pool（未导出），而本包用的是
//	database/sql（stdlib.OpenDB 桥接）。判据本身只有两句 SQL，
//	在本包用同样的 SQL 实现一份，并把**同一个错误码**与**同一个预算**对齐
//	⇒ 前端与运维看到的行为与仓内其它 session-analytics 端点一致。
//
// ★ 刷新戳的来源：routing_mv_refresh_state（由 bg.MaterializedViewRefresher
//
//	每轮刷新写入 view_name + refreshed_at）。981 的 4 个视图已登记进
//	bg.SessionAnalyticsViews ⇒ 刷新器会写它们的戳 ⇒ 本门能读到。
package sessionanalytics

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// sessionMvFreshnessBudget 是会话分析视图族的陈旧度预算。
//
// ★ 算术（与 admin.sessionMvFreshnessBudget 对齐，90 分钟）：
//
//	刷新间隔 15 分钟（bg.SessionViewsInterval）+ 一轮实测 ~5 秒
//	稳定态最坏年龄 ≈ 15min + 5s  < 90min  ⇒ 放行
//	漏一轮             ≈ 30min        < 90min  ⇒ 仍放行（可容忍几轮漏刷）
//	停摆 90 分钟       ⇒ 503 analytics_view_stale
//
// ★ 为什么不用「间隔之外再留一轮」的余量：仓内 357 族刻意用 90min 口径，
//
//	两族若用不同预算，前端看到的降级时机就会不一致 ⇒ 同一个页面上两个卡片
//	一个报「陈旧」另一个不报。本包沿用 90 分钟。
const sessionMvFreshnessBudget = 90 * time.Minute

// sessionAnalyticsViews 是本包端点依赖的物化视图。
// ★ 与 bg.SessionAnalyticsViews 保持同步：那边少一个 ⇒ 这边永远判陈旧（503）；
//
//	那边多一个 ⇒ 无害（本门只查端点实际用到的视图）。
var sessionAnalyticsViews = []string{
	"user_session_aggregates",
	"project_task_aggregates",
	"model_usage_aggregates",
}

// mvFreshWithinBudgetSQL 判定视图「存在 + 最近被刷新过」。
//
// ★ 判据是**两个都要**：视图在场（pg_matviews）+ 有刷新戳（routing_mv_refresh_state）
//
//	且戳在预算内。缺刷新戳一律算陈旧（预 837 库 / 刷新器从未跑过 ⇒ 数据不可信）。
const mvFreshWithinBudgetSQL = `
SELECT
    (SELECT rs.refreshed_at FROM routing_mv_refresh_state rs WHERE rs.view_name = $1),
    EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname = 'public' AND matviewname = $1)
`

// viewsFreshWithinBudget 判定所列视图是否全部新鲜。
//
// ★ 任何一步出错（表不在场、查询失败、视图不存在、无刷新戳）都返回 false：
//
//	降级方向必须是「拒绝返回可能过期的数字」，而不是「乐观地当成新鲜」。
func viewsFreshWithinBudget(ctx context.Context, db *sql.DB, budget time.Duration, views ...string) bool {
	if db == nil {
		return false
	}
	for _, view := range views {
		var refreshedAt sql.NullTime
		var exists bool
		err := db.QueryRowContext(ctx, mvFreshWithinBudgetSQL, view).Scan(&refreshedAt, &exists)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				// 查询失败（含 routing_mv_refresh_state 不存在 ⇒ 预 837 库）
				return false
			}
			return false
		}
		if !exists || !refreshedAt.Valid {
			return false
		}
		if time.Since(refreshedAt.Time) >= budget {
			return false
		}
	}
	return true
}

// requireFreshViews 在写响应前检查所列视图的新鲜度。
//
// ★ 刻意与「查询报错」分开：这里是在**查询之前**判断 —— 等查询跑完再判断，
//
//	这份过期数据已经付过一遍查询代价了（与 admin.requireFreshSessionViews 同理）。
//
// 返回 false 时调用方必须直接 return（响应已写出）。
func (h *Handler) requireFreshViews(w http.ResponseWriter, r *http.Request, views ...string) bool {
	if viewsFreshWithinBudget(r.Context(), h.db, sessionMvFreshnessBudget, views...) {
		return true
	}
	writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_stale",
		"会话分析聚合视图的数据已超出新鲜度预算（"+sessionMvFreshnessBudget.String()+
			"）或尚未被刷新——刷新器未运行。已拒绝返回过期数字，请稍后重试。")
	return false
}
