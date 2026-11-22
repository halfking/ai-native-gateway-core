package admin

// usage_trend_deep_link_filters_test.go —— 用量趋势页「深链条件真的过滤数据」的判据。
//
// # 这道门测什么
//
// 2026-11-23 实测取证（211 现役，浏览器实点）：
//
//	`/admin/usage-trends?end_user_id=anonymous` ⇒ 页面**根本不认这个条件**
//	（条件区全是「全部」、Top 10 用户仍是全站 3 个用户、Top 5 模型仍是 5 个）
//	⇒ ★ URL 承诺的条件与页面呈现的数据**不一致**
//	  （"界面显示的 ≠ 数据反映的" —— 与上一轮修的「排行卡片点击过滤失效」同族）。
//
// 而排行卡片跳请求日志页带的就是这五个参数 ⇒ **同一个链接在两页必须同义**。
//
// # 本门钉住三件事
//
//  1. **接线**：排行端点（admin/usage.go）与用量趋势明细档
//     （admin/usage_trend_series.go）都必须把五维谓词**拼进 WHERE**
//     ——「读了参数却没作用在行上」是这类修复最常见的假绿形态。
//  2. **别名正确**：同一套身份口径要能在两个 FROM 源上复用
//     （排行/日志侧别名 rl＝canonical 视图；用量趋势明细侧别名 r＝会话族原生源）
//     ⇒ 别名没跟着换 ⇒ SQL 引用不存在的列 ⇒ 端点 500。
//  3. **占位符连续**：五维谓词的 $n 必须接着已有参数往后排
//     ⇒ 编号错位 ⇒ 42P18 类型推断失败（上一轮真库门第一次跑就抓到过同族问题）。
//
// # 判据纪律：每条断言都必须**能在被测对象坏掉时变红**。
import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// usageTrendFilterFixture 返回一份五维齐全的过滤值（各维一个可辨识值）。
func usageTrendFilterFixture() rankingDimensionFilters {
	return rankingDimensionFilters{
		clientModel:     "cli-1",
		endUserID:       "dev-A",
		apiKeyPrefix:    "sk-aaa",
		applicationCode: "app-1",
		taskType:        "chat",
	}
}

// TestUsageTrendRankingClausesUseSharedCore 钉住排行端点与明细档**复用同一份谓词**。
//
// ★ 为什么重要：两处各写一份 ⇒ 改了一边忘了另一边 ⇒
//
//	「卡片上的条数」与「点进去看到的条数」对不上（上一轮修的正是这个病）。
func TestUsageTrendRankingClausesUseSharedCore(t *testing.T) {
	usageSrc := readRankingFilterSrc(t, "usage.go")
	if !strings.Contains(usageSrc, `buildRankingDimensionClausesFor(trendFilters.dims, "rl"`) {
		t.Error("admin/usage.go 的排行查询必须复用 buildRankingDimensionClausesFor（不得另抄一份谓词）")
	}
	// ★ API Key 那条查询自带 JOIN（占位符编号另起）⇒ 也必须单独加一份过滤
	if strings.Count(usageSrc, `buildRankingDimensionClausesFor(trendFilters.dims, "rl"`) < 2 {
		t.Error("admin/usage.go 的 API Key 排行查询（自带 JOIN）也必须加五维过滤（否则该卡片口径与其它卡片打架）")
	}

	seriesSrc := readRankingFilterSrc(t, "usage_trend_series.go")
	if !strings.Contains(seriesSrc, `buildRankingDimensionClausesFor(f.dims, "r"`) {
		t.Error("admin/usage_trend_series.go 的明细档 SQL 必须复用 buildRankingDimensionClausesFor（别名 r＝会话族原生源）")
	}
	// ★ 序列端点与模型清单端点**两处**都要过滤（只过滤一处 ⇒ 图表与模型下拉口径打架）
	if strings.Count(seriesSrc, `buildRankingDimensionClausesFor(f.dims, "r"`) < 2 {
		t.Error("admin/usage_trend_series.go 的序列 SQL 与模型清单 SQL 都要加五维过滤")
	}
}

// TestUsageTrendSourceSwitchesToDetailWhenDimsSet 钉住「设了五维就必须换读面」。
//
// ★ 汇总档（request_stats_dim_minute）只有 model 一个维度 ⇒ 设了客户端/用户/项目/
//
//	任务却留在汇总档 ⇒ 筛选被**静默忽略**（URL 带条件、图表是全站）。
func TestUsageTrendSourceSwitchesToDetailWhenDimsSet(t *testing.T) {
	base := usageTrendFilters{}
	if got := usageTrendSource(base); got != "request_stats_dim_minute" {
		t.Errorf("无条件时应走汇总档，实得 %q", got)
	}
	for _, tc := range []struct {
		name string
		dims rankingDimensionFilters
	}{
		{"client_model", rankingDimensionFilters{clientModel: "cli-1"}},
		{"end_user_id", rankingDimensionFilters{endUserID: "dev-A"}},
		{"api_key_prefix", rankingDimensionFilters{apiKeyPrefix: "sk-aaa"}},
		{"application_code", rankingDimensionFilters{applicationCode: "app-1"}},
		{"task_type", rankingDimensionFilters{taskType: "chat"}},
	} {
		f := usageTrendFilters{dims: tc.dims}
		if got := usageTrendSource(f); got != "request_logs_with_current_month" {
			t.Errorf("设了 %s ⇒ 必须切到明细档（汇总档没有这些列 ⇒ 筛选会被静默忽略），实得 %q", tc.name, got)
		}
	}
}

// TestRankingDimensionClausesForRendersAliasAwarePredicates 钉住**别名真的参与渲染**。
//
// ★ 这条是「两个 FROM 源复用同一套口径」的地基：
//
//	别名没跟着换 ⇒ 谓词引用了另一个 FROM 源里的列 ⇒ 端点 500。
func TestRankingDimensionClausesForRendersAliasAwarePredicates(t *testing.T) {
	f := usageTrendFilterFixture()

	clauses, args, next := buildRankingDimensionClausesFor(f, "r", 3)
	if len(clauses) != 5 {
		t.Fatalf("五维过滤应渲染 5 条谓词，实得 %d 条", len(clauses))
	}
	if len(args) != 5 {
		t.Errorf("五维过滤应绑定 5 个参数，实得 %d", len(args))
	}
	if next != 8 {
		t.Errorf("nextArgIdx 必须接着参数往后排（3 + 5），实得 %d", next)
	}
	for _, c := range clauses {
		if strings.Contains(c, "rl.") {
			t.Errorf("别名 r 的谓词里不得出现 rl.：%s", c)
		}
		if !strings.Contains(c, "r.") {
			t.Errorf("别名 r 的谓词必须引用 r. 前缀：%s", c)
		}
	}
	// end_user_id 的两条腿（原值命中 OR 身份表达式命中）都要在
	joined := strings.Join(clauses, "\n")
	if !strings.Contains(joined, "r.end_user_id = $") {
		t.Errorf("end_user_id 谓词必须有「原值精确命中」这条腿（旧链接/直链要还能用）：%s", joined)
	}
	if !strings.Contains(joined, strings.ReplaceAll(userIdentityExpr, "rl.", "r.")) {
		t.Errorf("end_user_id 谓词必须内嵌共享身份表达式（别名 r 形态）")
	}
	// ★ 普通维度必须用共享的 dimensionIdentityExpr（空串/NULL 归 'unknown'）
	if !strings.Contains(joined, "COALESCE(NULLIF(r.client_model, ''), 'unknown')") {
		t.Errorf("client_model 谓词必须走共享身份表达式（否则与排行口径对不上）：%s", joined)
	}
	// api_key_prefix 必须是前缀语义
	if !strings.Contains(joined, "starts_with(r.api_key_prefix, $") {
		t.Errorf("api_key_prefix 谓词必须是 starts_with 前缀语义：%s", joined)
	}
}

// TestRankingDimensionClausesForIsAliasSensitive 钉住「换别名 ⇒ 输出必须跟着变」。
//
// ★ 这是**变异自证**的等价物：如果别名被忽略（写死 rl 或写死 r），
//
//	本条立刻红 ⇒ 它证明上一条的「别名生效」不是恒真。
func TestRankingDimensionClausesForIsAliasSensitive(t *testing.T) {
	f := usageTrendFilterFixture()
	rlClauses, _, _ := buildRankingDimensionClausesFor(f, "rl", 3)
	rClauses, _, _ := buildRankingDimensionClausesFor(f, "r", 3)
	if len(rlClauses) != len(rClauses) {
		t.Fatalf("两个别名的谓词条数必须一致，实得 %d vs %d", len(rlClauses), len(rClauses))
	}
	for i := range rlClauses {
		if rlClauses[i] == rClauses[i] {
			t.Errorf("第 %d 条谓词在两个别名下必须不同（别名没生效 ⇒ SQL 会引用另一个 FROM 源的列）：%s", i, rlClauses[i])
		}
	}
	// rl 形态必须与既有（改动前）逐字一致 ⇒ 回归保护
	joined := strings.Join(rlClauses, "\n")
	if !strings.Contains(joined, "COALESCE(NULLIF(rl.client_model, ''), 'unknown')") {
		t.Errorf("rl 别名的输出必须与既有形态逐字一致（回归保护）：%s", joined)
	}
	if !strings.Contains(joined, userIdentityExpr) {
		t.Errorf("rl 别名的 end_user_id 谓词必须逐字内嵌共享常量 userIdentityExpr：%s", joined)
	}
}

// TestUsageTrendFiltersReadFiveDimensionParams 钉住**读参数**这一半。
//
// ★ 「读了参数却没拼进 WHERE」是假绿的头号形态 ⇒ 读与拼必须分别有判据
//
//	（读 ⇒ 本条；拼 ⇒ TestUsageTrendRankingClausesUseSharedCore）。
func TestUsageTrendFiltersReadFiveDimensionParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/usage/trend-series?client_model=cli-1&end_user_id=dev-A"+
			"&api_key_prefix=sk-aaa&application_code=app-1&task_type=chat", nil)
	f := usageTrendFiltersFromRequest(req)
	if f.dims.clientModel != "cli-1" {
		t.Errorf("client_model 未读到，实得 %q", f.dims.clientModel)
	}
	if f.dims.endUserID != "dev-A" {
		t.Errorf("end_user_id 未读到，实得 %q", f.dims.endUserID)
	}
	if f.dims.apiKeyPrefix != "sk-aaa" {
		t.Errorf("api_key_prefix 未读到，实得 %q", f.dims.apiKeyPrefix)
	}
	if f.dims.applicationCode != "app-1" {
		t.Errorf("application_code 未读到，实得 %q", f.dims.applicationCode)
	}
	if f.dims.taskType != "chat" {
		t.Errorf("task_type 未读到，实得 %q", f.dims.taskType)
	}
	if f.dims.empty() {
		t.Error("五维都有值时 empty() 必须为 false")
	}
	// 无参数 ⇒ 全空（缺省 = 不过滤 ⇒ 与改动前逐字等价）
	empty := usageTrendFiltersFromRequest(httptest.NewRequest(http.MethodGet, "/api/admin/usage/trend-series", nil))
	if !empty.dims.empty() {
		t.Error("无参数时五维必须全空（缺省 = 不过滤）")
	}
}

// TestRankingDimensionFilterClausesPlaceholderOrder 钉住占位符**连续**。
//
// ★ 编号错位 ⇒ PG 42P18（参数个数与占位符对不上）⇒ 端点 500。
//
//	（上一轮真库门第一次跑就因测试自己编号错位而抓到同族问题。）
//
// ★ 维度顺序＝谓词渲染顺序：client_model → end_user_id → api_key_prefix
//
//	→ application_code → task_type ⇒ 第 i 条用 $(5+i)。
func TestRankingDimensionFilterClausesPlaceholderOrder(t *testing.T) {
	f := usageTrendFilterFixture()
	clauses, args, next := buildRankingDimensionClausesFor(f, "rl", 5)
	if len(clauses) != len(args) {
		t.Fatalf("谓词条数 %d 与参数个数 %d 必须一致", len(clauses), len(args))
	}
	// 五个维度（end_user_id 的两条腿**共用**一个占位符）共占 5 个参数
	if next != 10 {
		t.Errorf("五个维度共占 5 个参数 ⇒ nextArgIdx 应为 10，实得 %d", next)
	}
	for i, c := range clauses {
		want := 5 + i
		if !strings.Contains(c, "$"+strconv.Itoa(want)) {
			t.Errorf("第 %d 条谓词应引用 $%d：%s", i, want, c)
		}
	}
	// end_user_id 的两条腿必须共用同一个占位符（否则参数个数与占位符对不上）
	userClause := clauses[1]
	if strings.Count(userClause, "$6") != 2 {
		t.Errorf("end_user_id 的两条腿必须共用同一个占位符 $6（实得 %d 次）：%s", strings.Count(userClause, "$6"), userClause)
	}
	if strings.Contains(userClause, "$7") {
		t.Errorf("end_user_id 的谓词不得提前占用 $7（那是 api_key_prefix 的）：%s", userClause)
	}
}
