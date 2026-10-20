package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/internaltraffic"
)

// ensureRequestLogsCurrentMonthView keeps the canonical read view
// `request_logs_with_current_month` in its **session-only (two-arm)** shape.
//
// 2026-09-07 事故自愈（历史）：共享 PG 上的 canonical 查询视图被带外操作删掉，
// /api/logs 全部请求 42P01。写路径不受影响，只有读路径挂——这类"表在、视图没了"
// 的状态没有任何 migration 会重建，网关也没有启动自愈，于是持续报错。⇒ 本函数
// 的存在意义始终是**自愈**：视图不在（或形态不对）时把它装回去。
//
// 2026-09-14 存储优化方案 v2 S2：canonical 升级为 session 家族拼装体
// （session_turns(_hot) 投影 UNION ALL 冻结 v1 体 × 反连接）。约 40 个读方
// 同名同列契约零改动切换。
//
// ★ 2026-10-20（迁移 885）：v1 族（`_request_logs*`）整体退役 ⇒ 第三臂
// （冻结 v1 体 + 两条反连接去重腿）与它依赖的两张中间层包装视图
// （…_without_customer_id / …_without_request_class_due_at）**一并消失**。
// 剩下的两臂（session_turns_hot ∪ session_turns，734 details 特征层
// LEFT JOIN）的投影文本与 710/815/816/817/824 装出来的逐字相同 ⇒ 118 列同名
// 同序 ⇒ 读方零改动。
//
// ⚠ 自愈**不得再触碰 v1 族**：那些表已被 885 删除，包装视图也没了 ⇒ 任何
// 重建 v1 链的分支都会 42P01，而且失败点正是"刚删掉的表"，排查成本极高。
// ⇒ 下面**没有**任何 `_request_logs` 字面量；会话族缺席时**不做 DDL**。
//
// ⚠ 精确仪器：判"v1 臂还在不在"用 `strpos`，**不用** `LIKE '%…_request_logs…%'`
// —— SQL 的 LIKE 里 `_` 是**单字符通配符**（审计 §10.8 的坏尺子）。同一把
// 尺子不能换到新判据上。
//
// 幂等：视图健康且已是两臂形态时零 DDL。
func (d *DB) ensureRequestLogsCurrentMonthView(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 734：details 特征层在场时，最新形态须含 session_turn_details JOIN；
	// 老库（733 未跑）回退 710 形态仍视为健康，零 DDL。
	var detailsFamilyExists bool
	if err := d.pool.QueryRow(ctx, `
		SELECT to_regclass('public.session_turn_details_hot') IS NOT NULL
		   AND to_regclass('public.session_turn_details') IS NOT NULL
	`).Scan(&detailsFamilyExists); err != nil {
		return fmt.Errorf("probe session_turn_details family: %w", err)
	}
	var canonicalExists, bodyIsSessionOnly, bodyHasV1Arm bool
	// 体形探测基于 pg_views 行内求值（视图缺失时 EXISTS 子查询零行，
	// pg_get_viewdef 不会被求值——不能直接对 '...'::regclass 取视图定义，
	// 视图缺失会 42P01 炸掉整个探针）。
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
		),
		EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
			  AND strpos(pg_get_viewdef(schemaname || '.' || viewname, true), 'session_turns') > 0
			  AND (NOT $1 OR strpos(pg_get_viewdef(schemaname || '.' || viewname, true), 'session_turn_details') > 0)
		),
		EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
			  AND strpos(pg_get_viewdef(schemaname || '.' || viewname, true), '_request_logs') > 0
		)
	`, detailsFamilyExists).Scan(&canonicalExists, &bodyIsSessionOnly, &bodyHasV1Arm); err != nil {
		return fmt.Errorf("probe request_logs_with_current_month body: %w", err)
	}
	if canonicalExists && bodyIsSessionOnly && !bodyHasV1Arm {
		return nil
	}
	if !canonicalExists {
		slog.Warn("request_logs_with_current_month missing; self-healing the session-only view " +
			"(mirror of sql/migrations/startup/885_drop_v1_request_logs_family.sql)")
	}

	// 会话族在场（430+526+707）→ 装两臂体。缺会话族时**没有可退的形态**：
	// v1 族已随 885 退役 ⇒ 此时不做任何 DDL，把问题留给启动序列报错，
	// 而不是让自愈在一个已经退役的族上失败。
	var sessionTurnsExist bool
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'session_turns'
		)
	`).Scan(&sessionTurnsExist); err != nil {
		return fmt.Errorf("probe session_turns presence: %w", err)
	}
	if !sessionTurnsExist {
		slog.Warn("request_logs_with_current_month: session_turns absent and the v1 family is retired " +
			"(885) — no fallback body to install; leaving the view as-is")
		return nil
	}
	if _, err := d.pool.Exec(ctx, canonicalSessionOnlyDDL(detailsFamilyExists)); err != nil {
		return fmt.Errorf("rebuild request_logs_with_current_month as session-only two-arm body: %w", err)
	}
	if _, err := d.pool.Exec(ctx, `COMMENT ON VIEW public.request_logs_with_current_month IS `+
		canonicalSessionOnlyComment); err != nil {
		return fmt.Errorf("comment request_logs_with_current_month: %w", err)
	}
	slog.Info("request_logs_with_current_month view installed as the session-only two-arm body "+
		"(session_turns_hot ∪ session_turns; the v1 arm is retired with the v1 family)",
		"details_join", detailsFamilyExists)
	return nil
}

// sessionFamilyProjectionV2 是 710 会话分支的 113 列投影（别名固定 t；hot 与
// parent 两分支复用同一文本）。列映射契约（直映/派生/守卫 CAST/NULL 补位）
// 逐列登记见 sql/migrations/startup/710_request_logs_view_session_family_v2.sql
// 头注释；修改任一侧必须同步另一侧（contract 测试校验 viewdef 等价）。
// projectionExprsV2 holds the 113 session-branch expressions in canonical
// column order. init-time composition joins them with
// canonicalColumnOrderV2 into `+"`"+`expr AS name`+"`"+` pairs — bare NULL casts would
// otherwise name view columns after their type ("int8"/"text"), colliding
// into 42701 duplicate column names.
// sessionIsInternalSessionIDExpr is the **read layer's** definition of "this
// row is not a user session": any session id starting with `sys:` is internal
// (health probes, auto workers) and its projected gw_session_id is NULLed out
// so it can never join to a user-facing session.
//
// ⚠ It is a named constant, not a literal repeated at each use site, because
// the population measurement in session_dim_population_test.go has to classify
// rows with **the same** predicate the read layer uses. A second copy of
// `LIKE 'sys:%'` would be free to drift, and the direction it drifts in is
// the misleading one: a gate that counts `sys:` traffic as user traffic
// reports a dim-coverage number that is wrong in exactly the way nobody
// re-checks. See that file for the measured consequence (0.00% vs 97.78%).
//
// ⚠ This is NOT the same thing as the `is_auto_request` column, which looks
// interchangeable and is not. Measured 2026-10-05 on the local real database
// over the full session family (parent ∪ hot, 1,693,480 rows):
//
//	sys:%    + is_auto_request=true  : 753,425
//	sys:%    + is_auto_request=other :       0   ← the implication is exact
//	non-sys: + is_auto_request=true  :   1,439
//	non-sys: + is_auto_request=other : 938,616
//
// So `sys:%` implies auto, but auto does not imply `sys:%`. The read layer keys
// off the id, and the gate must too — a measurement keyed off the column would
// report a different population than the one production serves.
const sessionIsInternalSessionIDExpr = "(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)"

var projectionExprsV2 = []string{
	"NULL::bigint",
	"t.request_id",
	"t.ts",
	"t.tenant_id::text",
	"(CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END)",
	"(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)",
	"t.end_user_id",
	"NULL::text",
	"t.model",
	"(CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END)",
	"NULL::bigint",
	"t.canonical_id",
	"NULL::text",
	"t.submit_mode",
	"t.prompt_tokens",
	"t.completion_tokens",
	"NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0)",
	"t.cost_usd::numeric(14,8)",
	"t.latency_ms",
	"t.success",
	"t.error_kind",
	"t.search_text",
	"t.cache_read_tokens",
	"t.cache_write_tokens",
	"t.identity_hash",
	"t.virtual_client_id",
	"NULL::text",
	"NULL::text",
	"NULL::boolean",
	"t.stream_first_chunk_ms",
	"t.stream_chunk_count",
	"t.stream_interrupted",
	"t.stream_done_sent",
	"t.request_checksum",
	"t.response_checksum",
	"NULL::text",
	"t.egress_protocol",
	"t.failure_stage",
	"t.failure_detail_code",
	"t.request_preview",
	"t.transform_summary",
	"t.response_preview",
	"t.stream_done_sent",
	"t.cost_display::numeric(14,8)",
	"t.cost_currency",
	"t.usage_source",
	sessionIsInternalSessionIDExpr,
	"NULL::text",
	sessionRequestStatusExpr,
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"t.is_auto_request",
	"t.task_type",
	"NULL::text",
	"(CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END)",
	"t.auto_confidence::numeric(4,3)",
	"t.work_type",
	"t.task_type_chosen",
	"NULL::numeric(4,3)",
	"NULL::text",
	"NULL::text",
	"t.credits_charged",
	"t.parent_request_id",
	"NULL::text",
	"t.compression_strategy",
	"t.compression_meta",
	"NULL::integer",
	"NULL::integer",
	"NULL::jsonb",
	"NULL::text[]",
	"NULL::jsonb",
	"NULL::numeric(3,2)",
	"t.upstream_finish_reason",
	// ⚠ tools 尚无写入路径，本列恒为空数组 —— 读它得不到信息。
	// 事实校准（2026-10-19 复核）：列由 456 建为
	// `tools JSONB NOT NULL DEFAULT '[]'::jsonb`，所以它在库里是 **'[]' 而非
	// NULL**；本投影把它原样透出，形状正确、值为空。
	// 缺的是写入方：turn_writer.go 的 INSERT 列清单里没有 tools，
	// 全仓也无人写它（同 attempt_no，见 admin/turns_sessions.go 的注）。
	// ⇒ 别写「投影了 tools 就等于有工具调用数据」的判据（恒真）。
	"t.tools",
	"t.client_endpoint",
	"t.client_timeout",
	"NULL::integer",
	"NULL::integer",
	"t.client_request_id",
	"t.upstream_status_code",
	"NULL::text[]",
	"NULL::text",
	"NULL::text",
	"NULL::jsonb",
	"(CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END)",
	"t.attachment_count",
	"t.routing_attempts",
	"t.routing_summary",
	"t.agent_name",
	"t.agent_type",
	"t.client_protocol::character varying(50)",
	"t.canonical_model",
	"t.t0_arrived_at",
	"t.t1_total_enqueued_at",
	"t.t2_total_dequeued_at",
	"t.t3_model_enqueued_at",
	"t.t4_model_dequeued_at",
	"t.t5_cred_enqueued_at",
	"t.t6_cred_dequeued_at",
	"t.t7_forward_start_at",
	"t.t8_response_start_at",
	"t.t9_response_end_at",
	"NULL::text",
	"t.is_final_success",
	"t.origin_actor::character varying(255)",
	"t.customer_id",
	"NULL::text",
	"NULL::timestamptz",
	"t.system_fingerprint",
	"t.raw_model_name",
	// 738 追加尾列：session 分支无源补位（NULL::double precision）。
	"NULL::double precision",
	// 740 追加的 client_ip：**816 起改为 session 侧有源投影**（审计 §9.60.6.1）。
	//
	// 原注释写「session_turns.client_ip 为 text 且未回填，不能直映」——两半都要改：
	// 「未回填」是**错的**（本机 7 天 172,305/202,774 = 85.0% 有值；252 7 天 17,586 行
	// 有值），而「text 不能直映」只说明它**需要一次显式转换**，不是不能映。
	//
	// 与 v1 同义的依据（252 生产库，审计 §9.60.6.1）：同 request_id 配对 826 行
	// session_turns.client_ip == host(_request_logs.client_ip) **826/826、差异 0**。
	// 本机那条「两列 100% 相同 ⇒ 它是转发头副本」的旧裁决已被证伪——本机全库
	// client_forwarded_for 只有 6 个 distinct 取值、多跳链路 0 条，零分辨力。
	//
	// 为什么带守卫而不是直转：session_turns.client_ip 是 **text**，没有类型
	// 约束，一个畸形值会让 `::inet` 抛错并**打挂整条 canonical 视图的每一个读方**。
	// 代价是「畸形 → NULL」而不是「整条查询报错」。
	//
	// **守卫判据在 817 换成 pg_input_is_valid**（审计 §9.64）。816 用的是字符类
	// 正则 `^[0-9a-fA-F:.]+$`，它**只挡住非字符集的垃圾**：
	// `192.168.1` / `deadbeef` / `1.2.3.4.5.6` / `:::` / `...` / `999.1.1.1`
	// **全部通过那个正则**，然后在 `::inet` 上抛错——真库实测已复现。
	// 我在 §9.61 报过一张守卫行为表（garbage / 多跳链 / 空串 全落 NULL），
	// **那张表只测了字符类不合法的值，于是给了「守卫已覆盖」一个过宽的结论**。
	// `pg_input_is_valid(v,'inet')`（PG 16+，本仓一致跑 PG 17）是**完整的语义
	// 判据**：同一批坏值 192.168.1→f、deadbeef→f、':::'→f，而 '1.2.3.4' 与
	// '2a06:98c0:3600::103'→t。
	//
	// 写方侧也补了一道 net.ParseIP（并行会话，origin_mw.resolveClientIP）——
	// 但那**只保护新写入的行**：库里已有的值、以及任何别的写入方仍会进到这里，
	// 所以读侧的语义判据不能省。
	"(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END)",
	// 813 追加尾列（§9.22）：三列的 session 侧源在 session_turns 上，且与 v1
	// 逐值一致（真库 1,515,960 行同 request_id 配对实测 both_differ=0），
	// 缺的只是「近窗覆盖率」——近 7 天 origin_stage/client_forwarded_for 已达
	// 86%。trace_events 同批**不**投影：镜像从未写它（1d/7d/30d 非空率恒为
	// 0），而 v1 侧有 691,883 行带值且已被反连接丢弃 ⇒ 投影它等于把数据
	// 换成 NULL，是 §9.18「修好了但变全盲」的同一形状。
	"t.origin_stage",
	"t.token_band",
	"t.client_forwarded_for",
}

// sessionFamilyProjectionV2 is the session branch projection for BOTH turn
// sources (alias fixed to t), each expression explicitly named to the frozen
// contract. Column mapping contract: sql/migrations/startup/
// 710_request_logs_view_session_family_v2.sql header.
// detailsProjectionColumns is the 734 view-contract group: details column
// name → the v1 frozen-contract NULL type. withDetails=true projections emit
// `d.<name>` at these positions; false falls back to the 710 NULL placeholders.
// Types match public.session_turn_details DDL (migration 733) exactly, so the
// UNION ALL positional typing is unchanged in both shapes.
var detailsProjectionColumns = map[string]string{
	"client_model": "text", "provider_id": "bigint", "client_profile": "text",
	"virtual_ip": "text", "virtual_mac": "text", "affinity_hit": "boolean",
	"transform_rule_id": "text", "gw_task_id": "text", "api_key_prefix": "text",
	"owner_user": "text", "application_code": "text", "key_alias": "text",
	"api_key_owner_user": "text", "auto_profile": "text", "confidence_num": "numeric(4,3)",
	"model_chosen": "text", "strategy_used": "text", "compression_reason": "text",
	"outbound_msg_count": "integer", "outbound_token_est": "integer", "outbound_msg_hashes": "jsonb",
	"quality_flags": "text[]", "quality_fix_actions": "jsonb", "quality_score": "numeric(3,2)",
	"stream_chunk_errors": "integer", "stream_chunks_sent": "integer", "attachments": "jsonb",
	"request_type": "text", "request_class": "text", "due_at": "timestamptz",
}

// buildSessionProjectionExprs composes the session-branch expressions for an
// explicit canonical column-name list: details positions flip between NULL
// placeholders (710 shape) and d.<col> (734 shape); everything else comes
// from projectionExprsV2 **looked up by column name**.
//
// 按名查而不是按下标取，是 815 引入 preRateColumnOrder 之后的硬要求：回退
// 列序不是完整列序的前缀（credits/client_ip 插在 815 三列之前），按下标取
// 会把 `NULL::double precision` 贴到 `origin_stage` 这个名字上——列名对得上
// 而列是错的，UNION ALL 只按位置匹型，所以它不报错、只是让 pre-736 回退体
// 的三列恒 NULL。守卫 TestViewV2ProjectionContractSync 的三形态断言就是
// 为这条而设：它读的是 `t.<col> AS <col>` 这段文本，下标错位会立刻变红。
func buildSessionProjectionExprs(names []string, withDetails bool) []string {
	out := make([]string, len(names))
	for i, name := range names {
		if withDetails {
			if _, ok := detailsProjectionColumns[name]; ok {
				out[i] = "d." + name
				continue
			}
		}
		expr, ok := projectionExprByColumn[name]
		if !ok {
			// 契约漂移：投影表里没有这一列。panic 而非静默产出空表达式——
			// 静默会渲染出 ` AS origin_stage` 这种语法错，错误现场离真因很远。
			panic(fmt.Sprintf("session projection: no expression registered for column %q", name))
		}
		out[i] = expr
	}
	return out
}

// projectionExprByColumn 是 projectionExprsV2 的按名索引，与
// canonicalColumnOrderV2 一一对应（init 期自校验长度与逐位一致）。
var projectionExprByColumn = func() map[string]string {
	m := make(map[string]string, len(projectionExprsV2))
	for i, name := range canonicalColumnOrderV2 {
		if i >= len(projectionExprsV2) {
			panic(fmt.Sprintf("projectionExprsV2 shorter than canonicalColumnOrderV2 at %q", name))
		}
		m[name] = projectionExprsV2[i]
	}
	if len(projectionExprsV2) != len(canonicalColumnOrderV2) {
		panic(fmt.Sprintf("projectionExprsV2 has %d entries, canonicalColumnOrderV2 has %d",
			len(projectionExprsV2), len(canonicalColumnOrderV2)))
	}
	return m
}()

// sessionRequestStatusExpr is the session-leg expression for the frozen v1
// `request_status` column. It is a package-level const (not a function) so that
// the view-ensure chain, both native readers, and migration 824 all render the
// identical text — the viewdef-equivalence contract in
// db/view_schema_v2_contract_test.go compares the composed DDL verbatim.
//
// ── Why the error_kind branch exists (audit §9.160) ────────────────────────
//
// The previous expression classified a turn as `rate_limited` only via
// `t.status_code = 429`. Measured on the local real database
// (1,688,629 rows in session_turns, 2026-09/10):
//
//   - `status_code = 429` matches **0** rows. Not one, in the whole table.
//   - 437,402 rows are genuine rate-limited turns, and every one of them
//     carries `status_code = 500` on the session side.
//   - So the `rate_limited` arm was **dead code on the session leg**: it could
//     never fire. All 437,402 turns were reported as plain `failure`.
//   - The only signal that survives the mirror is `error_kind`:
//     `error_kind = 'rate_limit_exceeded'` ⟺ v1 `request_status =
//     'rate_limited'` with **0 counterexamples in either direction**
//     (394,614 twin rows) and 0 rows with `success = true`.
//
// Effect of the old expression on session_turns:
//
//	failure 1,358,245 / success 330,384 / rate_limited 0
//	after:   failure   920,843 / success 330,384 / rate_limited 437,402
//
// i.e. the canonical view — and the eight native readers that share this
// projection — overstated `failure` by 32% and under-reported `rate_limited`
// as 0. This is not a retirement-only concern: it is wrong **today**, and once
// _request_logs is dropped the view would report zero rate-limited requests
// system-wide.
//
// ── Why the status_code = 429 arm is kept ──────────────────────────────────
//
// It is dead on today's data but it is the shape a future upstream 429 would
// take, and dropping it would make the expression silently narrower than the
// one it replaces. Keeping it costs nothing and can only add correct rows.
//
// ── Why the NULL-success guard stays first ────────────────────────────────
//
// Unchanged contract, deliberately: `success IS NULL` still yields NULL rather
// than a guessed label. Local data has 0 such rows, so this is latent, and
// preserving it keeps the change provably scoped to the rate_limited arm.
// (The admin session-detail reader had the matching latent hole — it scanned
// `success` into a bare bool and dropped the whole row on NULL; fixed there.)
//
// ── Why the IN-set, not just 'rate_limit_exceeded' (R41 audit F1) ──────────
//
// The historical cross-tab above proved the single-value form only on
// pre-823 data, where mirrors never recorded request_status at all. The
// write side emits RequestStatusRateLimited with exactly two error_kind
// literals: 'rate_limit_exceeded' (gateway RPM/concurrent rejections,
// handler.go:3244 / messages.go:433 / responses.go) and 'key_throttled'
// (handler.go:2376, api-key anomalous-usage throttle). Post-823 mirrors
// carry the rate_limited label while keeping error_kind='key_throttled';
// a single-value arm would file every such row under 'failure' forever —
// and the view's session leg wins over the v1 leg, so nothing downstream
// could correct it.
const sessionRequestStatusExpr = `(CASE ` +
	`WHEN t.success IS NULL THEN NULL ` +
	`WHEN t.success THEN 'success' ` +
	`WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited' ` +
	`WHEN t.status_code = 429 THEN 'rate_limited' ` +
	`ELSE 'failure' END)`

// sessionFamilyProjectionFor renders the named projection (details-aware) for
// an explicit canonical name list.
func sessionFamilyProjectionFor(names []string, withDetails bool) string {
	exprs := buildSessionProjectionExprs(names, withDetails)
	if len(exprs) != len(names) {
		panic(fmt.Sprintf("session projection drift: %d exprs vs %d contract names",
			len(exprs), len(names)))
	}
	parts := make([]string, len(exprs))
	for i, expr := range exprs {
		parts[i] = expr + " AS " + names[i]
	}
	return strings.Join(parts, ",\n\t\t")
}

// sessionFamilyProjection renders the full canonical-column projection
// (details-aware). Both the view-ensure chain and native readers go through
// this one composer.
func sessionFamilyProjection(withDetails bool) string {
	return sessionFamilyProjectionFor(canonicalColumnOrderV2, withDetails)
}

// canonicalSessionOnlyComment 与迁移 885 的 COMMENT ON VIEW 同文（COMMENT 的
// IS 只收单个字面量，不能像 SQL 赋值那样 || 拼接）。824 起补记 request_status
// 的分类口径（816/817 的两次 client_ip 改写当时未同步本常量，视图注释一度落后
// 两代；这里补的是最新状态，不是逐版流水）。885 起本常量描述**两臂**形态。
const canonicalSessionOnlyComment = `'会话存储解耦 v4（885）: v1 族（_request_logs*）整体退役 ⇒ canonical 视图去掉第三臂（冻结 v1 体 + 双反连接去重），只剩两臂 session_turns_hot ∪ session_turns。两臂投影与 710/734/813/815/816/817/824 装出来的逐字相同 ⇒ 118 列同名同序 ⇒ 读方零改动。仍 NULL：id（v1 请求行 id ≠ session turn id）/test_col/test_tab_indent/provider_model/credits_rate_multiplier，以及 client_ip 本身（字面量非法时落 NULL，不抛错）。trace_events 刻意未投影（镜像从不写，近窗非空率 0，投影即净数据损失）。'`

// canonicalColumnOrderV2 是 115 列的契约顺序（= 现网 canonical 视图列序：
// 710 冻结 113 列 + 738 credits_rate_multiplier + 740 client_ip）。
// UNION ALL 按位置匹型：会话分支按此顺序展开投影；v1 分支内层（包装链 +
// lateral）在冻结/动态两形态下列序不同（fp/raw 位置漂移），故外面包一层
// 按名归一化的显式投影——两形态下内层都恰好携带全部 113 个名字。
var canonicalColumnOrderV2 = []string{
	"id", "request_id", "ts", "tenant_id", "application_id", "api_key_id",
	"end_user_id", "client_model", "outbound_model", "credential_id",
	"provider_id", "canonical_id", "client_profile", "request_mode",
	"prompt_tokens", "completion_tokens", "total_tokens", "cost_usd",
	"latency_ms", "success", "error_kind", "search_text", "cache_read_tokens",
	"cache_write_tokens", "identity_hash", "virtual_client_id", "virtual_ip",
	"virtual_mac", "affinity_hit", "stream_first_chunk_ms", "stream_chunk_count",
	"stream_interrupted", "stream_done_sent", "request_checksum",
	"response_checksum", "transform_rule_id", "egress_protocol",
	"failure_stage", "failure_detail_code", "request_preview",
	"transform_summary", "response_preview", "stream_done_received",
	"cost_display", "cost_currency", "usage_source", "gw_session_id",
	"gw_task_id", "request_status", "api_key_prefix", "owner_user",
	"application_code", "key_alias", "api_key_owner_user", "is_auto_request",
	"task_type", "auto_profile", "auto_decision", "auto_confidence",
	"work_type", "task_type_chosen", "confidence_num", "model_chosen",
	"strategy_used", "credits_charged", "parent_request_id",
	"compression_reason", "compression_strategy", "compression_meta",
	"outbound_msg_count", "outbound_token_est", "outbound_msg_hashes",
	"quality_flags", "quality_fix_actions", "quality_score",
	"upstream_finish_reason", "tool_calls", "client_endpoint", "client_timeout",
	"stream_chunk_errors", "stream_chunks_sent", "client_request_id",
	"upstream_status_code", "test_col", "test_tab_indent", "provider_model",
	"attachments", "has_attachments", "attachment_count", "routing_attempts",
	"routing_summary", "agent_name", "agent_type", "client_protocol",
	"canonical_model", "t0_arrived_at", "t1_total_enqueued_at",
	"t2_total_dequeued_at", "t3_model_enqueued_at", "t4_model_dequeued_at",
	"t5_cred_enqueued_at", "t6_cred_dequeued_at", "t7_forward_start_at",
	"t8_response_start_at",
	"t9_response_end_at",
	"request_type",
	"is_final_success",
	"origin_actor",
	"customer_id",
	"request_class",
	"due_at",
	"system_fingerprint",
	"raw_model_name",
	// 738（R56）+ 740（R57 B7）追加尾列：自愈重建体必须携带，否则 bg
	// rollup（r.credits_rate_multiplier）与 client_ip 维度在读端缺列。
	"credits_rate_multiplier",
	"client_ip",
	// 813 追加尾列：origin_stage/token_band/client_forwarded_for。id 刻意
	// 不在其中——真库 1,515,984 组同 request_id 配对里 r.id = t.id 命中
	// **0** 次：v1 的 _request_logs.id 是请求行 id，session_turns.id 是 turn
	// id，判据是「同一个东西」而不是「session 侧有这个名字」。
	"origin_stage",
	"token_band",
	"client_forwarded_for",
}

// canonicalSessionOnlyDDL renders the **session-only (two-arm)** canonical view.
//
// 2026-10-20（迁移 885）：v1 族（`_request_logs*`）整体退役 ⇒ canonical 视图的
// **第三臂**（冻结 v1 体 + 两条反连接去重腿）随之退役。留下的两臂是
//
//	session_turns_hot t LEFT JOIN session_turn_details_hot d
//	UNION ALL
//	session_turns   t LEFT JOIN session_turn_details   d
//
// ⚠ 两臂的投影文本与 710/815/816/817/824 装出来的**逐字相同** ⇒ 118 列同名同序
// ⇒ 约 40 个读方零改动。
//
// ⚠ 为什么删掉的是「臂」而不是「列」：v1 臂贡献的那些列（customer_id /
// request_class / due_at / system_fingerprint / raw_model_name /
// credits_rate_multiplier / client_ip / origin_stage / token_band /
// client_forwarded_for）在会话腿**全部自带**（要么直映、要么显式 NULL 补位）
// ⇒ 去掉整条臂不会少一列，也不会改变任何一列的取值形状。
//
// ⚠ 本函数**不再**接受 baseHasFP/baseHasRaw/baseHasCredits/baseHasCIP 与
// middleCols：它们只服务于 v1 臂（v1 侧 lateral 追加 / 中层包装链列集）。
// 留着它们当"不用也能过"的参数，等于允许调用方以为形状还能调 —— 所以
// 签名直接改掉，让每个调用点都必须重新面对「两臂」这个事实。
func canonicalSessionOnlyDDL(hasDetails bool) string {
	names := canonicalColumnOrderV2
	proj := sessionFamilyProjectionFor(names, hasDetails)
	hotJoin := ""
	parentJoin := ""
	if hasDetails {
		hotJoin = `
	LEFT JOIN public.session_turn_details_hot d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
		parentJoin = `
	LEFT JOIN public.session_turn_details d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
	}
	return fmt.Sprintf(`
	CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
	SELECT %s
	FROM public.session_turns_hot t%s
	UNION ALL
	SELECT %s
	FROM public.session_turns t%s`,
		proj, hotJoin,
		proj, parentJoin)
}

// SessionFamilyTurnsSourceSQL returns a parenthesized FROM-source emitting the
// frozen 113-column request-logs shape straight from the session-turn family
// (hot ∪ parent, details-joined per 734), for S3 wave-1 native readers
// (plan §4-S3: admin 日志读端分波去视图化). It reuses
// sessionFamilyProjection(true) so the view ensure chain and native readers
// share one copy of the column-mapping contract.
//
// Callers require migration 733 (session_turn_details family) to be applied —
// native readers run post-migration by construction (startup migrations
// precede serving).
//
// The returned source carries no alias — callers append one (e.g. `rl`).
// Unlike the 710 view it omits the frozen-v1 branch and anti-join entirely:
// rows promoted out of _request_logs history are NOT covered, so native mode
// is only correct while _request_logs still holds every not-yet-retired row
// (i.e. before S4 stop-write + TTL retirement of windows that predate the
// session mirror).
func SessionFamilyTurnsSourceSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)"
}

// SessionFamilyTurnsHotSourceSQL is the hot-only leg of
// SessionFamilyTurnsSourceSQL. Use for short dashboard windows (≤ ~7d hot
// retention) so parent-partition scans and concurrent MV REFRESH do not
// push /api/logs/top-models over its 10s budget (2026-10-15 生产 500 实证).
func SessionFamilyTurnsHotSourceSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)"
}

// SessionFamilyTurnsForSessionSQL returns the same frozen-column source as
// SessionFamilyTurnsSourceSQL, but with the session predicate **pushed inside**
// both legs so it can use idx_session_turns_session (session_id, turn_no DESC).
//
// Why this exists (measured, not assumed): in SessionFamilyTurnsSourceSQL the
// projected `gw_session_id` is the expression
// `(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)`,
// so a caller filtering on the projected name cannot use that index. The
// view-based equivalent pays for the v1 branch's NOT EXISTS anti-join as well.
// Same session, same 21 rows, local EXPLAIN ANALYZE:
//
//	view  (request_logs_with_current_month): Execution 188.5ms, 10204 buffers
//	native (this helper):                     Execution   0.585ms, ~20 buffers
//
// That is why the S3 wave-1 helper must NOT be reused for session-scoped
// readers: it is tuned for ts-window log queries, not per-session lookups.
//
// Contract for callers:
//   - The session id MUST already be bound at $1; the predicate is emitted as
//     `t.session_id = $1` on both legs, so no outer gw_session_id filter is
//     needed (and adding one would re-introduce the expression filter).
//   - The projected `gw_session_id` keeps its NULL-for-`sys:` shape, so rows
//     are byte-identical to the view's session branch.
//   - Callers needing tenant scoping add their own `WHERE rl.tenant_id = $2`
//     or equivalent after the alias.
//
// The returned source carries no alias — callers append one (e.g. `rl`).
func SessionFamilyTurnsForSessionSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = $1" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = $1)"
}

// SessionFamilyBodiesSourceSQL returns a FROM source over the session-family
// bodies tables that exposes the **v1 column names** (`request_body` /
// `response_body`), so a caller can swap the relation name without touching
// its projection list.
//
// # 为什么必须有它（而不是「直接 JOIN session_bodies」）
//
// 两侧的**列名不同**（实测 information_schema，2026-10-05）：
//
//	v1   _request_logs_bodies : request_id text NOT NULL, request_body jsonb, response_body jsonb
//	会话  session_bodies      : request_id text NOT NULL, request_delta jsonb, response_delta jsonb
//
// 语义相同（都是 JSONB），**名字不同**。所以「把 LEFT JOIN
// request_logs_bodies_with_current_month 换成 session_bodies」这种机械替换
// 会让调用点的 `COALESCE(rb.request_body, '{}'::jsonb)` 直接 42703。
// ⇒ 映射必须落在**这一层**，而不是散在每个调用点的 SELECT 里。
//
// 类型逐列核对过：两侧 bodies 列**都是 jsonb**，`request_id` **都是 text NOT NULL**
// ⇒ 纯改名，**不需要 cast**。（若将来一侧变成 text，这里必须加 `::jsonb`。
// 由 admin 侧的列名/类型对等门守着，见 admin/session_bodies_source_test.go。）
//
// # hot ∪ parent 两条腿，理由与 SessionFamilyTurnsSourceSQL 相同
//
// `session_bodies_hot`（近期热窗）与 `session_bodies`（月度分区父表）是两张表，
// 只读其一会漏掉另一侧的行。实测本地：hot 2,651 / parent 1,779,621，
// **两侧 request_id 交集为 0**（promote 会排空 hot），所以 UNION ALL 不会重复。
// ⚠ 但 `session_bodies` 父表内部有 57 行重复 request_id（37 个 id）——
// 实测**这 37 个都不在 session_turns 里**，所以今天不会让导出多出重复消息。
// 那个「今天不会」不是保证，由 admin 侧「bodies 源不得让 turn 重复」的门钉住。
//
// The returned source carries no alias — callers append one (e.g. `rb`).
func SessionFamilyBodiesSourceSQL() string {
	return "(SELECT " + sessionBodiesSourceProjection +
		" FROM public.session_bodies_hot sb" +
		" UNION ALL SELECT " + sessionBodiesSourceProjection +
		" FROM public.session_bodies sb)"
}

// sessionBodiesSourceProjection 与 SessionFamilyBodiesSourceSQL 共用同一份字面量，
// 拆出来是为了让**列集合成为可被引用的合同**（§9.231）。
//
// # 为什么要把列集合变成合同，而不是只写在注释里
//
// 这个源**不投影 `ts`**。于是「按 request_id 单键 JOIN」的读方可以直接换，
// 而 `ON rb.request_id = rl.request_id AND rb.ts = rl.ts` 的读方换过去会
// **解析期就报错**（42703）—— 那是好的，属响亮失败。
// 但「响亮」不等于「已登记」：审计 §9.231 实测 25 个 v1 bodies 读方里有
// 3 个是 ts 等值 JOIN（`admin/body_resolver.go`、`admin/compression_sessions.go`、
// `cmd/compression-bench/main.go`），它们**不能**用这个 helper。
// ⇒ 列集合必须能被别的包读到，才能让那道门按列名核对而不是按印象核对。
//
// ⚠ 别在这里加 `ts` 来「让大家都能用」：ts 在两侧的**相等率极低**
// （`admin/session_bodies_batch.go` 的文件头实测 99.85% 不等），
// 加上它会把一个解析期错误换成一个**静默的行数变化**。响亮失败优于静默改行。
var sessionBodiesSourceProjection = "sb.request_id, sb.request_delta AS request_body, " +
	"sb.response_delta AS response_body, sb.outbound_body"

// SessionBodiesHasOutboundPredicate returns "this turn really carried an
// outbound body" for a column alias fed by SessionFamilyBodiesSourceSQL.
//
// # 为什么不能只写 `outbound_body IS NOT NULL`（真库实测，2026-10-14）
//
// 会话族与 v1 对「没有这个正文」用了**两种不同的表达**：
//
//	v1 `_request_logs_bodies*`	无正文 = SQL NULL（实测 outbound_body /
//	                             request_body 的 jsonb-null 各 **0** 行）
//	会话族 `session_bodies*`	    无正文 = jsonb `null`（实测 outbound_body
//	                             11,420 行、request_delta 1,882 行，
//	                             **SQL NULL 各 0 行**）
//
// ⇒ 对会话族写 `IS NOT NULL` 是**恒真**：20,643 行里 20,643 行通过，
// 判据等于没写。而它的后果不是"少算"，是**多算**——实测同一张压缩统计表
// （2026-10-12）v1 口径 5,405、会话族 `IS NOT NULL` 口径 9,129（**多 69%**），
// 而多出来的那些轮次在 turns 行里 `outbound_msg_count` 是 NULL，
// 独立证人否定了它们。
//
// # 加上 `<> 'null'::jsonb` 之后才是等价替换（同一张表实测）
//
//	v1 口径				5,405
//	会话族忠实口径			4,204
//	并集（跨族 OR）			**5,405** ← 与 v1 逐字相同
//	（会话族 0 行是 v1 没有的；v1 多出的 1,201 行是 jsonb-null 行）
//
// ⇒ 跨族 OR 在**停写前的历史日**上逐字复现 v1 基线，同时补回停写后的流量。
// 这正是"确保数据在更改前后一致"能被验证的形态：不是"新数看起来差不多"，
// 是历史日的数字**完全相等**。
//
// ⚠ 同一条陷阱适用于 `request_body`（投影名 request_delta）：会话族的
// "无请求体"同样是 jsonb null，用 `IS NOT NULL` 会把 1,892 行算成有正文。
func SessionBodiesHasOutboundPredicate(alias string) string {
	return alias + `.outbound_body IS NOT NULL AND ` + alias + `.outbound_body <> 'null'::jsonb`
}

// SessionBodiesSourceSQL returns the aliased bodies FROM-source for **any
// package**, gated by the shared read-side switch.
//
// # 为什么它必须在 db 而不是 admin（§9.233）
//
// §9.232 把 7 个 bodies 读方改走 `admin.sessionBodiesFromSQL()`，但另外 4 个
// A 类读方在 `domains/sessionsummary`、`domains/sessionforensics`、`bg` ——
// 它们**看不见** admin 包里的未导出函数，于是「能换但换不了」。
//
// 而 db 才是同族 helper 的所在地：`SessionFamilyTurnsSourceSQL`、
// `SessionFamilyTurnsForSessionSQL`、`SessionFamilyBodiesSourceSQL` 都在这里。
// ⇒ 切换层与被切换的源放在一起，是唯一说得通的位置。
//
// # 无 import 环（已核实 2026-10-05）
//
// db 此前**不** import settings，settings 也**不** import db ⇒ 本次引入
// `settings` 依赖不成环。若将来 settings 反向依赖 db，这里会立刻编译失败——
// 那正是应该失败的时候（不要为了让它编过而在这里复制一份配置读取）。
//
// # 与 admin 那份的关系：那份**已被删掉**（§9.233）
//
// 第一版让它留在 admin 并**委托**给本函数。实测那层薄包装是**有害**的：
// 门与 v1 关系名字面量之间又隔了一层，而 `indirectSourceConsumers`
// 是按「谁调用了切换层」识别的 —— 一个只做 `+ " rb"` 的包装函数会让
// **9 个 admin 消费点在一次全仓解析里全部消失**（实测 5 个 vs 应有的 14 个）。
// ⇒ 删除包装，全部调用点直接调 `dbpkg.SessionBodiesSourceSQL()`。
// 代价是 5 个文件要新增 db import，收益是**单一切换层、单一探测路径**。
// # ★ 2026-10-20（迁移 885，v1 族最终删除）：本切换层**退化为 no-op**。
//
// 原实现：开关 `storage.session_bodies_native_read` 关（默认）时返回
// `request_logs_bodies_with_current_month`（**v1 正文族的视图**），开时返回
// 会话族源。那是灰度期的回滚设计：开关关 = 读 v1，开 = 读会话族。
//
// 885 删掉了 v1 族，**连同那张视图** ⇒ 默认臂指向一个**已不存在的关系**
// ⇒ 任何走默认臂的读方都是**整条语句 42P01**（不是那条腿失败，也不是静默空集）。
// 实测受影响的消费点有 12 个（admin 的 6 个文件 + bg 1 + domains 3 …）。
//
// ⇒ 开关的**灰度期已经结束**：v1 臂不再有对象，「关」这个选项没有意义。
//
//	本函数恒返回会话族源。
//
// # 为什么保留这个函数（而不是让所有调用点直接调 SessionFamilyBodiesSourceSQL）
//
// 12 个调用点分散在 6 个包里，其中 4 个包（domains/sessionsummary、
// domains/sessionforensics、bg、admin）不 import db 之外的切换层约定；
// 让它们各自改写会引入「有人改了、有人没改」的分叉——而分叉的代价是
// **一部分读方 42P01、一部分正常**，那种故障形态最难定位。
// ⇒ 保留**单一入口**，让它恒返回唯一正确的形态。
func SessionBodiesSourceSQL() string {
	return SessionFamilyBodiesSourceSQL()
}

// SessionBodiesNativeReadSetting 是 bodies 读端灰度开关的 key。
//
// 名字**以源命名、不以界面命名**：§9.230 它只服务会话导出/对比，
// §9.232 起服务 9 个消费点，§9.233 起还有跨包的。
// 若继续叫 `admin_session_bodies_native_read`，下一个读方会以为
// 「非 admin 的读方就不受这个开关管」——而事实正相反。
const SessionBodiesNativeReadSetting = "storage.session_bodies_native_read"

// SessionFamilyBodiesSourceColumns 是 SessionFamilyBodiesSourceSQL 对外暴露的
// **全部**列名（已去掉 `sb.` 前缀与 `AS` 别名）。
//
// 它是 §9.231 那道门的判据来源。调用方（admin 包）用它在**源码文本**里核对
// 「这个读方用到的 bodies 列是否都在合同内」—— 不能靠数投影个数，
// 也不能靠「helper 里有这个字符串」。
var SessionFamilyBodiesSourceColumns = []string{
	"request_id", "request_body", "response_body", "outbound_body",
}

// MirrorDriftClassSQL classifies a V1 _request_logs row into the three buckets
// the session mirror hook cares about. Kept as one expression so the aggregate
// and the breakdown cannot disagree — and so every consumer that needs to
// exclude "the hook would not have mirrored this" agrees on the definition.
//
// The two exclusion arms are a statement-by-statement transcription of the
// Go gates in internal/sessionv2mirror/hook.go — NOT an independent guess at
// "what looks internal". Divergence is not cosmetic: a row the hook
// deliberately skipped but this expression calls genuine_loss keeps s4_ready
// false forever and blocks the cutover that is actually safe.
//
//	internal_loopback ← hook.go:102 `!synthetic && IsInternalAutoEntry(entry)`
//	    IsInternalAutoEntry (telemetry/internal_loopback.go:23-39):
//	      1. requires is_auto_request IS TRUE (a NULL is NOT internal);
//	      2. request_type  ∈ {title_gen, summary};
//	      3. origin_actor  ∈ {auto-title-generator, auto-summary-generator,
//	                          session-summary};
//	      4. otherwise task_type IS NULL/'' (taskless auto entry).
//	    work_type is deliberately NOT an arm: the Go gate never reads it, so
//	    keying on it would mask real loss (is_auto_request=TRUE + task_type
//	    set + work_type='session_title' is a business turn the hook mirrors).
//	non_terminal ← hook.go:71 `!entry.Success && !isTerminalFailure(entry)`
//	    isTerminalFailure (hook.go:991-1002) is true when request_status ∈
//	    {failure, rate_limited} OR error_kind is non-empty; the row is
//	    mirrored when Success is true. Negating both yields the arm below.
//
// SSOT: this used to live as an unexported const in package main
// (cmd/gateway/dual_read_validator.go), which made it unreachable from admin.
// Duplicating it there would have put a 10-line classification under two
// definitions with no compiler link between them; the existing parity test
// (cmd/gateway/dual_read_class_parity_test.go) only guards the main copy.
//
// The fragment hardcodes the table alias `rl` — callers must alias their
// source that way. `... <> 'genuine_loss'` is the exclusion form.
const MirrorDriftClassSQL = `
CASE
  WHEN COALESCE(rl.is_auto_request, false)
       AND (   TRIM(COALESCE(rl.request_type, '')) IN (` + internaltraffic.GeneratorRequestTypesSQLList + `)
            OR TRIM(COALESCE(rl.origin_actor, '')) IN (` + internaltraffic.GeneratorActorsSQLList + `)
            OR TRIM(COALESCE(rl.task_type, '')) = '')            THEN 'internal_loopback'
  WHEN NOT COALESCE(rl.success, false)
       AND TRIM(COALESCE(rl.request_status, '')) NOT IN ('failure', 'rate_limited')
       AND TRIM(COALESCE(rl.error_kind, '')) = ''                 THEN 'non_terminal'
  ELSE 'genuine_loss'
END`
