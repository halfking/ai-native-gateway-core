// Package admin — session-analytics 查询错误分类（R33 P-2）
//
// 357 物化视图族（session_client_stats / session_task_stats /
// session_client_task_matrix）是 admin session-analytics 端点的聚合源。
// 当目标库尚未执行迁移 357 时，查询以 42P01 失败；此前直接落
// writeInternalErr 500，运维无从分辨"缺视图"与"真故障"（三十二轮 P-2
// 只修了环境半边）。本文件把 42P01 分类为带引导信息的 503。
package admin

import (
	"log/slog"
	"net/http"
)

// analyticsViewMigrationHint 命中 357 视图族缺失时返回给前端的引导文案。
func analyticsViewMigrationHint(relation string) string {
	return "聚合视图 " + relation + " 尚未初始化（迁移 357 未执行），请先在目标库应用 357_session_analytics_aggregation_views 后重试"
}

// writeAnalyticsQueryErr 分类 session-analytics 聚合查询错误：
// 42P01 → 503 + analytics_view_missing（含视图名与迁移 357 引导）；其余保持
// writeInternalErr 原语义。复用 dashboard_degrade 的判定；42P01 走
// ReportMissingRelation 的服务端日志通道（R35 复审补：此前仅客户端响应，
// 服务端无痕）。
func writeAnalyticsQueryErr(w http.ResponseWriter, op string, err error) {
	if IsMissingRelationError(err) {
		ReportMissingRelation(slog.Default(), op, err)
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_missing",
			analyticsViewMigrationHint(ExtractMissingRelationName(err)))
		return
	}
	writeInternalErr(w, op, err)
}

// writeAnalyticsDetailErr 是 detail 端点的分类双门（R35 复审补）：detail
// 此前把一切查询错误吞成 404 "not found"——缺 357 视图时运维会被误导为
// "任务/客户端不存在"。42P01 → 503 引导；其余维持 404 原语义（真不存在）。
func writeAnalyticsDetailErr(w http.ResponseWriter, notFoundMsg string, err error) {
	if IsMissingRelationError(err) {
		ReportMissingRelation(slog.Default(), "analytics detail", err)
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_missing",
			analyticsViewMigrationHint(ExtractMissingRelationName(err)))
		return
	}
	writeError(w, http.StatusNotFound, notFoundMsg)
}

// ── 陈旧度门（2026-10-07）─────────────────────────────────────────────
//
// 上一组门只区分「视图不存在」（42P01 → 503 + 迁移引导）。但 357 应用之后
// 还有一个更难发现的形态：**视图在，但数据停在上一次刷新的那一刻**。
// 2026-10-07 在 245 上实测到：视图 06:31:36 填过一次，之后全仓没有任何刷新
// 排程（通用刷新器只刷 632 的 routing_analytics_7d），而 session_summaries
// 一直在写；端点从 503 变成 200，返回 9h37m 前的数字，没有错误也没有提示。
//
// 42P01 那道门在这里**完全看不见**这种形态 —— 视图确实存在。
// 所以这里补的是第二道：按视图逐个查刷新戳，超出 sessionMvFreshnessBudget
// 就诚实地降级，而不是把陈旧数字当成结论发出去。
//
// 与 42P01 分成两个错误码：运维看到 analytics_view_missing 去应用迁移，
// 看到 analytics_view_stale 去查刷新器 —— 两件事的处置完全不同。

// sessionViewsFreshProbe 判定某个视图是否新鲜。抽成参数是为了让这道门
// 在**无库环境**下也能双向验证 —— 放行与拦截两条都必须可证，否则「返回 true」
// 那条分支永远没有覆盖，等于半个恒真断言。
// 生产实现绑定 mvFreshWithinBudget + sessionMvFreshnessBudget。
type sessionViewsFreshProbe func(view string) bool

// requireFreshSessionViews 在写响应前检查所列视图的新鲜度。全部新鲜返回 true；
// 任一超期则写出 503 并返回 false（调用方直接 return）。
//
// 刻意与 writeAnalyticsQueryErr 分开而不是合并进它：那个函数吃的是**查询返回的
// 错误**，而这里是在**查询之前**判断 —— 等查询跑完再判断，这份过期数据已经
// 付过一遍查询代价了。
func (h *Handler) requireFreshSessionViews(w http.ResponseWriter, r *http.Request, views ...string) bool {
	return requireFreshSessionViewsWith(
		w, r.URL.Path,
		mvFreshWithinBudgetFor(r.Context(), h.db, sessionMvFreshnessBudget),
		views...,
	)
}

// requireFreshSessionViewsWith 是可测试核心。budgeted 收窄成 func(string) bool
// 后，本文件不再依赖 pgxpool，测试可以注入任意新鲜度判定。
func requireFreshSessionViewsWith(w http.ResponseWriter, path string, budgeted sessionViewsFreshProbe, views ...string) bool {
	for _, view := range views {
		if budgeted(view) {
			continue
		}
		// 分不清是「视图不在」还是「刷新器没跑」时，两种处置都指向同一件事：
		// 去看日志确认。写进服务端而不只留在响应里。
		slog.Warn("session analytics view is stale or missing, refusing to serve",
			"view", view, "budget", sessionMvFreshnessBudget.String(), "path", path)
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_stale",
			"聚合视图 "+view+" 的数据已超出新鲜度预算（"+
				sessionMvFreshnessBudget.String()+
				"）或尚未被刷新——刷新器未运行。已拒绝返回过期数字，请稍后重试。")
		return false
	}
	return true
}
