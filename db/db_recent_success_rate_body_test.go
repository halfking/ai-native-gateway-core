package db

import (
	"strings"
	"testing"
)

// TestRecentSuccessRateBodyServesBothHotFamilies（S3 波 2，2026-10-13）钉住
// recent_success_rate 函数体的双族取样形状。
//
// 背景：S4 停写（storage.request_logs_write_enabled=false）后新流量只落
// session_turns(_hot)。原函数体只读 request_logs_hot——3 小时窗口滑过停写
// 时刻后 samples 恒 0、rate 恒 NULL，而消费方把零读成「正常」
// （BelowThreshold=false），凭据健康体系的降级/恢复机制静默失灵。这正是
// request_logs_stop_write_classification_test 登记的主失效源；本钉桩防止
// 有人把会话腿「顺手清掉」而门依旧全绿。
//
// 判据同层：断言直接驱动 boot 时真正发给 PostgreSQL 的那段文本
// （recentSuccessRateEnsureSQL），不是测试里另抄的一份。
func TestRecentSuccessRateBodyServesBothHotFamilies(t *testing.T) {
	body := recentSuccessRateEnsureSQL

	// 两族热腿都必须在场——缺任何一腿，停写切换的某一侧就是盲区。
	for _, leg := range []string{"FROM request_logs_hot", "FROM public.session_turns_hot"} {
		if !strings.Contains(body, leg) {
			t.Errorf("recent_success_rate 函数体缺少 %q：停写切换的一侧将供不出样本", leg)
		}
	}
	// UNION ALL 连接（不是两段独立查询）——取样必须是并集内最新的
	// p_sample_n 条，写门开时才与原单腿语义等价。
	if !strings.Contains(body, "UNION ALL") {
		t.Error("两腿必须是 UNION ALL 并集取样，分离的两段查询会改变样本语义")
	}
	// 取样语义：外层按 ts 取最新 p_sample_n 条再聚合——丢了这层，
	// 样本量随窗口内总量浮动，rate 不再是「最近 N 条」的口径。
	if !strings.Contains(body, "ORDER BY ts DESC LIMIT p_sample_n") {
		t.Error("并集必须按 ts 取最新 p_sample_n 条再聚合（取样口径回归）")
	}
	// 会话腿的 credential_id 是 TEXT（707 S1a）：数字守卫 CAST 必须在——
	// 非数字凭据行直接 ::bigint 会 22P02 炸掉整条函数。
	if !strings.Contains(body, "credential_id ~ '^[0-9]+$'") {
		t.Error("会话腿 credential_id 缺数字守卫 CAST（TEXT 列直接 ::bigint 会 22P02）")
	}
	// 探针排除谓词在两腿都必须保留（v1 腿语义、会话腿防御）。
	probeArms := strings.Count(body, "NOT IN ('self_check', 'node_probe', 'system_health')")
	if probeArms < 2 {
		t.Errorf("探针排除谓词应出现在两腿（实存 %d 处）——v1 腿是语义、会话腿是防御", probeArms)
	}
	// 函数签名与默认值不变：调用点传参 (id, raw_model, 50, 3) 逐位对应。
	if !strings.Contains(body, "p_sample_n      INT DEFAULT 50") ||
		!strings.Contains(body, "p_window_hours  INT DEFAULT 3") {
		t.Error("函数签名/默认值漂移会打碎 CROSS JOIN LATERAL 调用点")
	}
}

// TestRoutingAnalyticsSourceServesSessionFamily（S3 波 2）钉住
// routing_analytics_source 的源。裸 v1 双腿（hot ∪ parent）在停写后随热窗
// 排空静默归零——整套自动路由分析端点（matrix/flow/funnel）与 7d 物化
// 视图都会变空报表。判据同层：routingAnalyticsMVSQL 就是 boot 时执行的文本。
func TestRoutingAnalyticsSourceServesSessionFamily(t *testing.T) {
	body := routingAnalyticsMVSQL
	if !strings.Contains(body, "FROM request_logs_with_current_month") {
		t.Error("routing_analytics_source 未改读 canonical 视图——停写后自动路由分析将静默归零")
	}
	if strings.Contains(body, "FROM request_logs_hot") || strings.Contains(body, "FROM request_logs;") {
		t.Error("routing_analytics_source 仍在直读 v1 底表")
	}
	// CREATE OR REPLACE 列契约：15 列逐一在场（matview 消费方会 42703）。
	for _, col := range []string{
		"task_type::text", "outbound_model::text", "client_model::text", "work_type::text",
		"provider_id::bigint", "credential_id::bigint", "is_auto_request::boolean",
		"tenant_id::text", "request_id::text", "success::boolean",
		"latency_ms::numeric", "cost_usd::numeric", "origin_stage::text",
		"auto_profile::text AS auto_profile",
	} {
		if !strings.Contains(body, col) {
			t.Errorf("routing_analytics_source 投影缺 %s", col)
		}
	}
}
