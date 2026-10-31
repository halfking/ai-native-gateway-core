package sessionfamily

// 会话族「轮次读点」的两臂窄投影（R172 · 2026-10-27）
//
// # 这份文件解决什么
//
// `public.session_turns_with_current_month` 的 hot 臂带一个**相关反连接**
// （把「已经晋级到父表的同键行」从 hot 臂剔掉），视图整体 65 列。
// ⇒ 每次**规划**都要展开「两臂 × 65 列 + 一个 Nested Loop Anti Join」。
//
// 252 生产规模实测（EXPLAIN ANALYZE，**字面量**谓词，三轮交错；
// min / 中位 / max）：
//
//	读点        形态          Planning (ms)              Execution (ms)          shared hit
//	① 冲突读回   视图    8.655 / 14.033 / 17.427   0.394 / 0.488 / 0.784      2 ~ 312
//	① 冲突读回   本形态  6.647 /  6.902 / 16.741   0.387 / 0.489 / 0.515      3 ~ 222
//	② GetTurn   视图    7.948 / 11.187 / 27.518   0.627 / 0.701 / 1.739      3 ~ 872
//	② GetTurn   本形态  7.327 /  8.628 / 14.829   0.382 / 0.618 / 0.753      3 ~ 222
//	③ ListTurns 视图   13.690 / 22.751 / 25.359   0.682 / 1.086 / 51.255     3 ~ 243
//	③ ListTurns 本形态  6.712 / 10.976 / 12.249   0.529 / 0.603 / 0.724      3 ~ 232
//
// ⇒ ★ 可分辨的差异在**访问路径**与 **buffer 计数**（不是 execution 中位数）：
//   视图形态三处都多一个 `Subquery Scan`（＝逐 hot 行求值的反连接子查询），
//   本形态三处都**没有**；② 的 shared hit 872 → 222（3.9×）。
// ⇒ ★ execution 中位数两形态只差 1~2× 且区间高度重叠 ⇒ **只报「分布整体偏低」**。
//
// ★★ **第一版脚本的读数已作废**：那版用 `(SELECT tenant_id FROM …)` 取键 ⇒
//   谓词里出现子查询 ⇒ planner 不能下推 ⇒ 全走 Seq Scan ⇒ 读出
//   「view exec 23~57 ms」⇒ ★★ **那是量具的伪影，不是事实源的**（LESSONS §475 同族：
//   §475 错在采样次数，本轮错在谓词形态）。
//
// # ★★ 为什么是「变体 C」（保留反连接 + 把谓词推进两臂）而不是「变体 B」（去掉反连接）
//
// 【变体 B】两臂直查，**不要**反连接：
//
//	(SELECT … FROM session_turns_hot WHERE <键谓词>)
//	UNION ALL (SELECT … FROM session_turns   WHERE <键谓词>)
//
// ⇒ 它的等价性**依赖一个数据前提**：「hot ∩ 父表 = ∅」。
//   · 前提**大体成立**：promote_session_turns_hot_to_partition 是
//     「DELETE FROM session_turns_hot … INSERT INTO session_turns …」的**单语句
//     CTE** ⇒ 原子 ⇒ 一行是**搬家**不是**复制** ⇒ 同一 request_id 不会同时在两边；
//     写入侧的幂等守卫（`WHERE NOT EXISTS (ExistsByKeySQL(tenant, request))`）
//     也按 (tenant_id, request_id) 拦 ⇒ 跨分区也拦得住。
//     实测：本地真库 hot ∩ 父表 = 0 对；252 生产 = 0 对。
//   ★ 但前提**不是恒真的**：`cmd/tools/validate_sessions_v2/repair.go` 往父表
//     回填 v1 轮次时**没有存在性守卫** ⇒ 它跑完可能造出同键的两份。
//     而 canonical 视图（request_logs_with_current_month，113 列）**根本没有**
//     反连接 ⇒ 仓内早已接受「不按 request_id 去重」的语义 ⇒ B 与仓内主读面同语义。
//
// 【变体 C】两臂直查，**保留**反连接，但把键谓词**推进两臂内部**：
//
//	(SELECT … FROM session_turns_hot h
//	  WHERE <键谓词>
//	    AND NOT EXISTS (SELECT 1 FROM public.session_turns archived
//	                     WHERE archived.tenant_id::text = hot.tenant_id::text
//	                       AND archived.request_id = hot.request_id))
//	UNION ALL
//	(SELECT … FROM public.session_turns WHERE <键谓词>)
//
// ⇒ ★★ **它与视图是逐字同一套 arms**（反连接谓词逐字照抄）⇒ 等价性是
//   **结构性的**：不依赖任何数据前提、不依赖「hot ∩ 父表 = 0」、不怕任何
//   非原子回填路径。
//   ⇒ 而「谓词推进两臂」把反连接的**求值范围**从「整张 hot 表」缩到
//   「命中键谓词的候选行」⇒ 反连接不再逐行扫 hot。
//
// 252 实测（字面量谓词，三轮交错，② GetTurn 形态；★ 变体 B 是手写复刻，
// 不是产品形态 ⇒ 只作量级参照）：
//
//	形态                                  Execution(med)   shared hit
//	A 视图                                   0.701 ms        3 ~ 872
//	B 两臂无反连接（手写复刻）                   0.494 ms        169
//	C 两臂保留反连接（本轮产品形态）              0.618 ms        3 ~ 222
//
// ⇒ C 比 B 慢 ~1.25×（执行），但比 A 的 **buffer** 少 3.9×（872 → 222）。
// ⇒ ★★ 选 C：**用这点执行代价，换掉一个「前提被破就静默出错」的风险**。
//
// # 投影逐字等价（第二维）
//
// 视图的每一列都是**裸列**（`hot.<col>` / `session_turns.<col>`，**没有任何
// 表达式**；本地与 252 的 pg_get_viewdef 都是这个形态）⇒ 视图读到的值 =
// 直接读对应底表同一列的值。
// ★ 因此本文件把**视图那一层的裸列**原样搬进两臂，把 COALESCE 留在**外层**
//   （与改前「视图外再包 COALESCE」的层次完全一致）。
//
// # RLS 可见集一致（第三维）
//
// 视图带 `security_invoker = true`（757 迁移的自检强制它）⇒ 视图的 RLS 语义
// 由**底表**决定：`session_turns`（父表）RLS-ON（策略
// `session_turns_tenant_isolation`：`tenant_id::text = current_setting('app.current_tenant')`），
// `session_turns_hot` RLS-OFF。
// ⇒ 两种形态读的是**同样两张表**、在**同一个调用者身份**下读 ⇒ 可见行集一致。
//
// # 谓词与 UNION ALL 可交换（第四维）
//
// 过滤是合取式 ⇒ 与 UNION ALL 可交换 ⇒ 「谓词推进两臂内部」与「谓词留在外面」
// 结果同集；反连接是**逐行**谓词（不引用外层列）⇒ 「先过滤再反连接」与
// 「先反连接再过滤」同集。★ 两条合起来 ⇒ C ≡ 视图。
//
// # 参数
//
//   - tenantExpr / requestExpr / sessionExpr / limitExpr：SQL 表达式（如 "$1"）。
//     ★ 两臂用**同一对**表达式 ⇒ 两臂的键口径逐字相同。
//     ★ 谓词**推进两臂内部**：父表是全历史分区表，谓词留在外面会把整条
//     UNION ALL 物化（仓内 `db.SessionFamilyTurnsForSessionsSQL` 注释里记着
//     同一形态 1,800× 的实测差）。
//
// ★★ **窄投影必须自带过滤列**（R172 实测踩到）：如果 WHERE 引用的是列而投影里
// 没有它，PG 报 42703（column … does not exist）。本文件的所有投影都把
// 过滤列（tenant_id / request_id / session_id）与排序键（partition_date /
// turn_no）**一并列出**，从根上杜绝这类 42703。

// turnsReadCol 描述「轮次读点」投影里的一列。
//
//	name：底表列名（两臂都按这个名字投影 ⇒ 列名与视图一致）
//	coalesce：外层要包的那个 COALESCE 的默认值（"" = 不包）
//
// ★★ **raw 与 outer 由同一张表渲染** —— 改前 GetTurn 与 ListTurns 各写了一份
// SELECT 列表（43 列），两份**逐字相同**这件事没有任何机制保证；这里让
// 「两臂投影」与「外层投影」都从这张表渲染 ⇒ 结构上不可能漂移。
type turnsReadCol struct {
	name     string
	coalesce string
}

// turnsReadCols 是轮次读点的 43 列契约（与改前两份 SELECT 列表逐字同序）。
//
// ★ 列序 = Scan 顺序 = 改前的列序：**不许重排**（改了会静默错位读值）。
var turnsReadCols = []turnsReadCol{
	{"session_id", ""},
	{"turn_no", ""},
	{"tenant_id", ""},
	{"request_id", ""},
	{"ts", ""},
	{"project_id", "''"},
	{"namespace", "''"},
	{"parent_request_id", "''"},
	{"task_type", "''"},
	{"submit_mode", ""},
	{"compression_applied", ""},
	{"compression_strategy", ""},
	{"compression_meta", ""},
	{"compression_tokens_saved", "0"},
	{"injection_verdict", "'skip'"},
	{"output_verdict", "'skip'"},
	{"model", ""},
	{"provider", ""},
	{"credential_id", ""},
	{"prompt_tokens", "0"},
	{"completion_tokens", "0"},
	{"cache_read_tokens", "0"},
	{"cache_write_tokens", "0"},
	{"cost_usd", "0"},
	{"latency_ms", "0"},
	{"status_code", "0"},
	{"success", "false"},
	{"error_kind", ""},
	{"source_kind", ""},
	{"quality", ""},
	{"attachment_count", "0"},
	{"attachment_total_bytes", "0"},
	{"multimodal_types", "'{}'"},
	{"t0_arrived_at", ""},
	{"t1_total_enqueued_at", ""},
	{"t2_total_dequeued_at", ""},
	{"t3_model_enqueued_at", ""},
	{"t4_model_dequeued_at", ""},
	{"t5_cred_enqueued_at", ""},
	{"t6_cred_dequeued_at", ""},
	{"t7_forward_start_at", ""},
	{"t8_response_start_at", ""},
	{"t9_response_end_at", ""},
}

// TurnsReadColumnsRaw 是两臂内部投影的**裸列**列表（43 列）。
//
// ★ 它对应视图那一层：视图投影的就是裸列 ⇒ 两臂投影裸列 ⇒ 值逐字相同。
// ★ 它**包含全部过滤列与排序键**（tenant_id / request_id / session_id /
// partition_date / turn_no）⇒ 外层 WHERE / ORDER BY 不会 42703。
var TurnsReadColumnsRaw = turnsReadColumnsRaw()

// TurnsReadColumns 是外层 SELECT 的**读值**列表（43 列，含 COALESCE）。
//
// ★ 它对应改前「视图外再包 COALESCE」的那一层 ⇒ 改前改后读到的值逐字相同。
var TurnsReadColumns = turnsReadColumns()

func turnsReadColumnsRaw() string {
	parts := make([]string, 0, len(turnsReadCols))
	for _, c := range turnsReadCols {
		parts = append(parts, c.name)
	}
	return joinSQLList(parts)
}

func turnsReadColumns() string {
	parts := make([]string, 0, len(turnsReadCols))
	for _, c := range turnsReadCols {
		if c.coalesce == "" {
			parts = append(parts, c.name)
			continue
		}
		parts = append(parts, "COALESCE("+c.name+", "+c.coalesce+")")
	}
	return joinSQLList(parts)
}

// joinSQLList 把列名列表渲染成「逗号 + 换行 + 制表符」的形态（仓内 SQL 排版风格）。
func joinSQLList(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ",\n\t\t\t"
		}
		out += p
	}
	return out
}

// antiJoinPredicate 是**视图 hot 臂的反连接谓词**（逐字照抄）。
//
// ★ 它是本轮等价性的**承重件**：改一个字，两臂就不再与视图同语义。
// ★ 注意它**不含 partition_date**（⇒ 同键跨分区也算命中）。
const antiJoinPredicate = "NOT EXISTS (SELECT 1 FROM public.session_turns archived\n" +
	"\t\t\t\t\tWHERE archived.tenant_id::text = hot.tenant_id::text\n" +
	"\t\t\t\t\t  AND archived.request_id = hot.request_id)"

// TurnsArmsSQL 返回「hot ∪ 分区父表」的**两臂 FROM 源**：
// 键谓词推进两臂内部，hot 臂**保留**视图那条反连接（见文件头注「变体 C」）。
//
// ★ 返回值不带外层别名（调用方自己加，如 " t"）。
// ★ pred 为空字符串时：**父表腿不加 WHERE**、**hot 腿只带反连接**
//   （`WHERE NOT EXISTS (…)`）⇒ 形态仍是合法的 SQL。
//   ⚠ 此时这个源是**整族扫描**形态：hot 腿要对**每一行** hot 做一次反连接探测
//   ⇒ 生产规模上不可负担（252 上整族形态的行数查询就要 ~1 分钟以上）。
//   ⇒ ★ **只把它用在带键谓词的单点/单会话读点上**（谓词推进 ⇒ 候选行只有几条）。
func TurnsArmsSQL(columns, pred string) string {
	hot := "(SELECT " + columns + "\n\t\t\tFROM public.session_turns_hot hot"
	if pred != "" {
		hot += "\n\t\t\tWHERE " + pred + "\n\t\t\t  AND "
	} else {
		// ★ pred 为空时不能留孤立的 "AND"（语法错）⇒ 改成 "WHERE <反连接>"。
		hot += "\n\t\t\tWHERE "
	}
	hot += antiJoinPredicate + ")"
	parent := "(SELECT " + columns + "\n\t\t\tFROM public.session_turns"
	if pred != "" {
		parent += "\n\t\t\tWHERE " + pred
	}
	parent += ")"
	return "(" + hot + "\n\t\tUNION ALL\n\t\t" + parent + ")"
}

// TurnsListArmsSQL 返回「hot ∪ 分区父表」的**两臂 FROM 源**，供
// **跨会话列表**读点（`/api/admin/turns`）使用。
//
// ★★ 与 TurnsArmsSQL 的三处差别，每一处都有实测理由（2026-10-29）：
//
//	① **反连接的内侧带 partition_date 界** —— 本轮最值钱的一处。
//	   列表读点的谓词不是键谓词（是 ts 窗口 + 可选租户），所以
//	   「谓词推进两臂内部」**不会**减少反连接的求值次数：hot 臂命中窗口的
//	   每一行都要在父表上做一次存在性探测。而视图形态下，那次探测的内侧
//	   （`public.session_turns archived`）**拿不到任何外层谓词**（相关子查询
//	   的内侧不接收外层 qual）⇒ ⇒ 内侧 Append **遍历全部 5~6 个月分区**，
//	   每个分区按 (tenant_id::text) 索引定位后**扫该租户的全部索引项**、
//	   再用 `Filter: request_id` 过滤 ⇒ ★「每 hot 行扫该租户全部索引项」。
//	   本地真库实测（EXPLAIN ANALYZE, BUFFERS；24h 窗口、33 行 hot）：
//
//	形态                                   反连接内侧            buffers
//	视图形态（改前）        Nested Loop Anti Join × 5 分区 × 33 loops      360
//	两腿形态（改后）        Append 裁到 1 分区、单次扫描                    128
//
//	   ⇒ 形态从「每 hot 行一次嵌套循环探测」变成「一次性扫描窗口分区」。
//
//	② **两臂别名不同**（hot / p）⇒ 谓词按别名分别渲染（列表读点要投影
//	   20 个读值列 + tenant_id / request_id / partition_date）。
//
//	③ **反连接的租户比较逐字保留视图的 `::text` 形态** ⇒ 口径与视图
//	   逐字同源。★ 实测澄清：把它改成不带 cast（`archived.tenant_id =
//	   hot.tenant_id`）**不改变任何计划** —— PG 会把 varchar 比较一律归一成
//	   `::text` 形态（列类型实测：两表 tenant_id 都是 character varying），
//	   索引选择与 buffer 数逐项相同。⇒ 「去 cast」不是这轮的正确杠杆，
//	   **内侧分区界**才是。
//
// ★★ 等价性（① 的代价，必须说清楚）：★★
//
//	内侧加 partition_date 界 ⇒ 反连接只问「窗口内的父表分区里有没有同键行」。
//	它与视图（全分区）的差别**只可能**来自一种数据形态：
//	★ **同一个 (tenant_id, request_id) 出现在父表的两个不同 partition_date 上**。
//	这种行一旦存在，窗口内的 hot 行会被视图剔掉、却被新形态保留。
//	⇒ 该形态**不可能**由正常路径产生，三条依据：
//	  · 晋级路径**逐字保 partition_date**（promote_session_turns_hot_to_partition
//	    的 INSERT … SELECT 取 h.partition_date）⇒ hot 行的对应行必在同一分区；
//	  · 写路径的幂等探针 `ExistsByKeySQL` **不带分区界** ⇒ 同键跨分区会被拦；
//	  · 父表的唯一键是 (tenant_id, request_id, partition_date) ⇒ 同键同分区
//	    不可能重复。
//	★ 唯一可能的越界来源是 v1 回填工具（cmd/tools/validate_sessions_v2/repair.go，
//	  它往父表回填时没有存在性守卫）⇒ ⇒ 由真库门
//	  TestTurnsListTwoLegs_NoCrossPartitionDuplicateKeys **持续检验**：
//	  一旦这个前提在某个库上不成立，那道门当场报红。
//
// ★ 参数：
//
//   - columns：两臂都投影的列清单（裸列 ⇒ 与视图那一层同语义）。
//   - hotPred / parentPred：两臂各自的谓词（**必须**推进到臂内部）。
//   - pdFromExpr / pdToExpr：分区窗口的 SQL 表达式（"$1" / "$2"），
//     同时用于内侧的反连接界。
func TurnsListArmsSQL(columns, hotPred, parentPred, pdFromExpr, pdToExpr string) string {
	hot := "(SELECT " + columns + "\n\t\t\tFROM public.session_turns_hot hot" +
		"\n\t\t\tWHERE " + hotPred +
		"\n\t\t\t  AND NOT EXISTS (SELECT 1 FROM public.session_turns archived" +
		"\n\t\t\t\t\tWHERE archived.tenant_id::text = hot.tenant_id::text" +
		"\n\t\t\t\t\t  AND archived.request_id = hot.request_id" +
		"\n\t\t\t\t\t  AND archived.partition_date >= " + pdFromExpr +
		"\n\t\t\t\t\t  AND archived.partition_date <= " + pdToExpr + "))"
	parent := "(SELECT " + columns +
		"\n\t\t\tFROM public.session_turns p" +
		"\n\t\t\tWHERE " + parentPred + ")"
	return "(" + hot + "\n\t\tUNION ALL\n\t\t" + parent + ")"
}

// TurnConflictLookupSQL 返回「按 (tenant_id, request_id) 读回既有轮次」的 SQL
// （冲突读回：INSERT 的 ON CONFLICT DO NOTHING 命中后，取回真实的 session_id /
// turn_no / partition_date）。
//
// ★ 口径钉：**ORDER BY partition_date ASC LIMIT 1**（改前逐字相同）。
// ★ 投影列 = session_id, turn_no, partition_date（partition_date 是排序键，
//   必须在投影里 ⇒ 窄投影自带排序键）。
func TurnConflictLookupSQL(tenantExpr, requestExpr string) string {
	const cols = "session_id, turn_no, partition_date"
	return "SELECT " + cols + "\n\t\tFROM " +
		TurnsArmsSQL(cols, "tenant_id = "+tenantExpr+" AND request_id = "+requestExpr) +
		" t\n\t\tORDER BY partition_date ASC\n\t\tLIMIT 1"
}

// TurnByRequestSQL 返回「按 (tenant_id, request_id) 取单个轮次」的 SQL。
//
// ★ 口径钉：**LIMIT 1**、**无 ORDER BY**（改前逐字相同 —— 键在族内唯一 ⇒ 无并列）。
func TurnByRequestSQL(tenantExpr, requestExpr string) string {
	return "SELECT " + TurnsReadColumns + "\n\t\tFROM " +
		TurnsArmsSQL(TurnsReadColumnsRaw,
			"tenant_id = "+tenantExpr+" AND request_id = "+requestExpr) +
		" t\n\t\tLIMIT 1"
}

// TurnsBySessionSQL 返回「按 (tenant_id, session_id) 取该会话的轮次」的 SQL。
//
// ★ 口径钉：**ORDER BY turn_no ASC** + **LIMIT limitExpr**（改前逐字相同）。
func TurnsBySessionSQL(tenantExpr, sessionExpr, limitExpr string) string {
	return "SELECT " + TurnsReadColumns + "\n\t\tFROM " +
		TurnsArmsSQL(TurnsReadColumnsRaw,
			"tenant_id = "+tenantExpr+" AND session_id = "+sessionExpr) +
		" t\n\t\tORDER BY turn_no ASC\n\t\tLIMIT " + limitExpr
}
