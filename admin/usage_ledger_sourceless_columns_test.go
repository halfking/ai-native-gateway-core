package admin

// usage_ledger_sourceless_columns_test.go —— 回归门：禁止查询引用
// usage_ledger 根本不存在的列。
//
// ## 这个门挡住的是什么
//
// b9a8baba4 给 usage_enhanced.go 引入了三处引用：queryPeriodStats 的
// COUNT(DISTINCT ul.gw_session_id)、cache-economics 的 compression_strategy
// FILTER、cost-trend 的 ul.work_type / session_summaries 连接。这些列在
// usage_ledger 上从来不存在（它是计费宽表，20 列，见 information_schema）。
//
// 后果不是 500，而是更坏的东西：
//
//	period-compare    → 200 + 全部 0  （IsSchemaBehindError 吞掉 42703）
//	cache-economics   → 200 + 全部 0  （同上；UsageCost.vue 6 个指标全渲染 0）
//	cost-trend?intent → 500           （该端点只降级 42P01，不降级 42703）
//
// 第一条尤其恶劣：用户看到「本月花了 0 美元」，而库里实际有 1139 美元。
// 一个**永久性的代码缺陷**被降级机制伪装成了**临时性的迁移延迟**。
//
// ## 判据的鉴别力（必读）
//
// 这份门如果只会「全绿」，那它比没有门更坏 —— 它会让人以为这块有人管。
// 所以每条判据都配了**反向对照**：把当初真实的错误实现喂给检测器，要求它
// 必须报红。检测器若报不出违规，TestDetectorRejectsTheOriginalBug 就会失败。
//
//   PlanValidator   → 喂「work_type 挂在 ul. 上」等合成方案
//   SourceScanner   → 喂 b9a8baba4 的原始 SQL 文本
//   PeriodStats 反射 → 断言它真的看得见字段（空结构体必须判红）

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// sourcelessColumns 是 usage_ledger 没有、只有 request_logs 有的列。
// 实测来源：2026-10-03 information_schema.columns
//
//	usage_ledger(20) = request_id, ts, tenant_id, application_id, api_key_id,
//	  end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
//	  prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
//	  total_tokens, cost_usd, latency_ms, success, error_kind, rate_multiplier
var sourcelessColumns = []string{
	"gw_session_id",
	"compression_strategy",
	"work_type",
}

// validateCostTrendPlan 报告一个取数方案是否违规。
//
// 规则只有一条，但它正是那条被违反过的：分组列若绑定到计费宽表，而该列在
// 计费宽表上不存在，就是违规。检���器不读数据库 —— 它判的是「方案自称的基表
// 与它引用的列是否自洽」，这是静态可判的。
func validateCostTrendPlan(p costTrendPlan) []string {
	var bad []string
	isLedger := strings.HasPrefix(p.BaseTable, "usage_ledger")
	alias := p.BaseAlias
	if !isLedger {
		return nil
	}
	// 违规可能藏在两处：分组列本身，或 JOIN 条件。
	// 最初的 b9a8baba4 缺陷两处都有 —— work_type 在 GroupColumn 里，
	// intent 的 gw_session_id 在 JoinClause 里（"ON ss.session_key = ul.gw_session_id"）。
	// 只查 GroupColumn 的检测器会漏掉后者，那正是 TestDetectorRejectsTheOriginalBug
	// 当场抓出来的。两条都要扫。
	for _, col := range sourcelessColumns {
		needle := alias + "." + col
		if where := fragmentContaining(p.GroupColumn, needle); where != "" {
			bad = append(bad, "group_by="+p.GroupBy+" 把请求侧列 "+col+
				" 绑定到了计费宽表别名 "+alias+"（"+where+"）："+p.BaseTable+" 无此列 → 42703")
		}
		if where := fragmentContaining(p.JoinClause, needle); where != "" {
			bad = append(bad, "group_by="+p.GroupBy+" 的 JOIN 引用了计费宽表上不存在的列 "+
				needle+"（"+where+"）："+p.BaseTable+" 无此列 → 42703")
		}
	}
	return bad
}

// fragmentContaining 返回 needle 所在的片段（整段 SQL 压成一行），未命中返回 ""。
func fragmentContaining(fragment, needle string) string {
	if fragment == "" || !strings.Contains(fragment, needle) {
		return ""
	}
	return strings.Join(strings.Fields(fragment), " ")
}

// ── 正向：五个维度的归属必须与实测 schema 一致 ──────────────────────────

func TestPlanCostTrend_DimensionProvenance(t *testing.T) {
	// 期望值来自实测，不是从实现里抄的：
	//   model/provider/api_key 的列只在计费宽表侧被查询过（实测 200）；
	//   work_type/intent 的列只在 request_logs 上（实测 ledger 侧 500）。
	want := map[string]struct {
		baseTable   string
		baseAlias   string
		requestSide bool
	}{
		"model":     {"usage_ledger_with_current_month ul", "ul", false},
		"provider":  {"usage_ledger_with_current_month ul", "ul", false},
		"api_key":   {"usage_ledger_with_current_month ul", "ul", false},
		"work_type": {"request_logs rl", "rl", true},
		"intent":    {"request_logs rl", "rl", true},
	}

	for dim, exp := range want {
		p, ok := planCostTrend(dim)
		if !ok {
			t.Errorf("维度 %q 必须可用 —— UsageCost.vue 的下拉框提供它", dim)
			continue
		}
		if p.BaseTable != exp.baseTable {
			t.Errorf("维度 %q 基表 = %q, want %q", dim, p.BaseTable, exp.baseTable)
		}
		if p.BaseAlias != exp.baseAlias {
			t.Errorf("维度 %q 基表别名 = %q, want %q", dim, p.BaseAlias, exp.baseAlias)
		}
		if p.RequestSide != exp.requestSide {
			t.Errorf("维度 %q RequestSide = %v, want %v", dim, p.RequestSide, exp.requestSide)
		}
		if bad := validateCostTrendPlan(p); len(bad) > 0 {
			t.Errorf("维度 %q 方案违规: %v", dim, bad)
		}
	}
}

// 维度登记表不能悄悄少一项：少一项 = 那个维度从 API 消失。
// 这里把 UI 下拉框的真实取值写死（UsageCost.vue:386-388）。
func TestPlanCostTrend_CoversEveryDropdownOption(t *testing.T) {
	for _, dim := range []string{"model", "provider", "intent"} {
		if _, ok := planCostTrend(dim); !ok {
			t.Errorf("下拉框可选项 %q 未注册到 planCostTrend —— 点了就 400", dim)
		}
	}
	// 未知维度必须被拒，否则拼错的 group_by 会静默走 default 分支。
	if _, ok := planCostTrend("work_typo"); ok {
		t.Error("未知维度 work_typo 不应通过 planCostTrend")
	}
}

// ── 反向对照：检测器必须能抓住当初真实的错误实现 ────────────────────────

// TestDetectorRejectsTheOriginalBug 是本门存在的前提。
//
// 下面三条是 b9a8baba4 的真实写法（从 git 历史还原）。如果 validateCostTrendPlan
// 对它们报不出违规，说明检测器是恒绿的装饰品 —— 那比没有门更坏，因为它给出
// 「这块有人管」的错觉。这条测试就是防这件事的。
func TestDetectorRejectsTheOriginalBug(t *testing.T) {
	original := []struct {
		name    string
		comment string
		plan    costTrendPlan
	}{
		{
			name: "cost-trend work_type 挂在计费宽表上（原始缺陷）",
			plan: costTrendPlan{
				GroupBy: "work_type", BaseTable: "usage_ledger_with_current_month ul",
				BaseAlias: "ul", GroupColumn: "ul.work_type",
			},
		},
		{
			name: "cost-trend intent 连 session_summaries 但基表是 ledger",
			plan: costTrendPlan{
				GroupBy: "intent", BaseTable: "usage_ledger_with_current_month ul",
				BaseAlias: "ul", GroupColumn: "ss.user_intent",
				JoinClause: " LEFT JOIN session_summaries ss ON ss.session_key = ul.gw_session_id",
			},
		},
		{
			name: "queryPeriodStats 聚合 gw_session_id",
			plan: costTrendPlan{
				GroupBy: "period_stats", BaseTable: "usage_ledger_with_current_month ul",
				BaseAlias: "ul", GroupColumn: "COUNT(DISTINCT ul.gw_session_id)",
			},
		},
		{
			name: "cache-economics FILTER compression_strategy",
			plan: costTrendPlan{
				GroupBy: "cache_economics", BaseTable: "usage_ledger_with_current_month ul",
				BaseAlias: "ul",
				GroupColumn: "COUNT(*) FILTER (WHERE ul.compression_strategy IS NOT NULL " +
					"AND ul.compression_strategy <> '')",
			},
		},
	}

	for _, tc := range original {
		bad := validateCostTrendPlan(tc.plan)
		if len(bad) == 0 {
			t.Errorf("检测器漏报了「%s」—— 本门恒绿，等于没有门", tc.name)
		}
	}

	// 阳性对照：合法写法必须不报违规，否则检测器就是「怎么都红」，同样没有鉴别力。
	legal := costTrendPlan{
		GroupBy: "work_type", BaseTable: "request_logs rl", BaseAlias: "rl",
		GroupColumn: "rl.work_type",
	}
	if bad := validateCostTrendPlan(legal); len(bad) > 0 {
		t.Errorf("检测器误报了合法方案: %v", bad)
	}
}

// ── 源码扫描：拦住任何绕过 planner 的直接引用 ────────────────────────────

// stripGoCommentsKeepLines 去掉 Go 注释，但**保留换行**，因此行号不变；
// 并且不碰反引号原始字符串的内容。
//
// 为什么不直接复用本包的 stripGoComments（被另外 7 个审计门共用）：
// 它用正则实现，两个性质与本门的需要相反 ——
//  1. 把整段块注释替换成一个空格，**丢掉了换行**，报错里的 @line 会指错位置；
//  2. 不识别反引号原始字符串，会吃掉 SQL 里的 `//`（例如 URL），
//     那是**漏报真缺陷**的方向。
//
// 改共用实现等于一次性挪动 7 条门的判据，不该顺手做。该函数的局限记在这里，
// 留给需要它的那一轮单独评估。
//
// 为什么必须去注释：PeriodStats 的文档注释里写着「原实现
// COUNT(DISTINCT ul.gw_session_id)」——那是解释缺陷来源的说明，不是可执行 SQL。
// 扫描器若连注释一起扫，第一次跑就会把这段说明判成违规。
// 一条会误报的门比没有门更坏：它只会训练下一个人去改注释、加绕过标记，
// 而不是修真正的查询。TestScannerIgnoresCommentsButCatchesCode 是它的反向对照。
func stripGoCommentsKeepLines(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	const (
		code = iota
		lineComment
		blockComment
		rawString
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = lineComment
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = blockComment
				i++
			case c == '`':
				state = rawString
				out.WriteByte(c)
			default:
				out.WriteByte(c)
			}
		case lineComment:
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}
		case blockComment:
			if c == '\n' {
				out.WriteByte(c) // 保留行号
			}
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state = code
				i++
			}
		case rawString:
			out.WriteByte(c)
			if c == '`' {
				state = code
			}
		}
	}
	return out.String()
}

// scanForLedgerSourcelessRefs 找出「以计费宽表别名限定、但宽表没有的列」。
// 只看可执行代码，不看注释 —— 见 stripGoComments。
func scanForLedgerSourcelessRefs(src string) []string {
	var hits []string
	code := stripGoCommentsKeepLines(src)
	for _, col := range sourcelessColumns {
		needle := "ul." + col
		if i := strings.Index(code, needle); i >= 0 {
			line := 1 + strings.Count(code[:i], "\n")
			hits = append(hits, needle+" @line "+strconv.Itoa(line))
		}
	}
	return hits
}

// TestUsageEnhanced_SourceHasNoLedgerSourcelessRefs 扫真实源码。
// 与上面的反向对照配对：扫描器必须在合成样本上报红，才能相信它在本文件上报绿。
func TestUsageEnhanced_SourceHasNoLedgerSourcelessRefs(t *testing.T) {
	src, err := os.ReadFile("usage_enhanced.go")
	if err != nil {
		t.Fatalf("read usage_enhanced.go: %v", err)
	}

	// 反向对照先行：把原始缺陷文本喂给扫描器。
	badSample := `SELECT COUNT(DISTINCT ul.gw_session_id) FROM usage_ledger_with_current_month ul`
	if len(scanForLedgerSourcelessRefs(badSample)) == 0 {
		t.Fatal("扫描器漏报了原始缺陷样本 —— 下面那次扫描报绿没有意义")
	}

	if hits := scanForLedgerSourcelessRefs(string(src)); len(hits) > 0 {
		t.Errorf("usage_enhanced.go 仍有以计费宽表别名限定的无源列: %v\n"+
			"usage_ledger 是 20 列计费宽表，不含这三列；引用它必然 42703，"+
			"而 IsSchemaBehindError 会把整条查询失败降级成 200 + 全 0。", hits)
	}
}

// TestScannerIgnoresCommentsButCatchesCode 是扫描器的鉴别力证明，四种情形都要判对：
//
//	注释里的违规   → 不报（否则门恒红，只会诱导人改注释而不是修查询）
//	SQL 里的违规   → 报（这是真缺陷，正是本门要挡的）
//	代码里的违规   → 报
//	干净的代码     → 不报
//
// 少任何一条，这条门就没有鉴别力。
func TestScannerIgnoresCommentsButCatchesCode(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantHit bool
	}{
		{
			name:    "行注释里的说明不算违规",
			src:     "// 原实现 COUNT(DISTINCT ul.gw_session_id) 必然 42703\nvar x = 1\n",
			wantHit: false,
		},
		{
			name:    "块注释里的说明不算违规",
			src:     "/* 曾引用 ul.compression_strategy */\nvar x = 1\n",
			wantHit: false,
		},
		{
			name:    "反引号 SQL 里的引用是真违规",
			src:     "q := `SELECT COUNT(DISTINCT ul.gw_session_id) FROM usage_ledger ul`\n",
			wantHit: true,
		},
		{
			name:    "普通代码里的引用是真违规",
			src:     "col := \"ul.work_type\"\n",
			wantHit: true,
		},
		{
			name:    "合法写法不报",
			src:     "q := `SELECT rl.work_type, rl.gw_session_id FROM request_logs rl`\n",
			wantHit: false,
		},
	}

	for _, tc := range cases {
		got := len(scanForLedgerSourcelessRefs(tc.src)) > 0
		if got != tc.wantHit {
			t.Errorf("%s: 扫描器报违规=%v, want %v", tc.name, got, tc.wantHit)
		}
	}
}

// TestStripGoCommentsPreservesLineNumbers 保证去注释不破坏行号，
// 否则报错信息里的 @line 会指向错误的行 —— 报告指错位置比不报还糟。
func TestStripGoCommentsPreservesLineNumbers(t *testing.T) {
	src := "// line1\n/*\nline3\n*/\nvar x = `raw // not a comment`\n"
	got := stripGoCommentsKeepLines(src)
	if n := strings.Count(got, "\n"); n != strings.Count(src, "\n") {
		t.Errorf("去注释后换行数变了：%d != %d", n, strings.Count(src, "\n"))
	}
	if !strings.Contains(got, "raw // not a comment") {
		t.Error("反引号里的内容被当成注释吃掉了 —— SQL 活在反引号里，这样会漏报真缺陷")
	}
}

// ── PeriodStats 契约 ────────────────────────────────────────────────────

// TestPeriodStats_HasNoUniqueSessions 钉住「删字段」这个决定。
//
// 反向对照：先断言反射确实看得见 PeriodStats 的字段。若结构体被清空，
// 下面那个断言会永远通过 —— 那是恒绿，必须先排除。
func TestPeriodStats_HasNoUniqueSessions(t *testing.T) {
	typ := reflect.TypeOf(PeriodStats{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		seen[strings.Split(tag, ",")[0]] = true
	}

	// 阳性对照：反射必须看得见真实字段。若这条都不过，说明上面的循环
	// 根本没生效，后面「字段不存在」的断言就没有鉴别力。
	for _, mustExist := range []string{
		"period", "total_cost_usd", "total_requests", "total_tokens",
		"avg_cost_per_req", "unique_models",
	} {
		if !seen[mustExist] {
			t.Fatalf("反射看不到 PeriodStats.%s —— 检测器失效，下面「字段已删除」"+
				"的断言会恒绿而没有意义", mustExist)
		}
	}

	// unique_sessions 必须已删除：它需要 COUNT(DISTINCT gw_session_id)，
	// 而 usage_ledger 无 gw_session_id，换源后在交互预算内也算不出来
	// （request_logs 实测 30 天窗口 72s）。无消费方 + 算不出来 + 返回 0
	// 会与「真的是 0」无法区分 —— 正是本轮要消灭的缺陷。
	if seen["unique_sessions"] {
		t.Error("PeriodStats.unique_sessions 回来了：它无法计算，全库无渲染点，" +
			"留着只会诱导下一个人用 0 冒充真实值")
	}
}
