package main

// `rejection_reasons` 的**族表**判据：拿真实提取器 + 真实实抓夹具跑一遍，
// 钉住产出的类目，并断言**没有未归族**。
//
// # 为什么已有判据不够
//
// `rejection_reasons_test.go` 里那三条用的是**测试作者手写的 warning 字符串**。
// 它们验的是归一化函数本身，与「真实提取器今天发出什么措辞」无关。
//
// 后果在本轮真的发生了：新写的 `reasonFamilies` 族表里有一条 matcher 我照抄自
// 源码里那段**已过期的 doc 注释**（它引的旧快照少了 "billing" 一词），于是
// 真实键 `the prose above this table names a non-standard billing dimension`
// 永远不命中，28 条候选落进 `unclassified:` 桶。
//
// **而如果没人盯 `unclassified`，这跟没修是一样的** —— 它只是把「静默多出一个
// 类名」换成「静默多出一个 unclassified 类」。所以「断言未归族为 0」才是本判据
// 的承重处。
//
// # 判据的四条
//
//  1. 真实夹具 → 真实提取器 → 产出的**类目集合**与预期逐条相同。
//  2. 未归族桶数 = 0（承重处）。
//  3. **探测器自证**：造一条谁都不匹配的 warning，断言它**确实**落进
//     unclassified 桶。不加这条，「未归族 = 0」可能只是因为探测器压根没跑。
//  4. 不可相加的提醒仍在（classes 之和 > 不可用总数）。
//
// 依赖仓内跟踪的夹具（internal/vendorprice/testdata），所以夹具路径错必须
// Fatalf 而不是 Skipf —— 路径错时 SKIP 会让人以为「验过了」。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

// expectedRejectionFamilies 是 4 份实抓夹具跑出来的类目集合。
//
// 改动这里的正当理由只有两种：真的出现了新的可行动类别（加一行到 reasonFamilies），
// 或者一类真的消失了。**不要**为了让某个 matcher「看起来能用」而改这里 —— 那正是
// 下面第 2 条要抓的。
//
// 数字是**本判据这条路径**上的实测值：5 份夹具 → 134 条候选、9 类、类目之和 260。
//
// 2026-10-06：132 → 134、7 类 → 9 类，全部来自 `raw/deepseek.md` 重抓后新增的
// 规格表式（run-on）路径 —— 那页没有 markdown 表格，重抓后是 V4.1-Flash +
// OFF-PEAK/PEAK 峰谷定价，于是多了 2 条「峰谷分档」与 2 条「列归属不可证明」。
// ★ 类目之和（260）**大于**不可用候选数，这是设计如此：一条候选可能带多条警告，
//
//	归类时按前 3 条分别计入不同的族。所以不要把这两个数当成守恒等式。
//
// 2026-10-05：第 5 份夹具是 live-google-gemini-permodel-block.md（逐模型价块的页形，
// 见 internal/vendorprice/testdata/ 里那份文件的说明）。它带来 10 条候选，落在
// 上面三类里 —— 「逐模型价块」是**新增**的一类，其余两类的计数各 +9/+9。
//
// 2026-10-05 第二次：同一份夹具又加了 2 行**带生效日期的调价**
// （`$0.75 through December 31, 2026. $1.50 starting January 1, 2027.`），
// 带来「基准价是定价决策」这个**新增**的族（2 条），另三类各 +2。
// 同一份夹具里还留了一行**合法**的两笔金额（缓存读 + 存储），它**不**落在
// 新族里 —— 这正是「新诊断不是『一格有两个金额就报』」的证据。
//
// 第三次：加了 1 行**有日期措辞但没有金额**的 `Free tier` 行（4 个类各 +1，
// 新族仍 2）。它是为了让「一格里必须有金额」这个前置**可被观测** ——
// 在加它之前，teeth 实测去掉该前置**族计数一条都不变**，也就是说那个前置
// 当时是没人守的。
//
// ⚠ 它与真跑一次工具得到的数**不一样**（工具那次是 112 条不可用、5 类、tier 类 23）。
// 差在工具比提取器多一道闸：厂商认不出来的快照整页跳过（"refusing to invent a
// source_url"），而本判据直接把 4 个文件都喂给 Extract。
// 两者都对，**别把它们混着比** —— 看到数不一样先确认走的是哪条路。
var expectedRejectionFamilies = map[string]int{
	"cell states a price that changes on a date (baseline is a pricing decision)":        2,
	"column structure is not one price per (model, in/out)":                              97,
	"per-model price block: the model name is not in the table (fetch lost the anchor)":  20,
	"price is dimension-conditional (context length / modality / tier within one table)": 77,
	"priced in a unit that is not per-1M-tokens":                                         25,
	"row is context-tiered or per-token mix":                                             9,
	"table is a non-standard tier (Batch / Fast / Flex / Ultrafast)":                     26,
	// ↓ 2026-10-06：规格表式（run-on）路径上线后新增的两类，各 2 条（deepseek 一页）。
	//
	// 这两类是**新出现的可行动类别**，不是重新分类：`raw/deepseek.md` 重抓之后
	// 整页变成 `V4.1-Flash` + OFF-PEAK/PEAK 峰谷定价，而这一页**没有 markdown
	// 表格**，于是「钱在页面上但没进提案」第一次成了一种能被命名、能给出下一步
	// 的状态。`groupWarnings` 把它们暴露成 visible hole，正是这条判据设计的
	// 目的（2026-10-05 那次就是因为抄了注释里的措辞而漏匹配）。
	"run-on price sheet: price is time-of-day tiered (peak / off-peak)":     2,
	"run-on price sheet: this line's price is not attributable to a column": 2,
}

func TestRejectionReasonFamiliesCoverRealFixtures(t *testing.T) {
	// ★ 这个包在 cmd/tools/propose-baseline-prices/，**深度 3**，根是 ../../..
	dir := "../../../internal/vendorprice/testdata"
	entries, err := os.ReadDir(dir)
	if err != nil {
		// ★ Fatalf 而非 Skipf：夹具是仓里跟踪的文件，路径写错是我的错，
		// 而 SKIP 会显示成「验过了」。
		t.Fatalf("read the real fixtures at %s: %v", dir, err)
	}
	var cands []vendorprice.Candidate
	var files int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		files++
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		cands = append(cands, vendorprice.Extract("unknown-vendor", "https://example.invalid/"+e.Name(), b)...)
	}
	if files == 0 {
		t.Fatalf("no .md fixtures under %s — the class set below would be vacuous", dir)
	}
	t.Logf("ran the real extractor over %d fixture file(s), got %d candidate(s)", files, len(cands))

	classes := groupWarnings(cands, 3)
	if len(classes) == 0 {
		t.Fatalf("the real fixtures produced 0 classes across %d candidate(s) — either the "+
			"extractor stopped emitting warnings, or this test is not reaching it", len(cands))
	}

	// (2) 承重：未归族必须是 0。
	for _, cl := range classes {
		if strings.HasPrefix(cl.Reason, unclassifiedPrefix) {
			t.Errorf("%d candidate(s) fell into %q%s. That bucket exists so a new warning "+
				"phrasing shows up as a visible hole; a matcher in reasonFamilies is either "+
				"mis-transcribed or stale. Check the real head against the Match strings, and "+
				"never copy a phrasing out of a code comment — those go stale",
				cl.Count, cl.Reason, cl.Examples)
		}
	}

	// (1) 类目集合逐条相同。
	got := map[string]int{}
	for _, cl := range classes {
		got[cl.Reason] = cl.Count
	}
	for _, cl := range classes {
		want, ok := expectedRejectionFamilies[cl.Reason]
		if !ok {
			t.Errorf("unexpected class %q (%d candidate(s)) — if it is a genuinely new, "+
				"actionable cause, add a family to reasonFamilies AND an entry to "+
				"expectedRejectionFamilies", cl.Reason, cl.Count)
			continue
		}
		if cl.Count != want {
			t.Errorf("class %q has %d candidate(s), want %d", cl.Reason, cl.Count, want)
		}
	}
	for name, want := range expectedRejectionFamilies {
		if got[name] == 0 {
			t.Errorf("class %q (%d candidate(s) expected) no longer appears — the extractor or "+
				"the fixtures changed; delete the entry if the class is genuinely gone", name, want)
		}
	}
	// 每个归了族的类都必须带 Why —— 没有 Why 就退化回「一个类名」，
	// 而这一列存在的意义是「决定先修哪一类」。
	for _, cl := range classes {
		if cl.Why == "" {
			t.Errorf("class %q carries no Why — the operator is left with a label and no "+
				"next action", cl.Reason)
		}
	}

	// (4) 不可相加这条性质必须仍然成立，否则那句提醒就变成了一句假话。
	sum, unusable := 0, 0
	for _, c := range cands {
		for _, w := range c.Warnings {
			_ = w
		}
		if len(c.Warnings) > 0 {
			unusable++
		}
	}
	for _, cl := range classes {
		sum += cl.Count
	}
	if sum <= unusable {
		t.Errorf("class counts sum to %d across %d candidate(s) with warnings. The overlap note "+
			"%q claims one candidate can hit several classes; that is no longer true, so the "+
			"note is now a lie in the output", sum, unusable, rejectionReasonOverlapNote)
	}

	// (3) 探测器自证：造一条谁都不匹配的 warning，断言它**确实**被标成未归族。
	// 少了这条，「未归族 = 0」可能只是因为探测器没被调用过。
	probe := groupWarnings([]vendorprice.Candidate{
		{Model: "zzz-probe", Warnings: []string{
			"a warning phrasing that deliberately matches no family (existence check)",
		}},
	}, 1)
	if len(probe) != 1 || !strings.HasPrefix(probe[0].Reason, unclassifiedPrefix) {
		t.Errorf("the unclassified detector did not fire on a warning that matches no family; "+
			"got %+v. If this is because the detector never runs, then the \"zero unclassified\" "+
			"assertion above is vacuous", probe)
	}

	// 类目数本身也钉一下：它是最容易被人从外部察觉的变化。
	if len(classes) != len(expectedRejectionFamilies) {
		keys := make([]string, 0, len(classes))
		for _, cl := range classes {
			keys = append(keys, cl.Reason)
		}
		sort.Strings(keys)
		t.Errorf("the real fixtures now produce %d class(es), want %d: %s",
			len(classes), len(expectedRejectionFamilies), strings.Join(keys, " | "))
	}
}
