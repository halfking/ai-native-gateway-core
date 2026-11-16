package autoroute

// availability_filter_sql.go —— 决策面（autoroute）与候选面（provider）的
// **可用性判据对齐**（2026-11-17 auto 模式可用性轮 · 根修 6）。
//
// ★ 为什么要有这个文件：
//
// 决策面（autoroute 的 filterCurrentlyAvailable）与候选面（provider 的
// candidateQuerySQL）回答的是同一个问题——「这个 (凭据, 模型) 现在能不能
// 路由？」——但两边的闸**不是同一组**。差一道闸，auto 就会选中一个
// 候选面**必然滤空**的模型 ⇒ 请求在进 dispatch 之前就 503
// ⇒ 客户端只看到「原来的模型不可用」。
//
// ★ 211 真机取证（2026-11-17 00:24，auto 选到 qwen2.5-7b-instruct）：
//
//	credential 99023 / qwen2.5-7b-instruct 的状态闸**全部通过**——
//	credentials: status=active, routing_scope=all, availability_state=ready,
//	quota_state=ok, health_status=healthy, manual_disabled=false；
//	provider: enabled=true；binding: available=true；
//	v_routable_credential_models.is_routable = true。
//	⇒ 决策面认为它「可路由」，把它排到候选第 1（composite 76.2）。
//
//	但候选面多一道闸：**model lifecycle**（migration 966，2026-11-14 上线）——
//	models_canonical.released_at = 2024-09-19，超过 stale_threshold（2 years）
//	⇒ 该 offer 被候选面丢弃 ⇒ 候选集为空 ⇒ HTTP 503 no_candidates。
//
//	★ 这不是竞态，是**判据分歧**：决策面与候选面对同一行数据给出相反答案。
//
// ★ 本文件把候选面已有的三道闸补到决策面（缺一道就还会再犯）：
//
//	1. models_canonical.status = 'disabled' —— 候选面 matched CTE 有，决策面没有；
//	2. model lifecycle（released_at + stale_threshold + manual_priority 豁免）——
//	   候选面有（966 迁移），决策面没有 ⇒ **本轮实证命中的就是这一道**；
//	3. broken_confirmed 探针对（model probe）—— 候选面有（brokenPairExcludeSQL），
//	   决策面没有 ⇒ 探针已确认坏掉的模型会被决策选中，然后必然失败。
//
// ★ 通用式：**候选面与决策面必须同闸**。同键还不够——键一样、闸不一样，
// 仍然是「决策说能用、路由说不能用」，症状与键错位一模一样。

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/probemode"
)

// availabilityFilterSQLTemplate 是决策面实时可用性过滤的 SQL 正文。
//
// ★ 探针闸用占位符 /*BROKEN_PAIR_GUARD*/ 渲染而不是把表名内联进来：
// probemode.GuardStateTable() 是运行时函数，直接拼进 raw string 会把它
// 拆成两半 ⇒ 仓库的 sql_comment_syntax_test 扫描器（它专门盯「raw string
// 被拼接拆开后 `//` 注释对门隐形」）会失去视野。占位符 + strings.Replace
// 让 SQL 保持**一个**完整 raw string（provider 侧 brokenPairExcludeSQL
// 用的是同一手法）。
const availabilityFilterSQLTemplate = `
		SELECT cmb.credential_id, mc.canonical_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE (cmb.credential_id::text || ':' || lower(mc.canonical_name)) = ANY($1)
		  AND cmb.available = TRUE
		  AND pm.available = TRUE
		  AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
		  AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
		  -- credential-level availability (对齐 v_routable_credential_models 视图)
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, false) = false
		  AND COALESCE(c.availability_state, 'ready') = 'ready'
		  AND COALESCE(c.quota_state, 'ok') <> ALL (ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])
		  AND COALESCE(c.health_status, 'unknown') IN ('healthy', 'unknown')
		  -- provider-level availability
		  AND COALESCE(p.enabled, true) = true
		  AND COALESCE(p.manual_disabled, false) = false
		  -- node-probe backoff (对齐 v_routable_credential_models 视图)
		  AND NOT EXISTS (
		    SELECT 1 FROM node_probe_state nps
		    WHERE nps.credential_id = cmb.credential_id
		      AND nps.raw_model_name = pm.raw_model_name
		      AND nps.last_direct_ok = false
		      AND nps.next_retry_at > now()
		  )
		  -- ★ 根修 6-①：候选面 matched CTE 里的 models_canonical 状态闸。
		  -- 目录里被下架的模型在候选面查不到，决策面此前照样推荐。
		  AND COALESCE(mc.status, 'active') != 'disabled'
		  -- ★ 根修 6-②：model lifecycle 闸（对齐 provider 候选面，migration 966）。
		  -- 语义与候选面逐条一致：
		  --   * manual_priority > 0 ⇒ 运营手动置顶 ⇒ 豁免；
		  --   * released_at IS NULL ⇒ 年龄未知 ⇒ fail-open 放行；
		  --   * 否则 age(released_at) 必须 <= stale_threshold（缺设置项按 2 years）。
		  --   阈值子查询在**外层**套 COALESCE：只在内层套的话，设置项缺失时
		  --   子查询无行、比较结果为 NULL、整条 OR 链为 NULL ⇒ 有日期的模型被
		  --   **静默全丢**（与候选面注释里记的同一个陷阱）。
		  AND (
		      COALESCE(cmb.manual_priority, 0) > 0
		      OR mc.released_at IS NULL
		      OR age(now(), mc.released_at) <= COALESCE(
		          (SELECT (value #>> '{}')::interval
		             FROM settings_kv
		            WHERE key = 'model_lifecycle.stale_threshold'),
		          '2 years'::interval
		      )
		  )
		  -- ★ 根修 6-④：**统一可路由视图闸**（与候选面**同源同口径**）。
		  --
		  -- ★★ 为什么前面补了三道闸还不够、必须直接用视图：
		  --
		  -- 候选面（provider/client.go 的 candidateQuerySQL）的核心闸是
		  -- v.is_routable = TRUE —— 它来自**统一视图**
		  -- v_routable_credential_models（manual > auto 优先级的单一裁决）。
		  -- 决策面此前是**自己复刻**一套状态闸 ⇒ 两边的口径必然漂移
		  -- （实测：决策池 1010 个模型里 **189 个（19%）**「决策面活、候选面死」）。
		  -- ★★ 复刻永远追不上视图 ⇒ **别复刻，用同一个视图**。
		  --
		  -- 实测影响面（211 真库，2026-11-17）：决策池 1010 → **821**，
		  -- 而平台此刻真正可路由的模型有 **863** 个 ⇒ 加闸后决策池与真实
		  -- 可路由集合基本对齐（821/863）。
		  --
		  -- 视图里没有、而候选面另有的三道闸（canonical disabled /
		  -- model lifecycle / broken_confirmed）仍由下面三条保留 ——
		  -- 即：**视图口径 + 三道补闸**，两侧一致。
		  AND EXISTS (
		    SELECT 1 FROM v_routable_credential_models v
		    WHERE v.credential_id = cmb.credential_id
		      AND v.provider_model_id = pm.id
		      AND v.is_routable = TRUE
		  )
		  -- ★ 根修 6-③：broken_confirmed 探针对闸（对齐 provider 候选面）。
		  -- 探针已连续确认坏掉的 (凭据, 模型) 对，选它必然失败。
		  /*BROKEN_PAIR_GUARD*/
	`

// availabilityFilterSQL 返回决策面实时可用性过滤的最终 SQL。
func availabilityFilterSQL() string {
	return strings.Replace(availabilityFilterSQLTemplate, "/*BROKEN_PAIR_GUARD*/", brokenPairGuardSQL(), 1)
}

// brokenPairGuardSQL 渲染 broken_confirmed 探针对闸。
//
// 与 provider 侧 brokenPairExcludeSQL 同一手法（strings.Builder，避免 raw
// string 被拼接拆成两半）：表名由 probemode.GuardStateTable() 给出——
// legacy 栈读 model_probe_state，新栈读 v_node_probe_state_compat 投影。
func brokenPairGuardSQL() string {
	var b strings.Builder
	b.WriteString("NOT EXISTS (\n")
	b.WriteString("\t\t    SELECT 1 FROM " + probemode.GuardStateTable() + " mps\n")
	b.WriteString("\t\t    WHERE mps.credential_id = cmb.credential_id\n")
	b.WriteString("\t\t      AND mps.raw_model_name = pm.raw_model_name\n")
	b.WriteString("\t\t      AND mps.state = 'broken_confirmed'\n")
	b.WriteString("\t\t  )")
	return b.String()
}
