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
func TestBoardTrendReadsUsageFactsNotMinuteRollup(t *testing.T) {
	code := readAdminFile(t, boardQueriesFile)
	body := goFuncBody(code, "func (h *Handler) queryBoardTrends(")
	if body == "" {
		t.Fatalf("找不到 queryBoardTrends —— 本门的前提（函数名/文件）需要重新评估")
	}
	if !strings.Contains(body, "FROM usage_facts") {
		t.Errorf("★ 趋势主路径没有读 usage_facts ⇒ ★ 趋势与英雄区不同源 ⇒ "+
			"★ 卡片 ≠ Σ趋势桶（生产实测曾出现「卡片 189,008 笔、趋势图 75,878 笔」）。\n"+
			"★ 修法：queryBoardTrends 改读 usage_facts（与 queryBoardSummaryFacts 同表）。")
	}
	if strings.Contains(body, "request_stats_minute") {
		t.Errorf("★ 趋势主路径里仍出现 request_stats_minute ⇒ ★ 它是**预聚合表**，\n"+
			"★ ★★ 写入时已剔除探测流量 ⇒ 结构上无法响应 include_probes ⇒ 口径分裂会回来。\n"+
			"★ （降级路径请另建 queryBoardTrendsMinute，勿混在主路径里。）")
	}
}

// ── 门 ②：饼图主路径读请求级日志，且**不再**读预聚合维表 ─────────────────
func TestBoardPiesReadRequestLevelLogsNotDimRollup(t *testing.T) {
	code := readAdminFile(t, boardQueriesFile)
	body := goFuncBody(code, "func (h *Handler) queryBoardPiesFacts(")
	if body == "" {
		t.Fatalf("找不到 queryBoardPiesFacts —— 本门的前提（函数名/文件）需要重新评估")
	}
	if !strings.Contains(body, "FROM request_logs_with_current_month") {
		t.Errorf("★ 饼图主路径没有读请求级日志 ⇒ ★ 那 7 个维度（agent_name/client_ip/\n"+
			"★ identity_hash/model/tenant/provider/error_kind）就没有请求级来源了。")
	}
	if strings.Contains(body, "request_stats_dim_minute") {
		t.Errorf("★ 饼图主路径里仍出现 request_stats_dim_minute ⇒ ★ 它是**预聚合维表**，\n"+
			"★ ★★ 写入时已剔除探测流量 ⇒ include_probes 开关对饼图无效 ⇒ 口径分裂会回来。")
	}
}

// ── 门 ③（★ 核心不变量）：趋势与英雄区**同表同谓词** ───────────────────
func TestBoardTrendAndHeroShareSameProbePredicate(t *testing.T) {
	code := readAdminFile(t, boardQueriesFile)
	hero := goFuncBody(code, "func (h *Handler) queryBoardSummaryFacts(")
	trend := goFuncBody(code, "func (h *Handler) queryBoardTrends(")
	if hero == "" || trend == "" {
		t.Fatalf("找不到 queryBoardSummaryFacts 或 queryBoardTrends")
	}
	const want = "traffic_class = 'business'"
	if !strings.Contains(hero, want) {
		t.Errorf("★ 英雄区汇总没有「%s」这个排除探测谓词 ⇒ 本门的比较基准变了。", want)
	}
	if !strings.Contains(trend, want) {
		t.Errorf("★ 趋势没有用**同一个**排除探测谓词「%s」⇒ ★★ 两边口径不一致 ⇒\n"+
			"★ ★★ 卡片（排除探测）与趋势（可能含探测）会是两个数 ⇒ 用户对不上。\n"+
			"★ 修法：趋势改读 usage_facts 并用与英雄区逐字相同的谓词。", want)
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
