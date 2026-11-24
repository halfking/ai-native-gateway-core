package admin

import (
	"os"
	"strings"
	"testing"
)

// dashboard_board_probe_scope_test.go —— 2026-11-20 口径对齐轮的钉测。
//
// owner 对拍契约（211 真库 7 天逐日实证过）：
//
//	board「不含探测」== /admin/reconciliation 内部对账 == usage_facts traffic_class='business'
//	board「含探测」  == /admin/reconciliation 供应商对账 == usage_facts 全量
//	request-logs 默认业务 lane == usage_facts business（探针物理隔离后同源）
//
// 修复前 board 主源是 request_stats_minute：写入者是「游标 + 30min 有界
// resweep」扫描器，部署窗口的迟到行永久漏统（真库实测部署小时 -1/-21 笔），
// 且它根本没有探测行，include 口径无从谈起。修复后主源 = usage_facts。
//
// 本门钉四件事：
//
//	① queryBoardSummaryFacts 的 exclude 臂用的是 traffic_class='business'
//	  （与 reportrollup 内部对账同一列），而不是 markers 谓词再算一遍；
//	② include 臂**没有** class 过滤（= 供应商对账全量）；
//	③ include 模式不落回分钟表（分钟表无探测行，落回 = 拿业务数冒充全量数）；
//	④ handleDashboardBoard 解析 include_probes 且该模式绕过 Redis 缓存
//	  （缓存键不含探测口径，混用会互相污染两种口径的载荷）。
// ★★★ 2026-11-24 修订（★ 源文本门 → 行为门 + 结构门）：★★★
//
// ★★★ 起因：★ 英雄区的 WHERE 与 SQL 被抽到 boardFactsWhere / boardSummaryFactsSQL
// ★★★ 之后，源文本门**误报**（函数体里不再有那两段文字）⇒ ★★ 第三例「量具量错了东西」。
// ★★★ 现在这门问的是**实际生成给 PG 的语句**：
// ★★★   ① 排除臂生成的 WHERE 里确实有 traffic_class='business'；
// ★★★   ② 含探测臂生成的 WHERE 里**没有**它；
// ★★★   ③ 生成的 SQL 确实读 usage_facts；
// ★★★   ④ 结构层：queryBoardSummaryFacts 必须走共用构造器（否则它可能自己另写一套）。
func TestBoardSummaryFactsUsesTrafficClassCaliber(t *testing.T) {
	tr := boardTimeRange{}

	// ① 排除臂：实测生成的 WHERE。
	exWhere, _ := boardFactsWhere("", tr, false)
	if !strings.Contains(exWhere, `traffic_class = 'business'`) {
		t.Error("queryBoardSummaryFacts 的排除臂必须用 traffic_class='business'。\n" +
			"对账快照（reportrollup.grainDaySQL 的 businessOnly 分支）按这一列分面；\n" +
			"换回 bg.ProbeTrafficExclusionPredicateView（markers 口径）会在边缘行上\n" +
			"与内部对账分叉——board 与对账要逐字相等就得用同一列。\n" +
			"★ 实测生成的 WHERE: " + exWhere)
	}
	// ② 含探测臂：同一个构造器在 includeProbes=true 下**不得**追加 class 过滤。
	incWhere, _ := boardFactsWhere("", tr, true)
	if strings.Contains(incWhere, `traffic_class`) {
		t.Error("traffic_class 过滤必须条件化在 !includeProbes 下：include 模式\n" +
			"读的是供应商对账口径（usage_facts 全量），加 class 过滤会让两种\n" +
			"切换状态返回同一组数字。\n" +
			"★ 实测生成的 WHERE: " + incWhere)
	}
	// ③ 主源必须是 usage_facts。
	heroSQL := boardSummaryFactsSQL(exWhere)
	if !strings.Contains(heroSQL, "FROM usage_facts") {
		t.Error("queryBoardSummaryFacts 必须读 usage_facts（对账真源）。\n★ SQL:\n" + heroSQL)
	}
	// ④ 结构层：必须走共用构造器，否则「同谓词」只是文本巧合。
	raw, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	facts := goFuncBodyStrict(code, "func (h *Handler) queryBoardSummaryFacts(")
	if facts == "" {
		t.Fatal("queryBoardSummaryFacts not found in dashboard_board_queries.go")
	}
	if !strings.Contains(facts, "boardFactsWhere(") {
		t.Error("queryBoardSummaryFacts 必须调用共用的 boardFactsWhere ⇒ ★★ 否则它可能\n" +
			"★★ 自己另写一套探测谓词，与趋势悄悄分叉。")
	}
}

func TestBoardSummaryDispatcherOrder(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))

	dispatcher := goFuncBodyStrict(code, "func (h *Handler) queryBoardSummary(")
	if dispatcher == "" {
		t.Fatal("queryBoardSummary not found in dashboard_board_queries.go")
	}
	// ③ include 模式不得落回分钟表。
	if !strings.Contains(dispatcher, "if includeProbes") {
		t.Error("queryBoardSummary 必须对 includeProbes 分流：分钟表不含探测行，\n" +
			"include 模式落回它等于拿业务数冒充「含探测」的全量数——那正是\n" +
			"本轮要修掉的「board 与供应商对账对不上」的另一种形态。")
	}
	// 主源必须是 facts；分钟表只是 exclude 模式的降级链。
	factsAt := strings.Index(dispatcher, "queryBoardSummaryFacts(")
	minuteAt := strings.Index(dispatcher, "queryBoardSummaryMinute(")
	if factsAt < 0 || minuteAt < 0 || factsAt > minuteAt {
		t.Error("queryBoardSummary 必须先试 usage_facts（queryBoardSummaryFacts）、\n" +
			"分钟表（queryBoardSummaryMinute）只能作为 exclude 模式的降级链。\n" +
			"顺序颠倒会把「部署日漏行」的分钟表重新变成主源。")
	}
}

func TestHandleDashboardBoardProbeScopeWiring(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board.go")
	if err != nil {
		t.Fatalf("read dashboard_board.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))

	handler := goFuncBodyStrict(code, "func (h *Handler) handleDashboardBoard(")
	if handler == "" {
		t.Fatal("handleDashboardBoard not found in dashboard_board.go")
	}
	if !strings.Contains(handler, `queryBool(r, "include_probes")`) {
		t.Error("handleDashboardBoard 必须解析 include_probes 查询参数。")
	}
	// ④ 缓存旁路：cache 分支的条件里必须带上 !includeProbes。
	for _, ln := range strings.Split(handler, "\n") {
		if strings.Contains(ln, "h.boardCache != nil") {
			if !strings.Contains(ln, "!includeProbes") {
				t.Errorf("缓存分支条件缺少 !includeProbes：%q\n"+
					"Redis 看板缓存的键只含 scope/days/provider，存的是默认（业务）口径\n"+
					"载荷；include 请求不绕过缓存会把含探测的载荷写进业务键（或反过来），\n"+
					"两种口径互相污染。", strings.TrimSpace(ln))
			}
			return
		}
	}
	t.Error("handleDashboardBoard 里没找到 boardCache 分支。")
}
