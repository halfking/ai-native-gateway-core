package db

// session_turns_windowed_source.go —— 会话族 turns 的**带分区键界**读源（R181 轮的
// SessionFamilyTurnsSourceWindowedSQL 及其配套）。
//
// ★ 为什么单独一个文件（2026-11-25）：这些构造只被 Go 读方（/api/logs 列表、
// 审核单上下文）拼接进查询，**不参与** canonical 视图的装配；而
// request_logs_view_schema.go 是视图装配文件——它里面的 SQL 字面量会在启动期
// 进入 CREATE OR REPLACE VIEW（即时校验，任何 42P01 都让整个 boot 失败），
// 所以 TestApplyMigrationsIncludesRequestLogsViewEnsure 对那个文件有一条负向
// 契约：不许出现 LEFT JOIN LATERAL。本文件的 details 腿正是 LATERAL 单行点查
// ⇒ 放在这里，守卫原样保留、不削弱。
//
// 依赖的投影（sessionFamilyProjection / detailsProjectionColumns）仍在
// request_logs_view_schema.go：读源与视图共用同一份 118 列契约。

import (
	"sort"
	"strings"
)

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
//
//	`request_logs_with_current_month` 读的是同一对关系（hot ∪ 父表）、
//	投影走**同一个** sessionFamilyProjection(true)、JOIN 条件逐字相同
//	（★ 视图那版带 `::text` 冗余转型，本版不带 ⇒ ★ 索引可用性反而更好，
//	  ★ 取值逐行相同 ⇒ 语义不变）★★
//	⇒ ⇒ 唯一差别 = 本版多了 partition_date 谓词 ⇒ ⇒ 行集是 ts 谓词的**子集**，
//	  ★ 而 partition_date == calendarDate(ts)（★ 所有写入方都用它）⇒
//	  ★ ⇒ 子集 == 原行集 ⇒ ⇒ **不丢行** ★
//
// ★ 参数：★ pdFrom / pdTo 是**两臂各自**要用的 SQL 片段（形如 "$1" / "$2"），
//
//	★ 由调用方决定参数编号 ⇒ ⇒ 本包不感知参数位置。
//
// ★ 返回值不带外层别名（调用方自己加，如 ` rl`）。
//
// ★★ 2026-11-25 request-logs 超时根修：★★ details 腿从「ON 子句等值连接」改成
// 「按连接键的**单行点查**」（LEFT JOIN LATERAL … LIMIT 1）。
//
// # 为什么（211 生产 EXPLAIN ANALYZE 实测，不是推断）
//
//	ON 子句形态下，规划器对「怎么连 details」是自由的。月分区
//	session_turns_2026_11 / session_turn_details_2026_11 的统计信息停在两天前
//	（autovacuum_analyze_scale_factor 用的是全局默认 0.1 ⇒ 24 万行要改 2.4 万行
//	才重采）⇒ 查询窗口整体落在 ts 直方图上界**之外** ⇒ 冷分区这一臂被估成
//	**3 行**（实际 4,850）、details 侧被估成 **1 行**（实际 16,916）⇒ 规划器选了
//	「Nested Loop + Materialize + **Join Filter**」而不是走唯一索引：
//
//	  Rows Removed by Join Filter: 104,492,130      ← 「昨天·含探测」聚合语句 37.7s
//
//	一条语句 26~38s，handler 一个预算内要跑三条 ⇒ 60s 超时。
//
// # 为什么是这个形状
//
//	· LATERAL 子查询**引用外层行**（t.tenant_id / t.request_id / t.partition_date）
//	  ⇒ 内侧只能按外层行**参数化**执行 ⇒ 走
//	  session_turn_details*_request_id_key (tenant_id, request_id, partition_date)
//	  唯一索引，每行一次点查（实测 0.005~0.018ms/次）⇒ 代价与**估算无关**。
//	· `LIMIT 1` 是**优化栅栏**：不带它，PG 会把这个简单子查询上拉回普通
//	  LEFT JOIN（实测计划逐字回到老形状）。它同时依赖并表达了一个事实：连接键
//	  在 details 上**唯一**（hot：session_turn_details_hot_request_key；月分区：
//	  session_turn_details_<月>_tenant_id_request_id_partition_key，均为 valid
//	  的唯一索引；211 实测键列无 NULL）⇒ 原 ON 形态每个 turn 至多配 1 行
//	  ⇒ LIMIT 1 **不改变行集**（真库对拍见
//	  TestSessionFamilyTurnsSourceWindowedSQL_RowSetEqualsPlainJoin）。
//	· 分区键界仍在（d.partition_date >= / <=）：JOIN 条件里有
//	  d.partition_date = t.partition_date 而 t 侧被限在 [pdFrom, pdTo] ⇒ 这条是
//	  **冗余谓词**（逐行等价、不是新筛选），留下的作用是让 details 的月分区在
//	  规划期就被裁掉（R181 实测：不给它时 details 侧 3 个月分区全扫）。
//
// ★ 统计信息陈旧本身另有一道根修（PartitionManager 的月分区 analyze 设置巡检）；
// 本函数这道是**查询形状的鲁棒性**：估算再错，计划也不会退化成笛卡尔式比较。
// 两者是同一条故障的两个面，不互相替代。
//
// ★ 同形态实测（211，统计信息仍陈旧）：聚合 / 分页页查询 ⇒ 近 24h 含探测
// 26.3s / 25.8s → 0.34s / 0.41s；「昨天」含探测 37.7s → 0.40s / 0.39s。
func SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo string) string {
	return "(" + strings.Join(SessionFamilyTurnsWindowedArms(pdFrom, pdTo), " UNION ALL ") + ")"
}

// sessionTurnDetailsHotFrom / sessionTurnDetailsParentFrom 是 details 两腿的
// FROM 子句（关系名整字面写出）。
const (
	sessionTurnDetailsHotFrom    = " FROM public.session_turn_details_hot d"
	sessionTurnDetailsParentFrom = " FROM public.session_turn_details d"
)

// sessionTurnDetailsLookupColumns 是 details 点查的显式列表（= 投影里以 d.<col>
// 出场的全部列，即 detailsProjectionColumns 的键）。按名排序 ⇒ 渲染文本确定。
var sessionTurnDetailsLookupColumns = func() string {
	cols := make([]string, 0, len(detailsProjectionColumns))
	for name := range detailsProjectionColumns {
		cols = append(cols, "d."+name)
	}
	sort.Strings(cols)
	return strings.Join(cols, ", ")
}()

// sessionTurnDetailsLookupJoin 渲染一条 turns 臂的 details 腿：按外层行键的
// 单行点查（详见 SessionFamilyTurnsSourceWindowedSQL 头注）。
func sessionTurnDetailsLookupJoin(detailsFrom, pdFrom, pdTo string) string {
	return " LEFT JOIN LATERAL (SELECT " + sessionTurnDetailsLookupColumns + detailsFrom +
		" WHERE d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" AND d.partition_date >= " + pdFrom + " AND d.partition_date <= " + pdTo +
		" LIMIT 1) d ON TRUE"
}

// SessionFamilyTurnsWindowedArms 返回带分区键界的**业务两臂**（热表臂、父表臂），
// 顺序固定。SessionFamilyTurnsSourceWindowedSQL 就是把它们用 UNION ALL 接起来
// ⇒ 两处的臂文本**同源**。分页路径要「每臂先取 top-K 再归并」时直接取臂
// （见 admin/logs.go 的 listLogsPageInnerSQL）。
//
// ★ 返回的每一臂自带括号、不带外层别名（调用方自己包一层再起别名）。
func SessionFamilyTurnsWindowedArms(pdFrom, pdTo string) []string {
	win := " WHERE t.partition_date >= " + pdFrom + " AND t.partition_date <= " + pdTo
	return []string{
		"(SELECT " + sessionFamilyProjection(true) +
			" FROM public.session_turns_hot t" +
			sessionTurnDetailsLookupJoin(sessionTurnDetailsHotFrom, pdFrom, pdTo) +
			win + ")",
		"(SELECT " + sessionFamilyProjection(true) +
			" FROM public.session_turns t" +
			sessionTurnDetailsLookupJoin(sessionTurnDetailsParentFrom, pdFrom, pdTo) +
			win + ")",
	}
}
