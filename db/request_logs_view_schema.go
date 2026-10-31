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
// ★ 2026-10-20（迁移 890）：v1 族（`_request_logs*`）整体退役 ⇒ 第三臂
// （冻结 v1 体 + 两条反连接去重腿）与它依赖的两张中间层包装视图
// （…_without_customer_id / …_without_request_class_due_at）**一并消失**。
// 剩下的两臂（session_turns_hot ∪ session_turns，734 details 特征层
// LEFT JOIN）的投影文本与 710/815/816/817/824 装出来的逐字相同 ⇒ 118 列同名
// 同序 ⇒ 读方零改动。
//
// ⚠ 自愈**不得再触碰 v1 族**：那些表已被 890 删除，包装视图也没了 ⇒ 任何
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
	// v1 族已随 890 退役 ⇒ 此时不做任何 DDL，把问题留给启动序列报错，
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
			"(890) — no fallback body to install; leaving the view as-is")
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

// SessionFamilyAPIKeyIDExpr 是 api_key_id 的**唯一**规范表达式。
//
// # 为什么它必须是常量而不是各调用点各写一份
//
// 原始列 `session_turns.api_key_id` 是 **text**（2026-10-24 生产实测，
// hot 与父表都是 text），而**过滤参数**是 int64 ⇒ 两侧必须先归一。
// 归一规则 = 「纯数字才转 bigint，否则 NULL」（`NULL` 永远不等于任何 int）。
//
// ★ 这条规则**必须与投影逐字一致**：投影决定「会话列表里展示的 api_key 是哪个值」，
// 而过滤决定「哪些会话出现在结果里」⇒ 两边规则不同 ⇒ 展示与过滤会对不上，
// ★ 而那正是「用户按某个 key 过滤却看不到自己的会话」这类现象的来源。
// ⇒ 故投影与过滤**共用同一个常量**，任何一侧改动都会被
// admin/turns_sessions_filter_pushdown_test.go 的单一真值源门抓到。
//
// ★ 形态选择的实测依据（252 生产只读，2026-10-24）：
//
//	is_null 745,743 / pure_digits 362,434 / non_numeric 0 / 前导零或补白 0
//
// ⇒ 今天所有非空值都是**纯数字** ⇒ `api_key_id = $n::text` 在**今天**等价；
// ★ 但它对未来的 `'0105'` 会与投影分叉（投影给 105，文本比较给 '0105'）
// ⇒ ⇒ 保留 CASE 形式 ⇒ 对**任意**输入都与投影逐字一致。
const SessionFamilyAPIKeyIDExpr = "(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)"

// SessionFamilyAPIKeyIDNumericPredExpr 是「api_key_id 归一后非空」的**裸正则**谓词。
//
// # ★★ 为什么必须是「裸正则」而不是 CASE 形式（R94，2026-10-24 生产实测）
//
// 【两种写法逐情形等价】对 `(CASE WHEN c ~ '^[0-9]+$' THEN c::bigint END) IS NOT NULL`
//
//	c = NULL  ⇒ CASE 得 NULL ⇒ 排除；裸正则得 NULL ⇒ 排除  ⇒ 同
//	c 非数字  ⇒ CASE 得 NULL ⇒ 排除；裸正则得 false ⇒ 排除 ⇒ 同
//	c = '18'  ⇒ CASE 得 18   ⇒ 保留；裸正则得 true  ⇒ 保留  ⇒ 同
//	（唯一差别：超 int64 的纯数字串 —— CASE 形式会在**WHERE 求值**时报 22003，
//	  裸正则形式只在**入选行的 SELECT 列表**求值时才报 ⇒ 后者**更宽松**，不是回归）
//
// ⇒ 生产真实非空批次逐行对拍（252，同一事务内同一快照）：
//     old_rows=20 / new_rows=20 / only_new=0 / only_old=0  ⇒ 逐行一致 ✓
//
// 【但只有裸正则能被规划器认出来】★ PG 的「谓词蕴含」判定是**语法的**
//   ⇒ 它无法证明「CASE 形式蕴含 `api_key_id ~ '^[0-9]+$'`」
//   ⇒ 写在 CASE 里的过滤条件**用不上**以该正则谓词建的部分索引。
//
// ★ 实测（252 生产，事务内建索引 + EXPLAIN ANALYZE + ROLLBACK ⇒ 零残留）：
//
//	形态                                          探针批次执行时间
//	CASE 形式过滤（无部分索引）                        1,350.8 ms   ← Rows Removed by Filter: 82,297
//	裸正则过滤（无部分索引）                            1,569.3 ms   ← 同上（等价但同样慢）
//	★ 裸正则过滤 + 部分索引 WHERE c ~ '^[0-9]+$'        **4.57 ms**  ← 295×
//
// 【为什么 CASE 形式那么慢】★ 探针会话（`sys:probe:*`）的 api_key_id
//   **全是 NULL**（252 实测 462,394 行全空）⇒ 过滤条件**无法从索引里判定**
//   ⇒ 必须回表 ⇒ 探针批次扫了 **82,297 行**堆，**全部被过滤掉**，产出 **0 行**。
//   ⇒ ★ 部分索引只含「api_key_id 是数字」的行 ⇒ 探针流量在索引里**一行都没有**
//   ⇒ 规划器走 **Index Only Scan** ⇒ 不用回表 ⇒ 4.57 ms。
//
// ★★ **语义耦合**：迁移 906 的部分索引谓词必须与本常量**逐字一致**
//   （由 migration_906_turns_api_key_numeric_idx_test.go 钉住）。
const SessionFamilyAPIKeyIDNumericPredExpr = "t.api_key_id ~ '^[0-9]+$'"

// SessionFamilyLatestTurnPerSessionSQL 返回「每个 (tenant_id, session_id) 最新一轮」的 source，
// 供会话列表页回填 api_key / handoff 父会话使用。
//
// # ★ 为什么要这个形态（2026-10-24 R93，生产只读实测）
//
// 【旧形态的代价】会话列表页的 api_key 回填曾是：
//
//	FROM public.session_turns_with_current_month t          -- 视图
//	JOIN <hot ∪ 全部历史, 113 列投影> rl ON rl.request_id = t.request_id
//
// ★ 而 `session_turns_with_current_month` 的**名字骗人**：它的父表腿实测是
// `FROM session_turns`（**全部分区**）⇒ 视图 = `hot ∪ 全部历史`
// ⇒ ⇒ **同一批行被扫了两遍**（视图侧 `Heap Fetches 86,507` ＋ source 侧同样 86,507 行）
// ⇒ ⇒ 再按 `request_id` **1:1 join**（**join 本身冗余** —— 同一行、同一列）。
//
// 【为什么只有 `?model=` 慢】它命中的 21 个**探针会话**在 2026_09 有 **85,712 轮**
// ⇒ 实测该 enrich 查询 **13,066 ms**；而无过滤批次只有 21×1 轮 ⇒ **23 ms**
// ⇒ ⇒ **差 570×** ⇒ 是「这批会话有多少轮」，不是「形状」。
//
// 【本形态】每个 source 一份「先聚合出该会话的最大 turn_no，再回定位那一行」：
//
//	(SELECT l.session_id, l.turn_no, <api_key 归一> AS api_key_id
//	   FROM (SELECT session_id, max(turn_no) AS turn_no FROM <src> t
//	          WHERE t.session_id = ANY($1) AND <api_key 归一> IS NOT NULL <extraPred>
//	          GROUP BY session_id) l
//	   JOIN <src> t ON t.session_id = l.session_id AND t.turn_no = l.turn_no <extraPred>)
//
// ★ **为什么不用 `DISTINCT ON`**：写在**分区父表**上时，规划器给的是
// **Merge Append ⇒ 全量 86,507 行**再 Sort+Unique（实测 **9,900 ms**）
// ⇒ ⇒ PG **不会把 DISTINCT ON 下推到分区** ⇒ 裁剪不生效。
// ⇒ 而 `GROUP BY session_id + max(turn_no)` 走 **index-only scan**（不回表）⇒ 只读索引页。
//
// 【生产实测（252，只读，2026-10-24）】
//
//	批次                      旧形态      本形态
//	新鲜会话（21 行）           23.2 ms     96.0 ms   ← ★ 慢 4×（绝对值 <100 ms）
//	当前月多轮（6 行）         115.0 ms     54.1 ms
//	★ 探针 85,712 轮          13,066 ms   1,339 ms   ← 9.8×
//
// 【口径等价】旧口径 =「视图里、且 api_key 归一后非空的轮次中 turn_no 最大那一轮」的 api_key；
// 本形态 =「hot ∪ 全部历史里、同样条件的最新一轮」的 api_key。
// ★ 两者**相同**：(a) 视图 = hot ∪ 全部历史 ⇒ 候选轮次集合相同；
// (b) 视图 hot 腿的 anti-join 只删「hot 与父表同时存在」的重复行 ⇒ 同一 request_id 的 api_key 相同；
// (c) 本形态在**每个 source 内**都带 `IS NOT NULL` 过滤 ⇒ 与旧口径的过滤位置等价
// （★ 漏掉它会让口径漂移：实测候选形态出现 `only_new=20 / only_old=0`）。
// ⇒ 生产三批次双向 EXCEPT ALL：新鲜会话 `0/0/21=21`、当前月多轮 `0/0/6=6`、探针批次 `0/0/0=0`。
//
// # 参数
//
//   - extraPred：额外的行级谓词（如租户收窄 `AND t.tenant_id = $2`），
//     **必须使用别名 `t`**（两处都会用：聚合子查询与回定位 JOIN）。
//     ★ 它会同时施加在「聚合」与「回定位」两步上 ⇒ 与旧形态「租户过滤只加在 source 侧」等价。
//
// # 占位符
//
//   - `$1` 必须是 session_id 数组（text[]）⇒ 与旧形态一致。
func SessionFamilyLatestTurnPerSessionSQL(extraPred string) string {
	leg := func(src string) string {
		return "(SELECT l.session_id, l.turn_no, " + SessionFamilyAPIKeyIDExpr + " AS api_key_id " +
			"FROM (SELECT session_id, max(turn_no) AS turn_no FROM " + src + " t " +
			"WHERE t.session_id = ANY($1) AND " + SessionFamilyAPIKeyIDNumericPredExpr + extraPred + " " +
			"GROUP BY session_id) l " +
			"JOIN " + src + " t ON t.session_id = l.session_id AND t.turn_no = l.turn_no" + extraPred + ")"
	}
	return "(" + leg("public.session_turns_hot") + " UNION ALL " + leg("public.session_turns") + ")"
}

var projectionExprsV2 = []string{
	"NULL::bigint",
	"t.request_id",
	"t.ts",
	"t.tenant_id::text",
	"(CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END)",
	SessionFamilyAPIKeyIDExpr,
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
	// tools —— 2026-10-20 起**有值了**（审计 G-6，迁移 889）。
	//
	// 事实校准：这一列此前**从未被写过**（456 建列，列默认值 '[]'::jsonb），
	// turn_writer 的 INSERT 列清单里没有它 ⇒ 每行都停在 '[]' ⇒ 本投影透出
	// 的 tool_calls 恒为空数组（不是「该请求没有工具调用」，是「没人写过」）。
	// 现由 domains/session/v2/turn_writer.go 的 turnToolsJSONBExpr 在
	// INSERT 的同一条语句里用子查询从 public.session_tools_hot 聚合
	//（按 session_id + request_id），元素是
	// {tool_name, tool_call_id, status, duration_ms, error_type} 的摘要。
	//
	// ⚠ **历史行仍是 '[]'**：889 不回填（工具行可能已被 promote 搬走）。
	// ⇒ 读这一列时不能假设「老行也是空数组」——新行有值、老行没有；
	//   也不能假设「有值就是完整工具记录」——那是摘要，不是正文。
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

// canonicalSessionOnlyComment 与迁移 890 的 COMMENT ON VIEW 同文（COMMENT 的
// IS 只收单个字面量，不能像 SQL 赋值那样 || 拼接）。824 起补记 request_status
// 的分类口径（816/817 的两次 client_ip 改写当时未同步本常量，视图注释一度落后
// 两代；这里补的是最新状态，不是逐版流水）。890 起本常量描述**两臂**形态。
const canonicalSessionOnlyComment = `'会话存储解耦 v4（890）: v1 族（_request_logs*）整体退役 ⇒ canonical 视图去掉第三臂（冻结 v1 体 + 双反连接去重），只剩两臂 session_turns_hot ∪ session_turns。两臂投影与 710/734/813/815/816/817/824 装出来的逐字相同 ⇒ 118 列同名同序 ⇒ 读方零改动。仍 NULL：id（v1 请求行 id ≠ session turn id）/test_col/test_tab_indent/provider_model/credits_rate_multiplier，以及 client_ip 本身（字面量非法时落 NULL，不抛错）。trace_events 刻意未投影（镜像从不写，近窗非空率 0，投影即净数据损失）。'`

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
// 2026-10-20（迁移 890）：v1 族（`_request_logs*`）整体退役 ⇒ canonical 视图的
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

// SessionFamilyCredentialIDExpr 是 credential_id 的**唯一**规范表达式。
//
// 它与 canonical 视图投影里的那一项**逐字相同**
// （projectionExprsV2 的第 9 项，见本文件上方），★ 因此：
//
//   - 查询侧写这个表达式 ⇒ 与视图侧的 rl.credential_id 语义相同；
//   - 与迁移 729 的表达式索引
//     `((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC)`
//     **同形** ⇒ 规划器能认出来（★ PG 用 equal() 比较表达式 ⇒ 别名 t 不影响匹配）。
const SessionFamilyCredentialIDExpr = "(CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END)"

// SessionFamilyCredentialCountSourceSQL 返回「**按凭据统计行数**」的
// **窄投影两臂** source（hot ∪ 母表，★ **不带 details 腿**）。
//
// # 为什么是「窄投影」而不是 SessionFamilyTurnsSourceSQL()
//
// canonical 视图 / 原生 118 列 source 都会让每一行**物化 118 列**
// （252 实测 plan 里的 `width=3083` 字节/行）。★ 而「统计行数」只需要
// credential_id 一列 ⇒ ⇒ 窄投影把 heap scan 的每行物化宽度压到 32 字节。
//
// # 252 生产实测（2026-10-27，只读，单凭据 count）
//
//	形态                                    Execution Time
//	视图（118 列，现状）                    14,728.010 ms
//	原生 118 列 source                        389.618 ms
//	★ 本 helper（窄投影 UNION ALL）             247.063 ms
//	拆成两条独立 count（各自子查询）           statement_timeout（★ 最差）
//
// ★★ 两版的 plan **cost 逐字相同**（cost=41.94..441.93 rows=391）
// ⇒ ⇒ **plan cost 里没有「物化宽度」** ⇒ ★★ 「EXPLAIN 一样」不蕴含「代价一样」。
//
// # 口径等价性
//
//   - 视图的 credential_id 列 = 本表达式 ⇒ 谓词**逐字相同**；
//   - ★ 视图的 details 腿是 LEFT JOIN 且 details 唯一键是
//     (tenant_id, request_id, partition_date) ⇒ **同一键最多 1 行**
//     ⇒ ⇒ LEFT JOIN **不改变行数** ⇒ ⇒ 「带 details 的视图」与
//     「不带 details 的两臂」**行数相同** ⇒ ⇒ 计数等价。
//
// # 参数
//
//   - credExpr：凭据 id 的 SQL 表达式（如 `c.id`）。★ 它会被拼进两处谓词。
func SessionFamilyCredentialCountSourceSQL(credExpr string) string {
	leg := func(src string) string {
		return "(SELECT t.credential_id FROM " + src + " t WHERE " + SessionFamilyCredentialIDExpr + " = " + credExpr + ")"
	}
	return "(" + leg("public.session_turns_hot") + " UNION ALL " + leg("public.session_turns") + ")"
}

// SessionFamilyMostUsedClientModelSourceSQL 返回「**most-used client_model**」
// 查询用的**窄投影** source（7 列；两臂 hot ∪ 母表，各带 details LEFT JOIN 腿）。
//
// # 用途
//
// bg/shared_pick.go 的探针选型（PickProbeModelForCredential 的 Priority 1）
// 要回答「这个凭据近 7 天用得最多的 client_model 是哪个」。现状读 canonical
// 视图 request_logs_with_current_month。
//
// # ★★ 它**不是**提速手段（2026-10-27 252 生产实测推翻了一个错误前提）
//
// ★ **错误前提**：「视图有 118 列 ⇒ 每行物化 118 列 ⇒ 改窄投影能省十几秒」。
//   —— **不成立**：PostgreSQL 会**内联**视图并**裁剪**未被引用的列
//   （remove_unused_subquery_outputs）⇒ 只取 1 列时，视图并不物化 118 列。
//
// ★ **实测（三形态同快照、同一会话、同一凭据 7 天窗）**：
//
//	形态                plan cost                Planning Time
//	canonical 视图      2026.88..2027.14         25 ~ 91 ms
//	★ 本 helper          2025.29..2025.55         8 ~ 13 ms
//
//   ⇒ ★★ **计划代价几乎相同**（2026.88 vs 2025.29）⇒ **执行阶段没有可省的量**；
//   ⇒ ★★ 真正的差别在**规划**：视图读面每次都要展开 118 列的定义，
//     规划成本是窄投影的 **3~11 倍**（CPU 成本，与 IO 噪声无关，每次调用都付）。
//
// ★ **为什么不能拿单次执行耗时当证据**：252 上**九个**执行（3 形态 × 3 轮交错）
//   的**计划形状逐项相同**（同一访问路径、同一行数、同一 buffer 计数
//   521,376 hit / 0 read），但 Execution Time 在 **368 ms ~ 16,194 ms** 之间
//   波动（**44×**）⇒ ⇒ 单次采样分不出形态差异，只能比**计划代价 / 形状 /
//   buffer 计数**这类确定性量。
//
// # 口径等价性（252 生产规模，同快照逐值对拍）
//
//	view_groups=9 / narrow_groups=9 / only_in_view=0 / only_in_narrow=0
//	view_total=14317 = narrow_total
//	top1_view=minimax-m3|6862 = top1_narrow
//
//   ⇒ 分组**多重集**逐值相同。（★ 比全部分组而不是只比 top-1 ——
//     `ORDER BY count(*) DESC LIMIT 1` 在并列时**本身不确定**。）
//
// # 为什么保留 `::text` 转型（免费，但更稳）
//
//   PostgreSQL 里 `text` 相等是**字节相等**，而 `varchar` 相等走**排序规则**
//   （确定性 ICU 排序规则下可能把规范等价的两种写法判相等）。
//   ⇒ `d.tenant_id::text = t.tenant_id::text` 是**字节精确**的，
//     与 canonical 视图的 join 谓词**逐字相同** ⇒ 不引入任何新的相等语义。
//   ⇒ 而去掉转型**不省任何量**：实测两形态计划代价相同（2025.29 vs 2025.29）、
//     buffer 计数相同 ⇒ ★ 转型是**免费的**，保守优先。
//
//   ★★ 订正本轮早先的一个**错误前提**：「PG 里 varchar 比较忽略尾空格、text 不忽略」
//     —— **不成立**：252 实测 `'a'::varchar(255) = 'a '::varchar(255)` 是 **false**
//     ⇒ varchar 也**不**忽略尾空格（只有 `char(n)` 忽略）⇒
//     ⇒ 保留转型**不是**为了「补 varchar 的漏」，而是为了「字节精确 + 与视图逐字相同」。
//
// # 投影的 7 列与「视图里那几列的来源」逐字对齐：
//
//	client_model  ← d.client_model    （details 腿）
//	quality_flags ← d.quality_flags   （details 腿）
//	ts / success / task_type / origin_actor ← t.*（turns 臂）
//	credential_id ← SessionFamilyCredentialIDExpr（与视图投影逐字相同）
//
// ★ join 键与视图逐字相同：`d.tenant_id::text = t.tenant_id::text` +
//   request_id + partition_date。
//
// ★ 探针排除谓词需要的三列（quality_flags / task_type / origin_actor）
//   **必须**在本投影里 —— 少一列就是 42703（仓内 R50 记录过同族事故）。
//
// ★ 返回值**不带别名**（调用方自己加），与 SessionFamilyTurnsSourceSQL 同约定。
func SessionFamilyMostUsedClientModelSourceSQL() string {
	leg := func(turns, details string) string {
		return "(SELECT d.client_model, d.quality_flags, t.success, t.ts, t.task_type, t.origin_actor, " +
			SessionFamilyCredentialIDExpr + " AS credential_id" +
			" FROM " + turns + " t" +
			" LEFT JOIN " + details + " d" +
			" ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date)"
	}
	return "(" + leg("public.session_turns_hot", "public.session_turn_details_hot") +
		" UNION ALL " + leg("public.session_turns", "public.session_turn_details") + ")"
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

// SessionFamilyTurnsSourceWindowedSQL 是 SessionFamilyTurnsSourceSQL 的
// **带分区键下界**变体（R181 轮新增）。
//
// ★ 为什么需要它（★ 实测坐实，不是推断）：★★
// `public.session_turns` / `session_bodies` / `session_turn_details` 都按
// **partition_date**（RANGE 分区）划分，而读方几乎都只按 **ts** 过滤
// ⇒ ⇒ PG 的分区裁剪**只认分区键谓词** ⇒ ⇒ 一个 ts 窗口会命中【全部】月分区。
// ★★ 本地真库 A/B 对照（session_turns 有 5 个月分区 + default）：
//
//	WHERE ts >= '2026-10-01' AND ts <= '2026-10-29'
//	  ⇒ Append → 5 个分区全被扫（2026_07 / 2026_08 / 2026_10 / 2026_11 / default）
//	WHERE ts >= ... AND partition_date >= '2026-10-01' AND partition_date <= '2026-10-29'
//	  ⇒ Seq Scan on session_turns_2026_10   ← ★ 只剩 1 个分区 ★
//
// ★★ 生产侧同一形态已被台账记下（252 的 pg_stat_statements 观测台账
// `scripts/audit/asset/query-plan-observations.tsv`）：/api/logs 的列表语句
// （stmt-1d7be58136）在**1 秒窗口**上 Seq Scan 了 **4 个整月分区**。
//
// ★ 与 canonical 视图的**口径等价性**：★ 本函数与
//   `request_logs_with_current_month` 读的是同一对关系（hot ∪ 父表）、
//   投影走**同一个** sessionFamilyProjection(true)、JOIN 条件逐字相同
//   （★ 视图那版带 `::text` 冗余转型，本版不带 ⇒ ★ 索引可用性反而更好，
//     ★ 取值逐行相同 ⇒ 语义不变）★★
//   ⇒ ⇒ 唯一差别 = 本版多了 partition_date 谓词 ⇒ ⇒ 行集是 ts 谓词的**子集**，
//     ★ 而 partition_date == calendarDate(ts)（★ 所有写入方都用它）⇒
//     ★ ⇒ 子集 == 原行集 ⇒ ⇒ **不丢行** ★
//
// ★ 参数：★ pdFrom / pdTo 是**两臂各自**要用的 SQL 片段（形如 "$1" / "$2"），
//   ★ 由调用方决定参数编号 ⇒ ⇒ 本包不感知参数位置。
//
// ★ 返回值不带外层别名（调用方自己加，如 ` rl`）。
func SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo string) string {
	win := " WHERE t.partition_date >= " + pdFrom + " AND t.partition_date <= " + pdTo
	// ★★ 详情侧（session_turn_details）**也要**带分区键下界，否则它仍然全扫：
	//   JOIN 条件里已经有 `d.partition_date = t.partition_date`，而 t 侧被限在
	//   [pdFrom, pdTo] ⇒ ⇒ d.partition_date 也必然落在同一区间 ⇒ ⇒ 在 ON 子句里
	//   **再加一条冗余谓词**是**逐行等价**的（不是新筛选）⇒ ⇒ 但它让 PG 能在
	//   **规划期**把 details 的月分区裁掉。
	// ★★ 实测（本地真库，R181 轮）：只给 turns 侧加下界时，details 侧仍是
	//   `Seq Scan on session_turn_details_2026_10 / _2026_11 / _default`（3 个分区）；
	//   两侧都加 ⇒ ⇒ details 只剩窗口内分区。
	// ★★ 放在 ON 而不是 WHERE：★ LEFT JOIN 的 ON 子句里加条件**不改变**结果集
	//   （左行照样保留）⇒ ⇒ 语义与「不加」完全一致 ⇒ ⇒ 零风险。
	joinWin := " AND d.partition_date >= " + pdFrom + " AND d.partition_date <= " + pdTo
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" + joinWin +
		win +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" + joinWin +
		win + ")"
}

// SessionFamilyTurnsSourceLowerBoundSQL 是 SessionFamilyTurnsSourceSQL 的
// **仅带分区键下界**变体（R184 轮新增）。
//
// ★★ 为什么需要「只有下界」这一支（R181 的 Windowed 变体不够用）：★★
//
//	SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo) **强制带上界**，
//	而**相对窗口**（`ts >= NOW() - ($n || ' hours')::interval`）的语义是
//	「最近 N 小时」⇒ ⇒ 它**没有** ts 上界 ⇒ ⇒ 硬造一个 partition_date 上界
//	就是**给读点加了一个原本没有的筛选** ⇒ ⇒ 语义漂移（未来 ts 的行会被丢掉）。
//
//	⇒ ⇒ 相对窗口只能加**下界** ⇒ ⇒ 本函数只推下界，**不推上界**。
//
// ★★ 为什么下界必须是「**与 ts 谓词同源**」的表达式（而不是应用侧算好的常量）：
//
//	应用侧常量（像 R181 的 turns 列表那样算 $1/$2）要求**应用时钟 ≤ 数据库时钟**，
//	否则 `NOW() - Nh`（DB 时钟）可能比应用算出的下界更早 ⇒ ⇒ 会**丢行**。
//	⇒ ⇒ 本函数允许调用方传**任意 SQL 表达式**作为下界 ⇒ ⇒ 调用方可以传
//
//	    (NOW() - ($2 || ' hours')::interval) AT TIME ZONE 'UTC'
//
//	★ 它与 ts 谓词**逐字同源** ⇒ ⇒ 由不变式
//	「partition_date == (ts AT TIME ZONE 'UTC')::date」可**证明**：
//	凡满足 ts 谓词的行，其 partition_date 必然 >= 该表达式的日期值
//	⇒ ⇒ **下界是 ts 谓词的蕴含条件 ⇒ ⇒ 不丢任何一行** ★
//	★ 且 `now()` 是 STABLE ⇒ 语句内取值恒一 ⇒ ⇒ 与 ts 谓词里的 NOW() **同一时刻** ★
//
// ★★ PG 的裁剪时机（★ 实测，别猜）：
//
//	· 参数值在**规划期**已知（custom plan / 常量）⇒ **计划期**裁剪
//	  ⇒ Append 里**根本不含**被裁的分区；
//	· 参数值**规划期未知**（generic plan，pgx 语句缓存走的就是这条路）
//	  ⇒ **运行期**裁剪 ⇒ Append 里仍列着被裁的子计划，但报 `Subplans Removed: N`
//	  ⇒ ⇒ **两种模式下都真的不扫那些分区** ★
//
// ★ 详情侧（session_turn_details）也要带下界，否则它仍然全扫：
//   JOIN 条件里已经有 `d.partition_date = t.partition_date`，而 t 侧被限在
//   partition_date >= pdFrom ⇒ ⇒ d.partition_date 也必然 >= pdFrom
//   ⇒ ⇒ 在 ON 子句里再加一条是**逐行等价的冗余谓词**（不是新筛选）⇒ ⇒ 零风险。
//   ★ 放在 ON 而不是 WHERE：★ LEFT JOIN 的 ON 子句里加条件**不改变**结果集
//   （左行照样保留）⇒ ⇒ 语义与「不加」完全一致 ⇒ ⇒ 零风险。
//
// ★ 参数：★ pdFrom 是要拼进两臂的**SQL 片段**（可以是常量，也可以是表达式）
//   ★ 由调用方决定 ⇒ ⇒ 本包不感知参数位置。
//
// ★ 返回值不带外层别名（调用方自己加，如 ` rl`）。
func SessionFamilyTurnsSourceLowerBoundSQL(pdFrom string) string {
	if strings.TrimSpace(pdFrom) == "" {
		// ★ 空下界 ⇒ 退化成原形态（不加谓词）⇒ 宁可「没裁剪」也不要「拼出坏 SQL」。
		return SessionFamilyTurnsSourceSQL()
	}
	win := " WHERE t.partition_date >= " + pdFrom
	joinWin := " AND d.partition_date >= " + pdFrom
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" + joinWin +
		win +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" + joinWin +
		win + ")"
}

// SessionFamilyTurnsSessionExistsSQL 返回「**外层会话 s** 是否含有满足 colPred 的轮次」
// 的**两腿 EXISTS**（hot 腿 ∪ 父表腿），供会话级过滤（`?api_key_id=` / `?model=` /
// `?provider=` / `?status_code=`）使用。
//
// # 为什么不用 public.session_turns_with_current_month 视图（★ 实测，非推断）
//
// 【症状】GET /api/admin/turns/sessions 带 `?api_key_id=` / `?model=` 时
//
//	生产恒 500@30s（角色级 statement_timeout=30s ⇒ SQLSTATE 57014）。
//	252 EXPLAIN ANALYZE 单变量 A/B（2026-10-24）：
//	  A 生产形态（api_key_id）    > 90 s，被 statement_timeout 杀掉，未跑完
//	  M 生产形态（model）              36,415 ms
//
// 【根因 ①：视图的 hot 腿带反连接】
//
//	CREATE VIEW session_turns_with_current_month AS
//	  SELECT … FROM session_turns_hot hot
//	  WHERE NOT EXISTS (SELECT 1 FROM session_turns archived
//	                    WHERE archived.tenant_id = hot.tenant_id
//	                      AND archived.request_id = hot.request_id)
//	  UNION ALL SELECT … FROM session_turns
//
//	⇒ 外层每一行都要在 hot 上跑一次 Nested Loop Anti Join。
//	⇒ ★★ 而「外层每一行」在这里是**几千行**（会话列表要扫到凑够 LIMIT 21）。
//
// 【根因 ②：api_key_id 那一支还多 JOIN 了一个 113 列的全量 source】
//
//	EXISTS (SELECT 1 FROM session_turns_with_current_month ft
//	        JOIN <SessionFamilyTurnsSourceSQL()> rl ON rl.request_id = ft.request_id
//	        WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id
//	          AND rl.api_key_id = $n)
//
//	⇒ rl 是 **hot ∪ 父表全部历史**（≈1.1 M 行 × 113 列）的派生表，
//	⇒ 而它在**关联子查询内部** ⇒ 外层每一行都要把它**物化一遍**
//	⇒ ⇒ 实测 52,854 ms（把谓词推进 source 内部也一样，R85 手法在这里无效）。
//
// 【修法】两腿 EXISTS，各自带 session_id/tenant_id 谓词：
//
//	EXISTS (SELECT 1 FROM session_turns_hot t WHERE t.session_id = s.session_id …)
//	OR EXISTS (SELECT 1 FROM session_turns t WHERE t.session_id = s.session_id …)
//
//	⇒ 谓词**落在索引上**（idx_session_turns_hot_tenant_session_turn 等）
//	⇒ 实测 52,854 ms → **192~212 ms**（api_key 族）、36,415 ms → **198 ms**（model 族）。
//
// ★ **这不是新发明**：admin/turns_filter_options.go 的 turnMergedSource
//   早在 2026-10-18 就用了同一手法（实测：视图 44,564 ms ⇒ 两腿 2,048/2,814 ms）。
//   本 helper 只是把那条**已验证**的形态收敛成**单一真值源**，避免第三次各写一份。
//
// # 口径等价性（★ 前提与实测，见下）
//
// 【model / provider / status_code 三族】旧形态读视图的对应列，新形态读
//
//	hot ∪ 父表的同名列。视图 ≡ hot ∪ 父表：**结构性成立**——
//	promote_session_turns_hot_to_partition 是
//	「DELETE FROM session_turns_hot … INSERT INTO session_turns …」的**移动**，
//	⇒ 同一 request_id 不会同时存在于两边 ⇒ 视图的反连接剔除 0 行
//	⇒ 252 生产实测（2026-10-24）：hot ∩ 父表 按 (tenant_id, request_id) 配对
//	   **0 行**；view 行数 1,107,672 = hot ∪ 父表原始行数 1,107,672。
//
// 【api_key_id 一族】旧形态读的是 **113 列投影**里的 `api_key_id`
//	（= SessionFamilyAPIKeyIDExpr 的文本列→bigint 归一），新形态读同一表达式
//	⇒ **逐字同一个谓词**。
//	★ 唯一的前提：旧形态用 `rl.request_id = ft.request_id` 关联（**不带 tenant_id**），
//	  新形态直接按 (session_id, tenant_id) 匹配 ⇒ 等价要求
//	  「request_id → (session_id, tenant_id) 是函数依赖」。
//	  252 生产实测（2026-10-24，全库 hot ∪ 父表、**跨租户**）：**0 例外**。
//	  ★ 而新形态同时**消掉了旧形态的一个潜在跨租户串味**（旧形态的 JOIN 不带 tenant_id）。
//
// # 契约
//
//   - 外层会话别名**必须**是 `s`（本 helper 固定引用 `s.session_id` / `s.tenant_id`）。
//   - colExpr 必须引用别名 `t`（两条腿都用 `t`）。
//   - 两条腿各自是独立子查询作用域 ⇒ 复用同一别名 `t` 合法。
//
// # ★★ R92（2026-10-24）：形态从「两条 EXISTS 用 OR 组合」改成
// **「派生表 UNION ALL + 单一 EXISTS」** —— 因为 OR 形态**锁死了驱动方向**。
//
// 【R90 形态为什么不够（生产实测，build 2841）】
//   R90 上线后，154 直连 REPEAT=3：
//     ?limit=20                       200×3  0.878/0.155/0.111 s
//     ?limit=20&api_key_id=105        200×3  9.576/0.216/0.151 s
//     ?limit=20&model=deepseek-ai%2F…  500×3  30.008/30.007/30.018 s
//     ?limit=20&provider=14           200×3  0.358/0.100/0.080 s
//     ?limit=20&status_code=500       200×3  0.554/0.181/0.153 s
//   ⇒ ★★ **R90「四条全修」的结论被证伪**：只有 model 那条仍 500@30s。
//
// 【真因：不是形状问题，是「稀有过滤值 ⇒ 外层深扫」】
//   252 生产只读实测：
//     该 model 在父表 **458 轮 / 30 个会话**，hot 腿 **0 轮**；
//     public.sessions **441,577 行 / 441,112 个会话**。
//   ⇒ 过滤只命中 30 / 441,112 个会话，而会话列表是
//     `ORDER BY s.updated_at DESC, s.session_id DESC LIMIT 21`
//     ⇒ **必须扫到凑够 21 行**。
//   ⇒ EXPLAIN ANALYZE 实测（事务内建索引后）：
//       Index Scan using sessions_2026_09_updated_at_session_id_idx
//         → Rows Removed by Filter: **72,455**
//       Index Scan using sessions_2026_10_updated_at_session_id_idx
//         → Rows Removed by Filter: **277,899**
//     ⇒ ★ 外层一共扫了 **350,354 个会话**才凑出 21 行 ⇒ **17,501 ms**。
//   ⇒ 对照：provider='14' 命中 181,907 轮、status_code=500 命中 671,117 轮
//     ⇒ 前 21 个会话立刻凑齐 ⇒ 0.358s / 0.554s
//     ⇒ ★ **同一个形状，只因「过滤值稀有度」不同，一个 0.3s 一个 30s**。
//   ⇒ ★ 已排除「统计信息陈旧」：session_turns last_analyze=2026-10-19、
//     session_turns_2026_10 last_autoanalyze=2026-10-24 ⇒ 统计是新鲜的；
//     规划器估 128 行 vs 实际 458 行（只低估 3.6×）⇒ 不是统计缺失能解释的量级。
//
// 【OR 形态为什么锁死了驱动方向】
//   OR 形态只能被规划成「两条相关 EXISTS」⇒ 规划器只能
//   **会话驱动**（外层扫会话、每行去 EXISTS 里查轮次）。
//   ⇒ 过滤值稀有时，会话驱动要扫 35 万行 ⇒ 30s 超时。
//   ⇒ 而**轮次驱动**（先枚举匹配的轮次、再去找会话）在稀有过滤下只要 30 行。
//
// 【V2 形态：让规划器能自己选】
//
//	EXISTS (SELECT 1 FROM (
//	    SELECT t.tenant_id, t.session_id, <colExpr> FROM public.session_turns t
//	    UNION ALL
//	    SELECT t.tenant_id, t.session_id, <colExpr> FROM public.session_turns_hot t
//	) ft WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id AND <pred>)
//
//   ⇒ 派生表可被 planner **pull-up** ⇒ 半连接的两个驱动方向都可选：
//   · 过滤值**稀有** ⇒ 轮次侧走 model 索引 ⇒ **轮次驱动**（外层只剩 30 行）
//   · 过滤值**常见** ⇒ 轮次侧无索引 ⇒ 顺序扫 110 万行太贵 ⇒ 规划器自动选
//     **会话驱动**（外层前 21 个会话立刻凑齐）
//   ⇒ ★ **不写死「轮次驱动」** —— 那是 JOIN 形态（V3），实测在常见过滤上
//     退化到 **10,080 ms**（provider='14'）。
//
// 【252 生产实测（事务内真实文件体 + ROLLBACK ⇒ 生产零持久变更）】
//
//	形态                                  model=稀有      provider='14'(常见)
//	────────────────────────────────────────────────────────────────────────
//	旧形态（R90 两条 EXISTS OR）              17,501 ms ✗    0.358 s ✓
//	只加索引、不改形态                        17,501 ms ✗    （仍会话驱动）
//	JOIN 派生表（V3，轮次驱动写死）              8.0 ms  ✓    **10,080 ms** ✗✗
//	★★ V2 形态 + 迁移 905 的 model 索引         **8.0 ms** ✓  **48.6 ms** ✓
//
// 【★ 口径等价（252 生产，同一事务内同一快照，双向 EXCEPT ALL）】
//
//	model（稀有）  : only_r90=**0** / only_v2=**0** / r90_rows=21 / v2_rows=21
//	provider='14'  : only_r90=**0** / only_v2=**0** / r90_rows=21 / v2_rows=21
//
//   ⇒ 结果集**逐行一致** ⇒ 两形态只差「求值顺序」，不差「取哪些行」。
//   ⇒ 结构上也等价：旧形态 =「hot 腿 EXISTS ∨ 父表腿 EXISTS」，
//     新形态 =「(hot ∪ 父表) 派生表上的 EXISTS」⇒ 同一谓词集、同一半连接语义
//     （NULL 两侧都用 `=` ⇒ NULL 不匹配 ⇒ 行为一致）。
//
// 【★ 硬依赖：迁移 905 的 model 索引】
//   V2 形态的「轮次驱动」分支**依赖** `idx_session_turns_model_session`：
//   无索引时实测 V2 / model=稀有 = **205,708 ms**（规划器退化成
//   「每行对 110 万轮做顺序扫」的嵌套循环半连接）。
//   ⇒ ★ 生产上由**启动迁移 905** 在新二进制对外服务**之前**建好该索引
//     （startup 迁移在切换前投递）⇒ 窗口为 0。
//   ⇒ ★ 若索引被回滚而二进制未回滚：HTTP 症状与今天**相同**（角色级
//     statement_timeout=30s ⇒ 同样 500@30s），不会更差，但会白烧 30s DB 时间。
//   ⇒ 语义耦合由 migration_905_turns_session_model_idx_test.go 钉住。
func SessionFamilyTurnsSessionExistsSQL(colExpr, colName, pred string) string {
	return "(EXISTS (SELECT 1 FROM (" +
		"SELECT t.tenant_id AS tenant_id, t.session_id AS session_id, " + colExpr + " AS " + colName +
		" FROM public.session_turns t" +
		" UNION ALL " +
		"SELECT t.tenant_id, t.session_id, " + colExpr +
		" FROM public.session_turns_hot t" +
		") ft WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id AND " + pred + "))"
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

// SessionFamilyTurnsForSessionsSQL 返回与 SessionFamilyTurnsSourceSQL **同一个冻结列投影**，
// 但把 **session 谓词推进两条腿内部**（`t.session_id = ANY($1)`），让规划器能用
// idx_session_turns_session (session_id, turn_no DESC) 逐会话取行，
// 而不是把全部历史物化后再 Hash Join。
//
// # 为什么需要它（★ 2026-10-24 生产实测，可复算）
//
// 【症状】GET /api/admin/turns/sessions 的 **api_key enrich 腿**独占端点耗时：
//
//	pg_stat_statements 增量差分（打 3 次端点，252 只读）：
//	  SELECT DISTINCT ON (t.session_id) …   每次 **20,176 ms** ← 端点的全部
//	  SELECT s.session_id, s.tenant_id, …    每次      **8.3 ms**（主列表查询）
//	  SELECT t.session_id, t.turn_no, …      每次      **4.2 ms**（轮次加载）
//
// 【根因】enrich 腿用的是 SessionFamilyTurnsSourceSQL() ——
// **session_turns_hot UNION ALL session_turns 父表的全部历史分区**，
// 而 session 谓词在 source **外部** ⇒ 规划器只能把整条 UNION ALL 物化
// （估算 1,587,817 行）再 Hash Join。
//
// 【修法与实测】单变量 A/B（252 EXPLAIN ANALYZE，同 21 个**真实** session_id）：
//
//	A 谓词在 source 外部：Execution Time 11,478.486 ms（Hash Join，估算 1,587,817 行）
//	B 谓词推进两腿内部：Execution Time      6.369 ms（Hash Join，估算      1,070 行）
//
//	⇒ ~1,800×；B 的两条腿走 `Index Scan using idx_session_turns_hot_tenant_session_turn`。
//
// # 与 SessionFamilyTurnsForSessionSQL（单 session 版）的区别
//
// 单 session 版谓词是 `t.session_id = $1`；本版是 `= ANY($1)`，
// 因为调用方（会话列表页）一次要 enrich **一批** session（默认 21 个）。
//
// # 契约
//
//   - $1 必须是 session_id 数组（text[]）；谓词发射为 `t.session_id = ANY($1)`，
//     两条腿各一份 ⇒ 同一个占位符在同一条 SQL 里出现两次（PG 允许）。
//   - 投影与 SessionFamilyTurnsSourceSQL 逐字同源（同一 sessionFamilyProjection(true)）
//     ⇒ 列集合、列名、列序完全一致 ⇒ 调用方的 SELECT 列表无需改动。
//   - 调用方仍需自己加租户收窄（`WHERE rl.tenant_id = $2`）—— 本 helper 不带租户语义。
//   - 语义等价性：JOIN 条件（`rl.request_id = t.request_id`）决定哪些 rl 行入选；
//     本 helper 只是把 rl 的候选集**收窄**到这些 session 自己的轮次。
//     ⇒ 只有当「同一 request_id 在 rl 里属于**别的** session」时才会少一行 ——
//     而 request_id 是轮的标识（UNIQUE (tenant_id, request_id, partition_date)），
//     实测不存在这种跨 session 归属。
//
// The returned source carries no alias — callers append one (e.g. `rl`).
func SessionFamilyTurnsForSessionsSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = ANY($1)" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = ANY($1))"
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
//
// # 适用范围（2026-10-21 §143.56 扩记）
//
//	原始合同是「喂给 SessionFamilyBodiesSourceSQL 的列别名」。本轮起它**也**被
//	用在两个不在那个源上的别名上：
//
//	- `f`  = `public.session_bodies_unified` 的别名（final_full 快照腿）
//	- `ff` = 同上的 LATERAL 结果别名
//
//	⇒ 成立条件只有一条：**该别名下有一个 jsonb 类型的 `outbound_body` 列**。
//	这三处都满足（会话族与 unified 视图同源于 `session_bodies*`）。
//	⇒ ★ 判据**只有一个事实源**：`cmd/gateway` 的 L3 冷启动式
//	（`lastOutboundSessionSQL`）三处「有正文」判据都调它，
//	与 `admin/data_lifecycle.go`、`cmd/compression-bench/main.go` 同口径。
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
// # ★ 2026-10-20（迁移 890，v1 族最终删除）：本切换层**退化为 no-op**。
//
// 原实现：开关 `storage.session_bodies_native_read` 关（默认）时返回
// `request_logs_bodies_with_current_month`（**v1 正文族的视图**），开时返回
// 会话族源。那是灰度期的回滚设计：开关关 = 读 v1，开 = 读会话族。
//
// 890 删掉了 v1 族，**连同那张视图** ⇒ 默认臂指向一个**已不存在的关系**
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
