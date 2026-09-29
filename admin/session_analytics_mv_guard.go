// Package admin — session-analytics 查询错误分类（R33 P-2）
//
// 357 物化视图族（session_client_stats / session_task_stats /
// session_client_task_matrix）是 admin session-analytics 端点的聚合源。
// 当目标库尚未执行迁移 357 时，查询以 42P01 失败；此前直接落
// writeInternalErr 500，运维无从分辨"缺视图"与"真故障"（三十二轮 P-2
// 只修了环境半边）。本文件把 42P01 分类为带引导信息的 503。
package admin

import (
	"net/http"
)

// analyticsViewMigrationHint 命中 357 视图族缺失时返回给前端的引导文案。
func analyticsViewMigrationHint(relation string) string {
	return "聚合视图 " + relation + " 尚未初始化（迁移 357 未执行），请先在目标库应用 357_session_analytics_aggregation_views 后重试"
}

// writeAnalyticsQueryErr 分类 session-analytics 聚合查询错误：
// 42P01 → 503 + analytics_view_missing（含视图名与迁移指引）；其余保持
// writeInternalErr 原语义。复用 dashboard_degrade 的判定与日志通道。
func writeAnalyticsQueryErr(w http.ResponseWriter, op string, err error) {
	if IsMissingRelationError(err) {
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_missing",
			analyticsViewMigrationHint(ExtractMissingRelationName(err)))
		return
	}
	writeInternalErr(w, op, err)
}
