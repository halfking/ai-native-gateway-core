package main

// rejection_reasons 归类的判据。
//
// 为什么需要它（2026-10-04 实测）：一页实抓快照产出 112 条不可用、30 种散着的
// 理由，没有聚合就只能逐条读。而逐条读的代价正是这套「抓取 → 提案 → 人确认」
// 流程要替人省掉的活 —— 它把人确认那一步的入口做没了。
//
// 而且它不只是「少一个汇总」：逐条读时人很容易**加总**，而加总会得到一个
// 不存在的数（112 条里前六类就合计 149），因为一条候选会同时命中好几类。
// 归类里必须把这件事写明，否则下一个读的人会算错。

import (
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

func cand(model string, warnings ...string) vendorprice.Candidate {
	return vendorprice.Candidate{Model: model, Warnings: warnings}
}

func TestWarningReasonKeyNormalisesTheVariableParts(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// 截到解释分界
		{
			"row contains a struck-through price (~~...~~) — the superseded original must not be used",
			"row contains a struck-through price",
		},
		// 剥括注（值是变量）
		{
			"the table header names a non-standard billing dimension (Short context) — these prices are conditional",
			"the table header names a non-standard billing dimension",
		},
		{
			"the table header names a non-standard billing dimension (Long context) — these prices are conditional",
			"the table header names a non-standard billing dimension",
		},
		// 数字归一 —— 实测踩到：这三类原本各被拆成独立的一类，而拆开之后
		// 每类的计数都变小，看着仍「正常」，没人发现归类已经碎了。
		{
			"column count 2 does not match the header's 4",
			"column count n does not match the header's n",
		},
		{
			"column count 3 does not match the header's 5",
			"column count n does not match the header's n",
		},
		{
			"row has 4 extra priced column(s) of the same role that were not taken (only the first is kept)",
			"row has n extra priced column(s) of the same role that were not taken",
		},
		// 去开头的 and（续写型 warning）
		{
			"and the header names a billing dimension (Modality) — these prices are conditional",
			"the header names a billing dimension",
		},
	} {
		if got := warningReasonKey(tc.in); got != tc.want {
			t.Errorf("warningReasonKey(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
		}
	}

	// 反向：两个只差数字的 warning 必须落到**同一个**键，否则归类会碎。
	a := warningReasonKey("column count 2 does not match the header's 4")
	b := warningReasonKey("column count 9 does not match the header's 7")
	if a != b {
		t.Errorf("digit-only differences must not split one class: %q vs %q", a, b)
	}
}

func TestGroupWarningsCountsReasonsNotRowsAndIsStable(t *testing.T) {
	cands := []vendorprice.Candidate{
		// 两条同理由（括注不同 ⇒ 同一类）
		cand("m1", "the table header names a non-standard billing dimension (Short context) — conditional"),
		cand("m2", "the table header names a non-standard billing dimension (Long context) — conditional"),
		// 同一条候选带**同一理由两次**（不同列）⇒ 只该计一次。
		// 文案照抄提取器的真实措辞（含 "of the same role" 与结尾那半句）——
		// 这里偷懒简写成 "…that were not taken" 会让期望键与实际键差一截，
		// 而报出来的现象是「count = 0」，看不出是自己文案写歪了。
		cand("m3", "row has 4 extra priced column(s) of the same role that were not taken (only the first is kept); check the page for the others",
			"row has 3 extra priced column(s) of the same role that were not taken (only the first is kept); check the page for the others"),
		// 一条同时命中两类（就是「计数会重叠」的那个来源）
		cand("m4", "header does not map to both an input and an output column (header line 76)",
			"row is context-tiered or per-token — a tiered price is not the flat list price"),
	}

	got := groupWarnings(cands, 3)
	// ★ 这里从 4 类变成 3 类，是 2026-10-05 加归族（reasonFamilies）后的**预期**结果：
	// m3 的「row has n extra priced column(s) of the same role…」与 m4 的
	// 「header does not map to both an input and an output column」成因相同
	// （列结构与「一行一入一出」对不上），归族后并成一类。
	// 改族表时这里会红 —— 那是设计上的耦合，别顺手把期望数改成「当前输出」。
	if len(got) != 3 {
		t.Fatalf("got %d classes, want 3 (two column-structure reasons must merge into one): %+v",
			len(got), got)
	}

	byReason := map[string]rejectionReason{}
	for _, r := range got {
		byReason[r.Reason] = r
	}
	if n := byReason["price is dimension-conditional (context length / modality / tier within one table)"].Count; n != 2 {
		t.Errorf("dimension class count = %d, want 2 — the two rows differ only in the parenthesised value", n)
	}
	// m3 把同一理由带了两遍（只差数字）⇒ 归一化 + 去重后仍只计一次。
	// m4 的第一条理由同族 ⇒ 并进来，所以这一类是 2。
	if n := byReason["column structure is not one price per (model, in/out)"].Count; n != 2 {
		t.Errorf("the single row carrying that reason twice counted %d in its class, want 2 "+
			"(m3 once, m4 once via the sibling reason) — otherwise one row's column count "+
			"inflates the class it belongs to; classes actually produced: %s",
			n, describeReasons(got))
	}
	// 一条候选命中两类：m4 的两条理由分属不同的族。
	if n := byReason["row is context-tiered or per-token mix"].Count; n != 1 {
		t.Errorf("multi-reason row did not land in the second class: count = %d, want 1", n)
	}

	// 示例名是给人「认出是哪几条」的，所以要真的带上模型名。
	if ex := byReason["price is dimension-conditional (context length / modality / tier within one table)"].Examples; len(ex) != 2 {
		t.Errorf("examples = %v, want 2 model names", ex)
	}

	// 排序：数量降序，同数按原因名稳定排序。顺序每次都变的话，
	// 两版提案没法 diff —— 而提案本来就要给人对比着看。
	for i := 1; i < len(got); i++ {
		if got[i-1].Count < got[i].Count {
			t.Fatalf("not sorted by count desc: %d then %d", got[i-1].Count, got[i].Count)
		}
		if got[i-1].Count == got[i].Count && got[i-1].Reason > got[i].Reason {
			t.Fatalf("ties not sorted by reason name: %q then %q", got[i-1].Reason, got[i].Reason)
		}
	}
	// 顺序稳定性：同样的输入再跑一次必须完全一样。
	again := groupWarnings(cands, 3)
	for i := range got {
		if got[i].Reason != again[i].Reason || got[i].Count != again[i].Count ||
			strings.Join(got[i].Examples, "\x00") != strings.Join(again[i].Examples, "\x00") {
			t.Fatalf("groupWarnings is not deterministic at %d: %+v vs %+v", i, got[i], again[i])
		}
	}
}

// TestGroupWarningsCountsOverlapAndSaysSo 钉住「计数会重叠」这个事实本身。
//
// 这条不是凑数：112 条实测里前六类合计 149，而人看到一排数字第一反应就是加总。
// 归类若不让人知道这一点，唯一的汇总入口就成了一个必然被算错的入口。
func TestGroupWarningsCountsOverlapAndSaysSo(t *testing.T) {
	// 一条候选同时命中 3 类 ⇒ 三类各计 1，合计 3 > 1 条。
	got := groupWarnings([]vendorprice.Candidate{
		cand("m1", "alpha reason — x", "beta reason — y", "gamma reason — z"),
	}, 3)
	if len(got) != 3 {
		t.Fatalf("got %d classes, want 3", len(got))
	}
	sum := 0
	for _, r := range got {
		sum += r.Count
	}
	if sum <= 1 {
		t.Fatalf("class counts sum to %d; the overlap that makes summing wrong is exactly what "+
			"must be visible to the reader", sum)
	}
	// 类型注释里必须写着「不可相加」——那是聚合唯一会被误用的地方。
	if !strings.Contains(rejectionReasonOverlapNote, "不可相加") &&
		!strings.Contains(rejectionReasonOverlapNote, "overlap") {
		t.Errorf("the overlap note does not warn against summing: %q", rejectionReasonOverlapNote)
	}
}

// describeReasons 把归类结果压成一行放进失败信息。
//
// 为什么值得单独一个 helper：这条判据第一版失败时报的是「counted 0 times」，
// 而**没有**把实际产出了哪几类打出来，于是只能靠再写一个临时测试去打印、
// 打印完发现状态是对的、于是更困惑。一条只会说「0」的断言是半个断言。
func describeReasons(rs []rejectionReason) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, strconv.Itoa(r.Count)+"×"+r.Reason)
	}
	return "[" + strings.Join(parts, " | ") + "]"
}
