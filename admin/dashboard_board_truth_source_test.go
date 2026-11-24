package admin

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// ============================================================================
// dashboard_board_truth_source_test.go
//
// 看板「真源统一」守卫 —— 2026-11-24 真源统一轮新增，第二轮升级。
//
// ── 现在要钉住什么（第二轮形态）──────────────────────────────────────────
//
// ★★★ 看板（/api/admin/dashboard/board）的**三条读链必须读同一个宇宙**：★★★
//
//	① 英雄区汇总  queryBoardSummaryLogs  → FROM request_logs_with_current_month
//	② 趋势分桶    queryBoardTrendsLogs   → FROM request_logs_with_current_month
//	③ 饼图        queryBoardPiesFacts    → FROM request_logs_with_current_month
//
// ★★★ 三条**共用** boardLogsWhereTerminal ⇒ 同视图、同终态集、同探测谓词。★★★
//
// ── 为什么从「英雄区+趋势」升级为「三条读链」────────────────────────────
//
// ★★★ 第一轮只把趋势切到 usage_facts、饼图切到请求级日志 ⇒ 英雄区与趋势同源，
// ★★★ **但饼图在另一个宇宙** ⇒ 生产实测 2026-11-24「近30天」页签：
// ★★★   卡片/趋势（usage_facts）= 163,457   饼图（请求级日志）= 331,373
// ★★★   ⇒ **同一页面 2.08 倍**（近 7 天窗口恰好相等 ⇒ 缺陷只在长窗口显形）。
// ★★★ 成因不是切片写错，而是**两个宇宙的历史覆盖起点不同**：
// ★★★   usage_facts 的写入起点 = 2026-11-10 13:38（该刻一次批量 DELETE 抹掉了
// ★★★   2026-10-12~11-10 的 ≈35.2 万行真实业务事实，证据链见
// ★★★   docs/12小时内修订审计-2026-11-24-0704.md §C）；
// ★★★   请求级日志的保留起点更早（211 实测至少回溯到 2026-10-12）。
// ★★★ owner 拍板：**整页切请求级日志** ⇒ 本门钉「三条读链一个宇宙」。
//
// ── 关键不变量（三条，缺一条就出现「数字对不上」）──────────────────────
//
//	★ ① **同视图**：三条读链必须读同一张请求级日志视图。
//	★ ② **同谓词**：三条必须共用同一个 WHERE 构造器（结构保证，非文本比对）。
//	★ ③ **同图例**：饼图的 dim_key 取值形态必须与 bg/stats_minute_rollup.go 的
//	     rollupDimQueries **逐条一致** ⇒ 否则「主路径 → 降级路径」切换时图例全变。
//
// ★ ③ 由 TestBoardPieDimKeyExpressionsMatchRollup 跨文件比对实现（★ 防漂移 ★）。
// =============================================================================

// boardTruthSourceFiles 是本门要读的源文件（相对 admin 包）。
const (
	boardQueriesFile = "dashboard_board_queries.go"
	boardHandlerFile = "dashboard_board.go"
	boardRollupFile  = "../bg/stats_minute_rollup.go"
)

// readAdminFile 读 admin 包内的源文件。
func readAdminFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return stripGoCommentsKeepLines(string(raw))
}

// ── 门 ①：趋势主路径读**请求级日志**，且**不再**读任何预聚合表 ─────────────
//
// ★★★ 2026-11-24 第二轮（owner 拍板「整页切请求级日志」）：★★★
// ★★★ 第一轮趋势读 usage_facts（与饼图不同源 ⇒ 「近30天」页签差 2.08 倍）；
// ★★★ 本轮英雄区与趋势**一起**切到请求级日志 ⇒ 三条读链一个宇宙。
//
// ★★★ 2026-11-24 修订：★ 本门是**行为门**（查**实际执行的 SQL**，不是查某个
// ★★★ 函数体里的源文本）。★★★
// ★★★ 起因：★ SQL 被抽成 boardTrendLogsSQL 之后，源文本门**误报**（函数体里
// ★★★ 不再有 "FROM usage_facts"）⇒ ★★ 这就是「量具量错了东西」的第三例：
// ★★★ 门量的不是「PG 实际收到什么」，而是「源码长什么样」。
// ★★★ 现在门直接问：**PG 会收到哪条语句**。
func TestBoardTrendReadsRequestLevelLogsNotRollups(t *testing.T) {
	where, _ := boardLogsWhereTerminal(boardTimeRange{}, "", false)
	sql := boardTrendLogsSQL("date_trunc('hour', r.ts)", where, "r.credits_charged")
	if !strings.Contains(sql, "FROM request_logs_with_current_month") {
		t.Errorf("★ 趋势主路径没有读请求级日志 ⇒ ★ 趋势与英雄区/饼图不同源 ⇒\n"+
			"★ ★ 卡片 ≠ Σ趋势桶 ≠ 饼图合计（生产实测曾出现「近30天」页签\n"+
			"★ ★ 卡片 163,457 vs 饼图 331,373，差 2.08 倍」）。\n"+
			"★ ★ 修法：queryBoardTrendsLogs 读 request_logs_with_current_month\n"+
			"★ ★（与 queryBoardSummaryLogs / queryBoardPiesFacts 同一张视图）。\n"+
			"★ SQL:\n%s", sql)
	}
	for _, forbidden := range []string{"usage_facts", "request_stats_minute", "request_stats_dim_minute"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("★ 趋势主路径里仍出现 %s ⇒ ★ 它要么是**预聚合表**（写入时已剔除\n"+
				"★ ★ 探测流量 ⇒ 结构上无法响应 include_probes ⇒ 口径分裂会回来），\n"+
				"★ ★ 要么是**另一个宇宙**（事实族与日志的历史覆盖起点不同 ⇒ 长窗口\n"+
				"★ ★ 必然分裂）。\n"+
				"★ （降级路径请另建 queryBoardTrendsMinute，勿混在主路径里。）\n"+
				"★ SQL:\n%s", forbidden, sql)
		}
	}
}

// ── 门 ②：饼图主路径读请求级日志，且**不再**读预聚合维表 ─────────────────
//
// ★★★ 2026-11-24 修订：★ 改成**行为门**（查实际执行的 SQL）。★★★
// ★★★ 起因同上：★ SQL 抽成 boardPieFactsSQL 后，源文本门误报。
func TestBoardPiesReadRequestLevelLogsNotDimRollup(t *testing.T) {
	where := "r.ts >= $1 AND r.ts < $2 AND r.request_status IN ('success','failure','rate_limited')"
	sql := boardPieFactsSQL(where, "r.credits_charged")
	if !strings.Contains(sql, "FROM request_logs_with_current_month") {
		t.Errorf("★ 饼图主路径没有读请求级日志 ⇒ ★ 那 7 个维度（agent_name/client_ip/\n"+
			"★ identity_hash/model/tenant/provider/error_kind）就没有请求级来源了。\n"+
			"★ SQL:\n%s", sql)
	}
	if strings.Contains(sql, "request_stats_dim_minute") {
		t.Errorf("★ 饼图主路径里仍出现 request_stats_dim_minute ⇒ ★ 它是**预聚合维表**，\n"+
			"★ ★★ 写入时已剔除探测流量 ⇒ include_probes 开关对饼图无效 ⇒ 口径分裂会回来。\n"+
			"★ SQL:\n%s", sql)
	}
}

// ── 门 ③（★ 核心不变量）：英雄区 / 趋势 / 饼图**整页同源同谓词** ─────────────
//
// ★★★ 2026-11-24 第二轮升级（★ 从「英雄区+趋势同源」升级为「整页三条读链同源」）：★★★
//
// ★★★ 第一轮的门只钉「英雄区 + 趋势」两条读链 ⇒ ★★ **漏掉了饼图** ⇒ ★★ 于是
// ★★★ 出现了「卡片与趋势相等、但饼图是另一个宇宙」的分裂：
// ★★★ 生产实测 2026-11-24「近30天」页签：卡片/趋势 163,457（事实族）vs
// ★★★ 饼图 331,373（请求级日志）⇒ **同一页面 2.08 倍**。
// ★★★ ⇒ 本门现在钉**三条**读链。
//
// ★★★ 三层写法（与上一轮同思路）：★
// ★★★  ① **结构层**：三条读链**必须**调用同一个 where 构造器 boardLogsWhereTerminal
// ★★★     ⇒ 「同谓词」不再靠人记得同步，而是**编译器级**的一致。
// ★★★  ② **行为层**：三条 SQL **逐字包含**同一段 WHERE，且都读**同一张视图**。
// ★★★  ③ **开关层**：include_probes=1 时三段的 WHERE **都不含**排除探测谓词。
func TestBoardHeroTrendAndPiesShareSameSourceAndPredicate(t *testing.T) {
	tr := boardTimeRange{}

	// ① 结构层：三条读链必须共用构造器。
	code := readAdminFile(t, boardQueriesFile)
	for _, fn := range []string{
		"func (h *Handler) queryBoardSummaryLogs(",
		"func (h *Handler) queryBoardTrendsLogs(",
		"func (h *Handler) queryBoardPiesFacts(",
	} {
		body := goFuncBody(code, fn)
		if body == "" {
			t.Fatalf("★ 找不到 %s —— 本门的前提（函数名/文件）需要重新评估", fn)
		}
		if !strings.Contains(body, "boardLogsWhereTerminal(") {
			t.Errorf("★ %s 没有调用共用的 boardLogsWhereTerminal ⇒ ★★ 三条读链的 WHERE\n"+
				"★ ★★ 又变成「各写各的」⇒ 探测口径可能悄悄分叉 ⇒ 同页两个数字。\n"+
				"★ ★★ 修法：三条都改用 boardLogsWhereTerminal（它同时决定视图、终态集与谓词）。", fn)
		}
	}

	// ② 行为层：三条 SQL 逐字含同一段 WHERE，且读同一张视图。
	where, _ := boardLogsWhereTerminal(tr, "", false)
	statements := map[string]string{
		"英雄区": boardSummaryLogsSQL(where, "r.credits_charged"),
		"趋势":  boardTrendLogsSQL("date_trunc('hour', r.ts)", where, "r.credits_charged"),
		"饼图":  boardPieFactsSQL(where, "r.credits_charged"),
	}
	for name, sql := range statements {
		if !strings.Contains(sql, "FROM request_logs_with_current_month") {
			t.Errorf("★ %s 必须读 request_logs_with_current_month ⇒ ★★ 三条读链必须是**同一个宇宙**\n"+
				"★ ★★ （事实族与日志的历史覆盖起点不同 ⇒ 长窗口必然分裂）。\n"+
				"★ ★★ SQL:\n%s", name, sql)
		}
		if strings.Contains(sql, "usage_facts") {
			t.Errorf("★ %s 的 SQL 里出现 usage_facts ⇒ ★★ 它是**另一个宇宙** ⇒ 同页两个数。\n"+
				"★ ★★ SQL:\n%s", name, sql)
		}
		if !strings.Contains(sql, where) {
			t.Errorf("★ %s 的 SQL 里没有逐字包含共用 WHERE ⇒ ★★ 三条读链口径不一致 ⇒\n"+
				"★ ★★ 卡片 / 趋势 / 饼图会是三个数 ⇒ 用户对不上。\n"+
				"★ ★★ 共用 WHERE: %s\n★ ★★ SQL:\n%s", name, where, sql)
		}
	}

	// ③ 开关层：include_probes=1 时三段都不该再带排除探测谓词。
	excludePred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "r", "r", "r")
	exWhere, _ := boardLogsWhereTerminal(tr, "", false)
	if !strings.Contains(exWhere, excludePred) {
		t.Errorf("★ 排除探测口径的 WHERE 里没有逐字包含 bg.ProbeTrafficExclusionPredicateView\n"+
			"★ ⇒ 本门的比较基准变了。\n★ 实际 WHERE: %s", exWhere)
	}
	incWhere, _ := boardLogsWhereTerminal(tr, "", true)
	if strings.Contains(incWhere, excludePred) {
		t.Errorf("★ include_probes=1 时 WHERE 里仍有排除探测谓词 ⇒ ★★ 切「含探测」时\n"+
			"★ ★★ 卡片 / 趋势 / 饼图仍停在排除探测口径 ⇒ 页面两个数字对不上。\n"+
			"★ 实际 WHERE: %s", incWhere)
	}
}

// ── 门 ④：include_probes 必须真的贯通到饼图与趋势 ────────────────────────
func TestBoardProbeScopeReachesPiesAndTrends(t *testing.T) {
	code := readAdminFile(t, boardHandlerFile)
	if !strings.Contains(code, "resolveBoardPies(ctx, filterTenant, tr, includeProbes)") {
		t.Errorf("★ handler 没有把 includeProbes 传给 resolveBoardPies ⇒\n" +
			"★ ★★ 看板切「含探测」时饼图仍是排除探测口径 ⇒ 页面两个数字对不上。")
	}
	if !strings.Contains(code, "resolveBoardTrends(ctx, filterTenant, tr, providerID, includeProbes)") {
		t.Errorf("★ handler 没有把 includeProbes 传给 resolveBoardTrends ⇒\n" +
			"★ ★★ 看板切「含探测」时趋势图仍是排除探测口径 ⇒ 页面两个数字对不上。")
	}
	queries := readAdminFile(t, boardQueriesFile)
	for _, fn := range []string{
		"func (h *Handler) queryBoardTrendsLogs(",
		"func (h *Handler) queryBoardPiesFacts(",
	} {
		body := goFuncBody(queries, fn)
		if body == "" {
			t.Errorf("★ 找不到 %s", fn)
			continue
		}
		if !strings.Contains(body, "includeProbes") {
			t.Errorf("★ %s 的函数体里没有 includeProbes ⇒ 它拿不到探测口径 ⇒ 开关对它无效。", fn)
		}
	}
}

// ── 门 ⑤：饼图降级到维表时**必须打降级标记** ────────────────────────────
func TestBoardPiesFallbackIsMarkedDegraded(t *testing.T) {
	code := readAdminFile(t, boardQueriesFile)
	body := goFuncBody(code, "func (h *Handler) queryBoardPies(")
	if body == "" {
		t.Fatalf("找不到 queryBoardPies")
	}
	if !strings.Contains(body, "degraded.Keys = append(degraded.Keys, key)") {
		t.Errorf("★ 饼图降级到预聚合维表时没有登记降级维度 ⇒ ★ 载荷不会带 degraded 标记 ⇒\n" +
			"★ ★★ 而维表恒为排除探测口径 ⇒ 「口径不同」被静默吞掉（这正是本轮要消除的那类缺陷）。")
	}
}

// ── 门 ⑥（★ 跨文件）：饼图 dim_key 表达式必须与 rollup 逐条一致 ──────────
//
// ★★ 这条门防的是「两处各写各的」：★ dim_key 的取值形态是**维表与看板之间的契约**
// ★★ —— 前端按这些 key 渲染图例，降级路径（读维表）也用同一套 key。
// ★★ 两边不一致 ⇒ 同一个页面在「主路径 → 降级路径」切换时图例全变 ⇒ 用户看到的是
// ★★ 「同一张图换了套名字」，而不是「数据变了」。
var rollupDimExprRe = regexp.MustCompile(`\{"([a-z_]+)",\s*` + "`([^`]+)`" + `\}`)

// TestBoardPieDimKeyExpressionsMatchRollup 比对 admin 侧的 dim_key 表达式与
// bg/stats_minute_rollup.go 的 rollupDimQueries。
func TestBoardPieDimKeyExpressionsMatchRollup(t *testing.T) {
	raw, err := os.ReadFile(boardRollupFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", boardRollupFile, err)
	}
	rollup := map[string]string{}
	for _, m := range rollupDimExprRe.FindAllStringSubmatch(string(raw), -1) {
		rollup[m[1]] = m[2]
	}
	if len(rollup) == 0 {
		t.Fatalf("★ 从 %s 里没解析出任何 rollupDimQueries 条目 ⇒ 本门的量具失效", boardRollupFile)
	}

	code := readAdminFile(t, boardQueriesFile)
	body := goFuncBody(code, "var boardPieDimKeyExprs = map[string]string{")
	if body == "" {
		// goFuncBody 只认 func 声明，这里退回按整文件找 map 块。
		body = code
	}

	for dimType, expr := range boardPieDimKeyExprs {
		want, ok := rollup[dimType]
		if !ok {
			t.Errorf("★ admin 侧声明了维度 %q，但 %s 的 rollupDimQueries 里没有 ⇒ "+
				"★ 两边的维度集合已漂移（要么删掉 admin 侧，要么给 rollup 加这个维度）。",
				dimType, boardRollupFile)
			continue
		}
		got := strings.ReplaceAll(expr, " ", "")
		if got != strings.ReplaceAll(want, " ", "") {
			t.Errorf("★ 维度 %q 的 dim_key 表达式与 %s 不一致：\n"+
				"★   admin 侧 : %s\n"+
				"★   rollup 侧: %s\n"+
				"★ ★★ 两边不一致 ⇒ 主路径与降级路径的图例形态会不同 ⇒ 降级时「同一张图换了套名字」。",
				dimType, boardRollupFile, expr, want)
		}
	}
}

// TestBoardPieDimTypesMatchRollup 核 admin 侧声明的维度集合是 rollup 的子集。
func TestBoardPieDimTypesMatchRollup(t *testing.T) {
	raw, err := os.ReadFile(boardRollupFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", boardRollupFile, err)
	}
	rollup := map[string]bool{}
	for _, m := range rollupDimExprRe.FindAllStringSubmatch(string(raw), -1) {
		rollup[m[1]] = true
	}
	got := make([]string, 0, len(boardPieDimTypes))
	for _, d := range boardPieDimTypes {
		got = append(got, d.dimType)
		if !rollup[d.dimType] {
			t.Errorf("★ 维度 %q 不在 %s 的 rollupDimQueries 里 ⇒ 降级路径取不到它。",
				d.dimType, boardRollupFile)
		}
	}
	sort.Strings(got)
	// ★ 看板的 7 个维度（agent_name/client_ip/identity_hash/model/error_kind/
	// ★ tenant/provider）必须**齐全** —— 少一个 ⇒ 页面少一块图。
	want := []string{"agent_name", "client_ip", "error_kind", "identity_hash", "model", "provider", "tenant"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("★ 看板饼图维度集合变了：\n"+
			"★   实际: %v\n"+
			"★   期望: %v\n"+
			"★ （少一个维度 = 页面少一块饼图；多一个 = 有维度的响应键没人消费。）",
			got, want)
	}
}

// ── 门 ⑦（★ 生产事故回写）：饼图主路径**不得用相关子查询（LATERAL）** ────────
//
// ★★★ 这条门来自一次**真实的生产事故**（2026-11-24 真源统一轮上线后）：
// ★★★
// ★★★   ERROR: input of anonymous composite types is not implemented (0A000)
// ★★★   LOCATION: record_in, rowtypes.c:103
// ★★★
// ★★★ 现象：饼图主路径（读请求级日志）**每次调用都报错** ⇒ 载荷带 degraded ⇒
// ★★★ 页面静默退回预聚合维表 ⇒ 「切含探测时饼图不动」的老症状原封不动地回来了。
// ★★★
// ★★★ 根因（**非数据问题，是协议问题**）：初版用
// ★★★ `CROSS JOIN LATERAL (VALUES …) AS d(dim_type, dim_key)` 展开 7 个维度。
// ★★★ 生产走 pgx = **扩展协议 + 预备语句**；在通用计划（generic plan）下，
// ★★★ PG 必须把 LATERAL 子查询里对外层的引用参数化，而外层是**视图** RTE
// ★★★ （request_logs_with_current_month）⇒ 整行引用是**匿名 record 类型**
// ★★★ ⇒ 生成 RECORD 类型的 Param ⇒ 执行期调用 record_in ⇒ 报错。
// ★★★ ★★ 它**非确定性**：同一条 SQL 有时过、有时炸（取决于当次选了通用/定制计划），
// ★★★ ★★ 而 psql 简单协议**永远**不会触发 ⇒ ★★ 用 psql 手测根本测不出来。
// ★★★
// ★★★ 修法：用 `CROSS JOIN unnest(ARRAY[…]) AS d(dim_type)`（常量数组 ⇒ 函数扫描
// ★★★ ⇒ 无外层引用 ⇒ 无 RECORD Param）+ `CASE d.dim_type` 取 dim_key。
// ★★★
// ★★ 为什么这条门只查「有没有 LATERAL」就够：★ LATERAL 是这里**唯一**的相关子查询
// ★★ 形态（credits 的标量子查询不含外层行引用 ⇒ 无 RECORD Param ⇒ 生产实测无罪）。
// ★★ 但真库门（-tags=integration）才是「能不能执行」的最终证据。
func TestBoardPieSQLHasNoCorrelatedLateral(t *testing.T) {
	where := "r.ts >= $1 AND r.ts < $2 AND r.request_status IN ('success','failure','rate_limited')"
	sql := boardPieFactsSQL(where, "r.credits_charged")

	if strings.Contains(strings.ToUpper(sql), "LATERAL") {
		t.Errorf("★ 饼图主路径 SQL 里出现了 LATERAL（相关子查询）⇒\n"+
			"★ ★★ 生产实测（2026-11-24，211）：★ 用 LATERAL (VALUES …) 展开维度时，\n"+
			"★ ★★ 预备语句（生产走扩展协议）在通用计划下会生成 RECORD 类型的 Param ⇒\n"+
			"★ ★★ `ERROR: input of anonymous composite types is not implemented` (0A000)\n"+
			"★ ★★ ⇒ 饼图每次调用都降级 ⇒ 「切含探测时饼图不动」的老症状原样复发。\n"+
			"★ ★★ 修法：用 unnest(ARRAY[…]) + CASE d.dim_type 展开维度（无相关子查询）。\n"+
			"★ ★★ SQL:\n%s", sql)
	}
	if !strings.Contains(sql, "CROSS JOIN unnest(ARRAY[") {
		t.Errorf("★ 饼图主路径没有用 unnest(ARRAY[…]) 展开维度 ⇒ 维度展开形态被改回去了。\n"+
			"★ SQL:\n%s", sql)
	}
	if !strings.Contains(sql, "CASE d.dim_type") {
		t.Errorf("★ 饼图主路径没有用 CASE d.dim_type 取 dim_key ⇒ 维度取值形态被改回去了。\n"+
			"★ SQL:\n%s", sql)
	}
	// ★ 7 个维度必须都在 CASE 分支里 —— 少一个 ⇒ 页面少一块饼图。
	for _, d := range boardPieDimTypes {
		if !strings.Contains(sql, "WHEN '"+d.dimType+"' THEN ") {
			t.Errorf("★ 维度 %q 不在饼图 SQL 的 CASE 分支里 ⇒ 该维度不会出现在饼图里。", d.dimType)
		}
	}
}
