package db

import "strings"

// session_family_probe_arm.go —— /api/logs「包含探测」口径的第三臂（2026-11-21 统计口径对齐轮）。
//
// # 为什么需要这条臂
//
// request-logs 的原生源（SessionFamilyTurnsSourceWindowedSQL）只读
// session_turns_hot ∪ session_turns 两臂。2026-11-02 探测隔离（迁移 928）后，
// **新**探测流量物理落在 session_turns_probe_hot（窄表 25 列），业务两臂里
// 根本没有它 ⇒ /api/logs?include_probe=1（lane=all）今天只是「摘掉业务两臂上的
// stage 过滤」，对隔离后的当前探测**仍然是盲的** ⇒ 包含探测的总请求永远小于
// 供应商对账（usage_facts 全量）。
//
// 本文件补第三臂：session_turns_probe_hot 按同一份 118 列 canonical 投影展开，
// 与业务两臂 UNION ALL。包含探测模式的行集变成：
//
//	业务两臂（含隔离前的历史探测 marker 行）∪ 探测臂（隔离后的当前探测行）
//
// 三臂按 lane 互斥（同一 request_id 只落一条车道），COUNT 不重复。
//
// # 费用列（cost_usd / cost_display / cost_currency / credits_charged）
//
// probe_hot 窄表没有费用列。探测请求的计费事实在 usage_facts（traffic_class
// ≠ 'business' 的行，EventWriter 事务内落库，与供应商对账同源），按 request_id
// 聚合后 LEFT JOIN 进探测臂：
//
//	· 包含探测模式的费用合计 = 业务两臂 t.cost_usd ∪ 探测臂 usage_facts 费用
//	  == usage_facts 全量费用 == 供应商对账（board 含探测口径同源）。
//	· 不重复：按 request_id 聚合（usage_facts 一请求可多 revision，GROUP BY 去重）；
//	  业务车道行不进探测臂（车道互斥），历史 marker 行的费用在业务两臂自带
//	  （session_turns.cost_usd），探测臂的 usage_facts join 只挂在 probe_hot 行上。
//	· **INNER 语义（2026-11-21 线上实证后从 LEFT 改 INNER）**：211 真库实测
//	  probe_hot（43,455 行/日）⊇ usage_facts 探测面（14,401 行/日）——差集是
//	  node_prober 的 0-token 存活探测，它们**不进统计事件流**，也不进对账快照
//	  （供应商对账 = usage_facts 全量）。owner 的口径是「包含探测 == 供应商
//	  对账」⇒ 探测臂只保留**有 fact 的行**（INNER）⇒ COUNT/token/cost 三面
//	  逐字相等；无 fact 的 probe_hot 行不进 request-logs（与改前行为一致：
//	  它们此前在任何 face 都不可见）。fact 缺行（写延迟边缘）时该行当日
//	  不出现在包含探测口径里，次日不补（对账口径本就按日锁定）。
//
// # 类型契约
//
// UNION ALL 按位置匹型。本投影每个表达式与业务两臂（sessionFamilyProjection）
// 的**输出类型**逐列兼容（见 probeProjectionExprs 的逐列注释与
// TestProbeArmProjectionTypeParity 契约门）：业务臂是 jsonb 的列（tools /
// routing_attempts / attachments）探测臂出 NULL::jsonb；业务臂带
// ::numeric(14,8) / ::character varying(255) 显式转型的列探测臂带同一转型。
// attachment_count 在扫描结构体里是非指针 int，探测臂出字面量 0（不是 NULL）。

// probeArm*Expr 是探测臂费用三列的投影表达式。
//
// 内层子查询把 usage_facts 聚合列命名为 probe_cost / probe_currency /
// probe_credits 后整体别名成 t（见 SessionFamilyProbeArmSourceSQL 的
// `FROM (...) t`）⇒ 外层投影与其它列一样以 t. 引用。
const (
	probeArmCostExpr     = "t.probe_cost::numeric(14,8)"
	probeArmCurrencyExpr = "t.probe_currency"
	probeArmCreditsExpr  = "t.probe_credits"
)

// sessionProbeProjectionExprs 返回探测臂 118 列投影（按 canonicalColumnOrderV2
// 顺序，别名固定 t = session_turns_probe_hot 展开、uf = usage_facts 聚合）。
//
// 与 projectionExprsV2（业务臂回退表）逐位对应；init 期由
// TestProbeArmProjectionNameParity 钉住「名字集合与顺序完全一致」。
func sessionProbeProjectionExprs() []string {
	out := make([]string, len(canonicalColumnOrderV2))
	probeByColumn := map[string]string{
		"request_id":        "t.request_id",
		"ts":                "t.ts",
		"tenant_id":         "t.tenant_id::text",
		"api_key_id":        SessionFamilyAPIKeyIDExpr,
		"client_model":      "t.client_model",
		"outbound_model":    "t.outbound_model",
		"credential_id":     SessionFamilyCredentialIDExpr,
		"provider_id":       "t.provider_id",
		"prompt_tokens":     "t.prompt_tokens",
		"completion_tokens": "t.completion_tokens",
		"total_tokens":      "NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0)",
		"cost_usd":          probeArmCostExpr,
		"latency_ms":        "t.latency_ms",
		"success":           "t.success",
		"error_kind":        "t.error_kind",
		"request_preview":   "t.request_preview",
		"cost_display":      probeArmCostExpr,
		"cost_currency":     probeArmCurrencyExpr,
		"gw_session_id":     "(CASE WHEN t.gw_session_id LIKE 'sys:%' THEN NULL ELSE t.gw_session_id END)",
		"request_status":    sessionRequestStatusExpr,
		"task_type":         "t.task_type",
		"credits_charged":   probeArmCreditsExpr,
		"quality_flags":     "t.quality_flags",
		"agent_name":        "t.agent_name",
		"origin_actor":      "t.origin_actor::character varying(255)",
		"raw_model_name":    "t.raw_model_name",
		"client_ip":         "(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END)",
		"origin_stage":      "t.origin_stage",
	}
	// 无源列的「类型对齐 NULL/字面量」表：类型必须与业务臂同位置表达式的输出
	// 类型兼容（UNION ALL 位置匹型）。
	nullByColumn := map[string]string{
		"id": "NULL::bigint", "application_id": "NULL::bigint",
		"end_user_id": "NULL::text", "canonical_id": "NULL::bigint",
		"client_profile": "NULL::text", "request_mode": "NULL::text",
		"search_text": "NULL::text", "cache_read_tokens": "NULL::integer",
		"cache_write_tokens": "NULL::integer", "identity_hash": "NULL::text",
		"virtual_client_id": "NULL::text", "virtual_ip": "NULL::text",
		"virtual_mac": "NULL::text", "affinity_hit": "NULL::boolean",
		"stream_first_chunk_ms": "NULL::integer", "stream_chunk_count": "NULL::integer",
		"stream_interrupted": "NULL::boolean", "stream_done_sent": "NULL::boolean",
		"request_checksum": "NULL::text", "response_checksum": "NULL::text",
		"transform_rule_id": "NULL::text", "egress_protocol": "NULL::text",
		"failure_stage": "NULL::text", "failure_detail_code": "NULL::text",
		"transform_summary": "NULL::text", "response_preview": "NULL::text",
		"stream_done_received": "NULL::boolean", "usage_source": "NULL::text",
		"gw_task_id": "NULL::text", "api_key_prefix": "NULL::text",
		"owner_user": "NULL::text", "application_code": "NULL::text",
		"key_alias": "NULL::text", "api_key_owner_user": "NULL::text",
		"is_auto_request": "NULL::boolean", "auto_profile": "NULL::text",
		"auto_decision": "NULL::jsonb", "auto_confidence": "NULL::numeric(4,3)",
		"work_type": "NULL::text", "task_type_chosen": "NULL::text",
		"confidence_num": "NULL::numeric(4,3)", "model_chosen": "NULL::text",
		"strategy_used": "NULL::text", "parent_request_id": "NULL::text",
		"compression_reason": "NULL::text", "compression_strategy": "NULL::text",
		"compression_meta": "NULL::jsonb", "outbound_msg_count": "NULL::integer",
		"outbound_token_est": "NULL::integer", "outbound_msg_hashes": "NULL::jsonb",
		"quality_fix_actions": "NULL::jsonb", "quality_score": "NULL::numeric(3,2)",
		"upstream_finish_reason": "NULL::text", "tool_calls": "NULL::jsonb",
		"client_endpoint": "NULL::text", "client_timeout": "NULL::boolean",
		"stream_chunk_errors": "NULL::integer", "stream_chunks_sent": "NULL::integer",
		"client_request_id": "NULL::text", "upstream_status_code": "NULL::integer",
		"test_col": "NULL::text[]", "test_tab_indent": "NULL::text",
		"provider_model": "NULL::text", "attachments": "NULL::jsonb",
		"has_attachments": "NULL::boolean", "attachment_count": "0",
		"routing_attempts": "NULL::jsonb", "routing_summary": "NULL::text",
		"agent_type": "NULL::text", "client_protocol": "NULL::character varying(50)",
		"canonical_model": "NULL::text",
		"t0_arrived_at":   "NULL::timestamptz", "t1_total_enqueued_at": "NULL::timestamptz",
		"t2_total_dequeued_at": "NULL::timestamptz", "t3_model_enqueued_at": "NULL::timestamptz",
		"t4_model_dequeued_at": "NULL::timestamptz", "t5_cred_enqueued_at": "NULL::timestamptz",
		"t6_cred_dequeued_at": "NULL::timestamptz", "t7_forward_start_at": "NULL::timestamptz",
		"t8_response_start_at": "NULL::timestamptz", "t9_response_end_at": "NULL::timestamptz",
		"request_type": "NULL::text", "is_final_success": "NULL::boolean",
		"customer_id": "NULL::bigint", "request_class": "NULL::text",
		"due_at": "NULL::timestamptz", "system_fingerprint": "NULL::text",
		"credits_rate_multiplier": "NULL::double precision", "token_band": "NULL::text",
		"client_forwarded_for": "NULL::text",
	}
	for i, name := range canonicalColumnOrderV2 {
		if expr, ok := probeByColumn[name]; ok {
			out[i] = expr
			continue
		}
		expr, ok := nullByColumn[name]
		if !ok {
			// 契约漂移：canonical 新增列而探测臂没登记 ⇒ panic 而非静默出
			// `AS <name>` 语法错（与 projectionExprByColumn 同一防呆形状）。
			panic("session probe arm: no expression registered for column " + name)
		}
		out[i] = expr
	}
	return out
}

// SessionFamilyProbeArmSourceSQL 返回探测臂（带 usage_facts 费用 join）的
// 带括号 FROM 片段，别名约定与业务两臂一致（外层调用方追加 ` rl`）。
//
// tsFrom / tsTo 是 ts 窗口谓词的 SQL 片段（形如 "$3" / "$4"）——探测臂没有
// partition_date 列（迁移 928 的窄表），裁剪/过滤都直接走 ts（probe_hot 上有
// idx_session_turns_probe_hot_ts 前导索引）；usage_facts 费用聚合的
// occurred_at 界用同一对参数（同为 timestamptz 语义，与业务臂的 ts 谓词同窗）。
func SessionFamilyProbeArmSourceSQL(tsFrom, tsTo string) string {
	proj := sessionProbeProjectionExprs()
	parts := make([]string, len(proj))
	for i, expr := range proj {
		parts[i] = expr + " AS " + canonicalColumnOrderV2[i]
	}
	inner := `
		SELECT t.request_id, t.ts, t.tenant_id, t.credential_id, t.raw_model_name,
		       t.client_model, t.outbound_model, t.success, t.status_code,
		       t.error_kind, t.latency_ms, t.prompt_tokens, t.completion_tokens,
		       t.origin_stage, t.origin_actor, t.task_type, t.api_key_id,
		       t.provider_id, t.client_ip, t.request_preview, t.gw_session_id,
		       t.agent_name, t.quality_flags,
		       uf.probe_cost, uf.probe_currency, uf.probe_credits
		FROM public.session_turns_probe_hot t
		JOIN (
			SELECT request_id,
			       SUM(cost_amount) AS probe_cost,
			       MAX(cost_currency) AS probe_currency,
			       SUM(credits_charged) AS probe_credits
			FROM public.usage_facts
			WHERE occurred_at >= ` + tsFrom + ` AND occurred_at <= ` + tsTo + `
			  AND traffic_class <> 'business'
			GROUP BY request_id
		) uf ON uf.request_id = t.request_id
		WHERE t.ts >= ` + tsFrom + ` AND t.ts <= ` + tsTo
	return "(SELECT " + strings.Join(parts, ",\n\t\t") + " FROM (" + inner + ") t)"
}

// SessionFamilyTurnsSourceWindowedAllSQL = 业务两臂（SessionFamilyTurnsSourceWindowedSQL）
// ∪ 探测臂（SessionFamilyProbeArmSourceSQL）——/api/logs「包含探测」模式的三臂源。
//
// 口径：包含探测模式的行集 == 业务行集 + 当前探测行集；COUNT 对齐 usage_facts
// 全量（供应商对账），费用对齐 usage_facts 全量费用。与 board include_probes
// 口径（usage_facts 无 class 过滤）同真源、互为对拍面。
func SessionFamilyTurnsSourceWindowedAllSQL(pdFrom, pdTo, tsFrom, tsTo string) string {
	return "(SELECT * FROM " + SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo) +
		" UNION ALL SELECT * FROM " + SessionFamilyProbeArmSourceSQL(tsFrom, tsTo) + ")"
}
