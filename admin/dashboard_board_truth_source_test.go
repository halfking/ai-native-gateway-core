package admin

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// dashboard_board_truth_source_test.go
//
// 看板「真源统一」守卫 —— 2026-11-24 真源统一轮新增。
//
// ── 要钉住什么 ──────────────────────────────────────────────────────────
//
// ★★★ 看板（/api/admin/dashboard/board）过去有**三条读链、三个源**：
//
//	① 英雄区汇总  queryBoardSummaryFacts → FROM usage_facts            （请求级）
//	② 饼图        queryDimPie            → FROM request_stats_dim_minute（★预聚合★）
//	③ 趋势        queryBoardTrends       → FROM request_stats_minute    （★预聚合★）
//
// ★★★ 而两张**预聚合表在写入时就剔除了探测流量**（bg/stats_minute_rollup.go：
// ★★★ rollup 表没有 origin_stage / quality_flags 列，「到行被汇总时请求级探测
// ★★★ 标记已经没了」⇒ 只能在写入时排除）⇒ ★★★ **结构上无法响应 include_probes**。
// ★★★ ⇒ 后果（生产实测，211，7 天窗口）：★ 看板切「含探测」时
// ★★★   英雄区卡片 = 189,008 笔，趋势图/饼图 = 75,878 笔 ⇒ ★★★ **同一页面两个数字**。
//
// ★★★ 本门钉住统一后的形态：
//
//	① 英雄区 = usage_facts（不变：它要与对账快照逐字相等，不能动）
//	② 趋势   = usage_facts（★ 与英雄区同表同探测谓词 ⇒ 各桶之和与卡片逐项相等）
//	③ 饼图   = 请求级日志（★ 那 7 个维度的唯一请求级来源；
//	          ★ usage_facts 的 39 列里没有 agent_name/client_ip/identity_hash，
//	          ★ 也没有 outbound_model/client_model ⇒ 饼图不能用 usage_facts）
//
// ── 为什么门要逐条钉，而不是只钉「读了哪张表」──────────────────────────
//
// ★ 关键不变量有三条，缺一条就会出现「数字对不上」：
//
//	★ ① **同表**：趋势必须与英雄区读同一张表（否则卡片 ≠ Σ趋势桶）。
//	★ ② **同谓词**：两边必须用**同一个**探测谓词（traffic_class='business'）。
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

// ── 门 ①：趋势主路径读 usage_facts，且**不再**读预聚合分钟表 ─────────────
//
// ★★★ 2026-11-24 修订：★ 本门改成**行为门**（查**实际执行的 SQL**，不是查某个
// ★★★ 函数体里的源文本）。★★★
// ★★★ 起因：★ SQL 被抽成 boardTrendFactsSQL 之后，源文本门**误报**（函数体里
// ★★★ 不再有 "FROM usage_facts"）⇒ ★★ 这就是「量具量错了东西」的第三例：
// ★★★ 门量的不是「PG 实际收到什么」，而是「源码长什么样」。
// ★★★ 现在门直接问：**PG 会收到哪条语句**。
func TestBoardTrendReadsUsageFactsNotMinuteRollup(t *testing.T) {
	where, _ := boardFactsWhere("", boardTimeRange{}, false)
	sql := boardTrendFactsSQL("date_trunc('hour', occurred_at)", where)
	if !strings.Contains(sql, "FROM usage_facts") {
		t.Errorf("★ 趋势主路径没有读 usage_facts ⇒ ★ 趋势与英雄区不同源 ⇒ "+
			"★ 卡片 ≠ Σ趋势桶（生产实测曾出现「卡片 189,008 笔、趋势图 75,878 笔」）。\n"+
			"★ 修法：queryBoardTrends 改读 usage_facts（与 queryBoardSummaryFacts 同表）。\n"+
			"★ SQL:\n%s", sql)
	}
	if strings.Contains(sql, "request_stats_minute") {
		t.Errorf("★ 趋势主路径里仍出现 request_stats_minute ⇒ ★ 它是**预聚合表**，\n"+
			"★ ★★ 写入时已剔除探测流量 ⇒ 结构上无法响应 include_probes ⇒ 口径分裂会回来。\n"+
			"★ （降级路径请另建 queryBoardTrendsMinute，勿混在主路径里。）\n"+
			"★ SQL:\n%s", sql)
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

// ── 门 ③（★ 核心不变量）：趋势与英雄区**同表同谓词** ───────────────────
//
// ★★★ 2026-11-24 修订（★ 从「比对文本」升级为「结构保证 + 行为验证」）：★★★
//
// ★★★ 旧写法：★ 两边函数体里各找一遍 "traffic_class = 'business'" 字符串。
// ★★★ ★★ 缺陷：★ 那只是**文本各写各的**——两边**完全可能**用同一段文字、
// ★★★ ★★ 却作用在不同语义的列上；而且任何一边改写谓词而忘了另一边，
// ★★★ ★★ 门只在文本层面察觉 ⇒ 仍可能「门绿而口径裂」。
//
// ★★★ 新写法（三层）：★
// ★★★  ① **结构层**：英雄区与趋势**必须**调用同一个构造器 boardFactsWhere ⇒
// ★★★     「同谓词」不再靠人记得同步，而是**编译器级**的一致。
// ★★★  ② **行为层**：英雄区 SQL 与趋势 SQL 里**逐字包含**同一段 WHERE。
// ★★★  ③ **开关层**：include_probes=1 时两边的 WHERE **都不含**排除探测谓词。
func TestBoardTrendAndHeroShareSameProbePredicate(t *testing.T) {
	tr := boardTimeRange{}

	// ① 结构层：两边必须共用构造器。
	code := readAdminFile(t, boardQueriesFile)
	for _, fn := range []string{
		"func (h *Handler) queryBoardSummaryFacts(",
		"func (h *Handler) queryBoardTrends(",
	} {
		body := goFuncBody(code, fn)
		if body == "" {
			t.Fatalf("★ 找不到 %s —— 本门的前提（函数名/文件）需要重新评估", fn)
		}
		if !strings.Contains(body, "boardFactsWhere(") {
			t.Errorf("★ %s 没有调用共用的 boardFactsWhere ⇒ ★★ 英雄区与趋势的 WHERE\n"+
				"★ ★★ 又变成「两边各写各的」⇒ 探测口径可能悄悄分叉。\n"+
				"★ ★★ 修法：两边都改用 boardFactsWhere（它同时决定表与谓词）。", fn)
		}
	}

	// ② 行为层：排除探测口径下，两条 SQL 逐字含同一段 WHERE。
	const want = "traffic_class = 'business'"
	heroWhere, _ := boardFactsWhere("", tr, false)
	trendWhere, _ := boardFactsWhere("", tr, false)
	heroSQL := boardSummaryFactsSQL(heroWhere)
	trendSQL := boardTrendFactsSQL("date_trunc('hour', occurred_at)", trendWhere)
	if !strings.Contains(heroSQL, "FROM usage_facts") || !strings.Contains(trendSQL, "FROM usage_facts") {
		t.Errorf("★ 英雄区与趋势必须读**同一张表** usage_facts ⇒\n"+
			"★ ★★ 英雄区 SQL:\n%s\n★ 趋势 SQL:\n%s", heroSQL, trendSQL)
	}
	if !strings.Contains(heroWhere, want) {
		t.Errorf("★ 英雄区汇总没有「%s」这个排除探测谓词 ⇒ 本门的比较基准变了。", want)
	}
	if !strings.Contains(trendSQL, heroWhere) {
		t.Errorf("★ 趋势 SQL 里没有逐字包含英雄区的 WHERE ⇒ ★★ 两边口径不一致 ⇒\n"+
			"★ ★★ 卡片（排除探测）与趋势（可能含探测）会是两个数 ⇒ 用户对不上。\n"+
			"★ 英雄区 WHERE: %s\n★ 趋势 SQL:\n%s", heroWhere, trendSQL)
	}

	// ③ 开关层：include_probes=1 时两边都不该再带排除探测谓词。
	incHero, _ := boardFactsWhere("", tr, true)
	incTrend, _ := boardFactsWhere("", tr, true)
	if strings.Contains(incHero, want) {
		t.Errorf("★ include_probes=1 时英雄区 WHERE 里仍有「%s」⇒ 含探测口径下仍被剔除。", want)
	}
	if strings.Contains(incTrend, want) {
		t.Errorf("★ include_probes=1 时趋势 WHERE 里仍有「%s」⇒ ★★ 切「含探测」时\n"+
			"★ ★★ 趋势仍停在排除探测口径 ⇒ 页面两个数字对不上。", want)
	}
}

// ── 门 ④：include_probes 必须真的贯通到饼图与趋势 ────────────────────────
func TestBoardProbeScopeReachesPiesAndTrends(t *testing.T) {
	code := readAdminFile(t, boardHandlerFile)
	if !strings.Contains(code, "resolveBoardPies(ctx, filterTenant, tr, includeProbes)") {
		t.Errorf("★ handler 没有把 includeProbes 传给 resolveBoardPies ⇒\n"+
			"★ ★★ 看板切「含探测」时饼图仍是排除探测口径 ⇒ 页面两个数字对不上。")
	}
	if !strings.Contains(code, "resolveBoardTrends(ctx, filterTenant, tr, providerID, includeProbes)") {
		t.Errorf("★ handler 没有把 includeProbes 传给 resolveBoardTrends ⇒\n"+
			"★ ★★ 看板切「含探测」时趋势图仍是排除探测口径 ⇒ 页面两个数字对不上。")
	}
	queries := readAdminFile(t, boardQueriesFile)
	for _, fn := range []string{
		"func (h *Handler) queryBoardTrends(",
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
		t.Errorf("★ 饼图降级到预聚合维表时没有登记降级维度 ⇒ ★ 载荷不会带 degraded 标记 ⇒\n"+
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
