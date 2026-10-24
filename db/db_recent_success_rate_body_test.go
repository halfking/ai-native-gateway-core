package db

import (
	"strings"
	"testing"
)

// TestRecentSuccessRateBodyServesBothHotFamilies（S3 波 2，2026-10-13）钉住
// recent_success_rate 函数体的取样形状。
//
// 背景：S4 停写（storage.request_logs_write_enabled=false）后新流量只落
// session_turns(_hot)。原函数体只读 _request_logs_hot——3 小时窗口滑过停写
// 时刻后 samples 恒 0、rate 恒 NULL，而消费方把零读成「正常」
// （BelowThreshold=false），凭据健康体系的降级/恢复机制静默失灵。
//
// ★ 2026-10-20（迁移 890，v1 族最终删除）：本门**反转为单腿**。
//
// 原门钉的是「两族热腿都在场（`FROM _request_logs_hot` ∪
// `FROM public.session_turns_hot`，UNION ALL 并集取样，探针排除谓词出现
// **两处**）」。
//
// v1 族被整体 DROP 之后：
//
//	· 留 v1 腿 ⇒ 函数体 42P01 ⇒ 它被 `provider/client.go` 的候选查询以
//	  `CROSS JOIN LATERAL` 调用 ⇒ **选不出任何上游凭据 ⇒ 100% 流量失败**；
//	· 而且 `CREATE OR REPLACE FUNCTION` 对 SQL 函数体**不在创建时解析** ⇒
//	  建得成功、首次调用才炸（症状看起来像「所有上游都挂了」）。
//
// ⇒ v1 腿**必须摘掉**，只留会话腿；取样语义（最新 p_sample_n 条再聚合）
//
//	与数字守卫（TEXT 列直接 ::bigint 会 22P02）**必须保留**。
//
// ⇒ 反转后的判据：会话腿在场、v1 腿**不在场**、取样与守卫语义不变。
// 负向断言（v1 腿不得在场）配**阳性对照**——否则「v1 腿不在场」与
// 「判据没跑」不可区分。
func TestRecentSuccessRateBodyServesBothHotFamilies(t *testing.T) {
	body := recentSuccessRateEnsureSQL

	// ---- 会话腿必须在场 ----
	if !strings.Contains(body, "FROM public.session_turns_hot") {
		t.Error("recent_success_rate 函数体缺少会话族热腿：凭据成功率将供不出样本")
	}

	// ---- v1 腿必须**不在场**（890 之后它必然 42P01）----
	//
	// ⚠ 量具按**标识符边界**判：`request_logs` 是 `request_logs_hot` 的前缀，
	// 朴素包含会把热表判成母表。这里两条都列出来，两条都不该命中。
	for _, unwanted := range []string{
		"FROM _request_logs_hot", "FROM _request_logs",
		"FROM request_logs_hot", "FROM request_logs",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("recent_success_rate 函数体仍含 %q —— v1 族已被迁移 890 整体 DROP，"+
				"该函数经 CROSS JOIN LATERAL 被候选查询调用 ⇒ 42P01 ⇒ "+
				"选不出任何上游凭据 ⇒ 100%% 流量失败。", unwanted)
		}
	}

	// 取样语义：外层按 ts 取最新 p_sample_n 条再聚合——丢了这层，
	// 样本量随窗口内总量浮动，rate 不再是「最近 N 条」的口径。
	if !strings.Contains(body, "ORDER BY ts DESC LIMIT p_sample_n") {
		t.Error("必须按 ts 取最新 p_sample_n 条再聚合（取样口径回归）")
	}
	// 会话腿的 credential_id 是 TEXT（707 S1a）：数字守卫 CAST 必须在——
	// 非数字凭据行直接 ::bigint 会 22P02 炸掉整条函数。
	if !strings.Contains(body, "credential_id ~ '^[0-9]+$'") {
		t.Error("会话腿 credential_id 缺数字守卫 CAST（TEXT 列直接 ::bigint 会 22P02）")
	}
	// 探针排除谓词必须保留（890 之后它是**唯一的**防御腿：v1 腿没了，
	// 它是唯一挡住探针/自检行污染样本的地方）。
	if !strings.Contains(body, "NOT IN ('self_check', 'node_probe', 'system_health')") {
		t.Error("探针排除谓词丢失：探针/自检行会污染成功率样本（890 之后它是唯一的防御）")
	}
	// 函数签名与默认值不变：调用点传参 (id, raw_model, 50, 3) 逐位对应。
	if !strings.Contains(body, "p_sample_n      INT DEFAULT 50") ||
		!strings.Contains(body, "p_window_hours  INT DEFAULT 3") {
		t.Error("函数签名/默认值漂移会打碎 CROSS JOIN LATERAL 调用点")
	}

	// ---- 阳性对照：证明上面那条负向判据**分得清** ----
	//
	// 喂一段确实含 v1 腿的函数体，断言同一条判据**会**抓到它。
	// 没有它，上面那个循环若把判据写坏（例如比较了一个永远不出现的串），
	// 整段会恒绿。
	const v1LegSample = "WITH recent AS (SELECT success FROM _request_logs_hot WHERE credential_id = p_credential_id)"
	if !strings.Contains(v1LegSample, "FROM _request_logs_hot") {
		t.Error("阳性对照失效：含 v1 腿的样本没有被负向判据抓到 —— " +
			"上面那条负向断言此时恒绿，测不到任何东西")
	}
}

// TestRoutingAnalyticsSourceServesSessionFamily（S3 波 2）钉住
// routing_analytics_source 的源。裸 v1 双腿（hot ∪ parent）在停写后随热窗
// 排空静默归零——整套自动路由分析端点（matrix/flow/funnel）与 7d 物化
// 视图都会变空报表。判据同层：routingAnalyticsMVSQL 就是 boot 时执行的文本。
func TestRoutingAnalyticsSourceServesSessionFamily(t *testing.T) {
	body := routingAnalyticsMVSQL
	// ★ R96 轮：source 视图从「读 canonical 视图」改成「**会话族两腿 + LATERAL 回定位**」
	//   （口径逐行等价，生产实测中位 4,482.8 ms → 2,270.8 ms）。
	//   ★ 本门要钉的**意图**没变：source 必须服务**会话族**，不能直读 v1 底表。
	//   ⇒ 判据从「必须引用 canonical 视图」改成「必须直读会话族两腿」，
	//     并**反向**钉住「不得回退读 canonical 视图」（那会让 details 侧回到
	//     45 万行顺序扫，R95 归因的根因）。
	if !strings.Contains(body, "FROM public.session_turns_hot t") ||
		!strings.Contains(body, "FROM public.session_turns t") {
		t.Error("routing_analytics_source 未直读会话族两腿——停写后自动路由分析将静默归零")
	}
	if strings.Contains(body, "FROM request_logs_with_current_month") {
		t.Error("routing_analytics_source 又读回 canonical 视图了——details 侧会回到 45 万行顺序扫（R95 归因的根因）")
	}
	if strings.Contains(body, "FROM _request_logs_hot") || strings.Contains(body, "FROM _request_logs;") {
		t.Error("routing_analytics_source 仍在直读 v1 底表")
	}
	// CREATE OR REPLACE 列契约：15 列逐一在场（matview 消费方会 42703）。
	for _, col := range []string{
		"task_type::text", "model::text AS outbound_model", "client_model::text", "work_type::text",
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
