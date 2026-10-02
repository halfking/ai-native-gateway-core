//go:build !integration

package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// 凭据热图的探测排除谓词钉桩（2026-10-02）。
//
// 这条查询在线上 500 了至少两周，而整套测试全绿：
//
//	ERROR: column rl.origin_stage does not exist   (SQLSTATE 42703)
//
// 三个独立的原因叠在一起，任何一个单独存在都抓不到：
//
//  1. R50（2026-09-21）把**物理表**谓词内联进一条以冻结 113 列视图为源的查询。
//     R49（2026-09-20）已经识别并修过同一件事，还写下了视图变体，但 R50 的调用面
//     守卫 bg.TestProbeExclusionPredicateCallSitesR50 的文件清单只含 bg 包 7 个文件。
//  2. exclude_self_test 缺省 true，所以裸调用和前端调用同走必错路径，不是潜伏缺陷。
//  3. TestBuildHeatmapSQL_GuardsAgainst42803Regression 只对 SQL **字符串**做断言，
//     而 pgxmock 匹配的是查询字符串——它从不让 PostgreSQL 解析。于是「这条 SQL 根本
//     跑不起来」这一整类缺陷对单测隐形（与 admin/sql_literal_validity_test.go 记录的
//     ''::jsonb 是同一个根因，第三次复发）。
//
// 结构性的一层由 TestNoPhysicalOnlyColumnsInViewSourcedSQL 覆盖，但它按单条字面量
// 判定，而这里的视图引用与谓词分属两条字面量、运行时才被 strings.Join 拼起来——
// 那个形状静态门看不见，由 TestCredentialHeatmapSQL_ExecutesOnRealDatabase 兜。
// 本文件补的是最便宜的一层：源码里就不许再出现那个拼写。

func TestHeatmapProbeExclusionUsesSharedViewPredicate(t *testing.T) {
	src, err := os.ReadFile("credential_monitor_heatmap.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	// Comments are stripped first, and that is not cosmetic: the fix's own
	// rationale quotes the bad literal (`NOT ('probe' = ANY(quality_flags))`)
	// in order to explain why it is wrong, so an unstripped scan makes this
	// guard red on correct code — the same failure mode that made the earlier
	// version of sql_literal_validity_test.go ban `''::text`. A guard that
	// fires on the explanation of the fix gets deleted instead of obeyed.
	code := stripGoComments(string(src))

	// 1) 越列禁令已**上移**到 TestNoPhysicalPredicateOnViewSource（全仓、判据
	//    是「物理谓词 × 视图源」而不是某一个列名）。这里原来禁 `rl.origin_stage`
	//    的理由是「该列不在 113 列契约内 → 必 42703」——815 之后这条理由**不成立**
	//    （origin_stage 已进契约），但禁令本身仍然必要，只是理由换了：谓词能在
	//    视图上跑不等于跑得对，它的 quality_flags 臂在 session 分臂恒 NULL ⇒
	//    探测流量不再被排除。保留一个理由已失实的断言，等于让后来人按错误理由
	//    去「优化」它。
	//
	// 2) 无 COALESCE 的 quality_flags 臂：`NOT ('probe' = ANY(NULL))` 求值为 NULL 而
	//    不是 TRUE，会把整个 session 分臂静默丢掉（实测 40,225/40,275 行）。
	if strings.Contains(code, "NOT ('probe' = ANY(") {
		t.Errorf("credential_monitor_heatmap.go 出现无 COALESCE 的 quality_flags 臂 " +
			"NOT ('probe' = ANY(...))：quality_flags 为 NULL 时该表达式是 NULL 而非 TRUE，" +
			"整行被 WHERE 丢弃，视图的 session 分臂因此对业务热图隐形。用视图变体谓词。")
	}

	// 3) 正面钉桩：必须用 bg 导出的共享拼写，理由写在视图变体的注释里。
	if !strings.Contains(code, "bg.ProbeTrafficExclusionPredicateView") {
		t.Errorf("credential_monitor_heatmap.go 必须引用 bg.ProbeTrafficExclusionPredicateView；" +
			"本地再抄一份就没有漂移守卫——R50 的内联正是这样漏过整套测试的。")
	}
}

// 谓词本身的形状钉桩：三条 format 动词要传三个别名。
//
// bg/probe_policy.go 原注释写「pass it twice」，而常量有三个 %s。少传一个不会编译
// 失败，只会渲染出 %!s(MISSING) 混进 SQL，然后在离真正错误很远的地方报语法错。
//
// 谓词的**内容**形状（origin_actor 臂在、origin_stage 臂不在）由
// TestViewPredicateHasNoPhysicalArm 守，理由写在那边——815 之后
// 「origin_stage 不在契约内」这个理由已经不成立，留在本文件会误导后来人。
func TestViewPredicateFormatArity(t *testing.T) {
	const p = bg.ProbeTrafficExclusionPredicateView
	if got := strings.Count(p, "%s"); got != 3 {
		t.Fatalf("view predicate has %d format verbs, want 3 — a caller passing fewer gets "+
			"%%!s(MISSING) baked into the SQL instead of a compile error", got)
	}
}

func excerptAround(s string, i int) string {
	start := i - 120
	if start < 0 {
		start = 0
	}
	end := i + 120
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
