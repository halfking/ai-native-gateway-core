package admin

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// dashboard_board_probe_scope_test.go —— 2026-11-20 口径对齐轮的钉测，
// 2026-11-24 第二轮（整页切请求级日志）改钉日志侧谓词。
//
// 历史契约（2026-11-20，事实族当主源时）：
//
//	board「不含探测」== /admin/reconciliation 内部对账 == usage_facts traffic_class='business'
//	board「含探测」  == /admin/reconciliation 供应商对账 == usage_facts 全量
//
// ★★★ 2026-11-24 第二轮订正（owner 拍板「整页切请求级日志」）：★★★
//
// ★★★ 英雄区主源不再读 usage_facts ⇒ ① ② 两条契约**不再成立**（事实族在
// ★★★ 2026-11-10 13:37 丢了 ≈35.2 万行 ⇒ 它自己就是少算的一方）。
// ★★★ 现在看板三条读链（英雄区/趋势/饼图）读**请求级日志**，探测谓词用
// ★★★ markers 谓词（bg.ProbeTrafficExclusionPredicateView）。
//
// 本门钉四件事：
//
//	① 排除臂生成的 WHERE 里逐字含 markers 谓词（与饼图/降级路径同一把尺）；
//	② include 臂**没有**排除探测谓词；
//	③ include 模式不落回分钟表（分钟表无探测行，落回 = 拿业务数冒充全量数）；
//	④ handleDashboardBoard 解析 include_probes 且该模式绕过 Redis 缓存
//	  （缓存键不含探测口径，混用会互相污染两种口径的载荷）。
// ★★★ 2026-11-24 修订（★ 源文本门 → 行为门 + 结构门）：★★★
//
// ★★★ 起因：★ 英雄区的 WHERE 与 SQL 被抽到共用构造器之后，源文本门**误报**
// ★★★ （函数体里不再有那两段文字）⇒ ★★ 第三例「量具量错了东西」。
// ★★★ 现在这门问的是**实际生成给 PG 的语句**。
// TestBoardSummaryUsesRequestLevelProbePredicate 钉英雄区的探测口径。
//
// ★★★ 2026-11-24 第二轮（owner 拍板「整页切请求级日志」）：★★★
// ★★★ 英雄区主源已从 usage_facts 换成请求级日志 ⇒ 探测谓词随之从
// ★★★ 「traffic_class='business'」（事实族列）换成 markers 谓词
// ★★★ （bg.ProbeTrafficExclusionPredicateView，日志列）⇒ ★★ 两把尺不同，
// ★★★ ★★ 边缘行上可能与对账分叉（代价已在 queryBoardSummary 的注释里登记）。
// ★★★ 但事实族在 2026-11-10 13:37 丢了 ≈35.2 万行 ⇒ 继续拿它当看板真源
// ★★★ 等于「用一个已知少算 2 倍的源当真相」。
//
// 本门钉四件事：
//
//	① 排除臂生成的 WHERE 里逐字含 markers 谓词（与饼图/降级路径同一把尺）；
//	② include 臂**没有**排除探测谓词；
//	③ 生成的 SQL 确实读请求级日志视图；
//	④ 结构层：queryBoardSummaryLogs 必须走共用构造器（否则它可能自己另写一套）。
func TestBoardSummaryUsesRequestLevelProbePredicate(t *testing.T) {
	tr := boardTimeRange{}
	excludePred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "r", "r", "r")

	// ① 排除臂：实测生成的 WHERE。
	exWhere, _ := boardLogsWhereTerminal(tr, "", false)
	if !strings.Contains(exWhere, excludePred) {
		t.Error("queryBoardSummaryLogs 的排除臂必须逐字含 bg.ProbeTrafficExclusionPredicateView\n" +
			"（请求级日志上没有 traffic_class 列 ⇒ 只有 markers 谓词可用；\n" +
			"它与饼图主路径、看板降级路径是同一把尺 ⇒ 三条读链不会分叉）。\n" +
			"★ 实测生成的 WHERE: " + exWhere)
	}
	// ② 含探测臂：同一个构造器在 includeProbes=true 下**不得**追加排除谓词。
	incWhere, _ := boardLogsWhereTerminal(tr, "", true)
	if strings.Contains(incWhere, excludePred) {
		t.Error("排除探测谓词必须条件化在 !includeProbes 下：include 模式读的是\n" +
			"全量口径，加了排除谓词会让两种切换状态返回同一组数字。\n" +
			"★ 实测生成的 WHERE: " + incWhere)
	}
	// ③ 主源必须是请求级日志视图。
	heroSQL := boardSummaryLogsSQL(exWhere, "r.credits_charged")
	if !strings.Contains(heroSQL, "FROM request_logs_with_current_month") {
		t.Error("queryBoardSummaryLogs 必须读 request_logs_with_current_month（整页同源）。\n★ SQL:\n" + heroSQL)
	}
	if strings.Contains(heroSQL, "usage_facts") {
		t.Error("queryBoardSummaryLogs 不得再读 usage_facts ⇒ ★★ 它是**另一个宇宙**\n" +
			"（覆盖起点 2026-11-10 13:38 vs 日志更早 ⇒ 长窗口必然分裂）。\n★ SQL:\n" + heroSQL)
	}
	// ④ 结构层：必须走共用构造器，否则「同谓词」只是文本巧合。
	raw, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	facts := goFuncBodyStrict(code, "func (h *Handler) queryBoardSummaryLogs(")
	if facts == "" {
		t.Fatal("queryBoardSummaryLogs not found in dashboard_board_queries.go")
	}
	if !strings.Contains(facts, "boardLogsWhereTerminal(") {
		t.Error("queryBoardSummaryLogs 必须调用共用的 boardLogsWhereTerminal ⇒ ★★ 否则它可能\n" +
			"★★ 自己另写一套探测谓词，与趋势/饼图悄悄分叉。")
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
	// 主源必须是请求级日志；分钟表只是 exclude 模式的降级链。
	logsAt := strings.Index(dispatcher, "queryBoardSummaryLogs(")
	minuteAt := strings.Index(dispatcher, "queryBoardSummaryMinute(")
	if logsAt < 0 || minuteAt < 0 || logsAt > minuteAt {
		t.Error("queryBoardSummary 必须先试请求级日志（queryBoardSummaryLogs）、\n" +
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
