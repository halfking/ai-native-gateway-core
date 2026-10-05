package main

// 「一个 canonical 不得出现两个不同的价」的不变量判据。
//
// # 为什么这条不变量要单独判，而不是靠 buildDraft 的 dup 兜底
//
// 现状里唯一的兜底是 `if _, dup := d.Models[canonical]; dup { refuse }`：
// 它**位置太靠后**（在互证之后），而且「谁赢」由**文档顺序**决定 —— 厂商把
// Fast mode 那张表放在后面，标准价就赢；哪天段落顺序变了，入库的基准价跟着变，
// 而页面上什么都没改。**一条会随排版漂移的基准价比没有基准价坏得多**：
// 它看起来是对的。
//
// # 判据清单（每条对应一个具体的失效形态，不是「覆盖率」）
//
//	1  撞名的两行**整组**扣下，且两行都出现在冲突条目里（带行号与出处）
//	2  同价重复**不是**冲突（数字唯一，交给 buildDraft 的 dup 兜底）
//	3  nil ≠ 0：「没报输入价」与「输入价是 0」不是同一个价
//	4  币种不同 ⇒ 冲突，且理由点名 currency
//	5  单位不同 ⇒ 冲突，且理由点名 unit
//	6  分档（tier）不同**不是**冲突（同价时基准列没有歧义）—— 这条钉住刻意的设计
//	7  解析不出 canonical 的行原样放过，不造「空串互相冲突」的假冲突
//	8  不同 canonical 的同价行都留下（防过度拦截）
//	9  ★ 对账恒等式：进门 == 留下 + 扣下（扣下 ≠ 丢钱）
//	10 跨厂商对同一 canonical 声明不同价也算冲突（SSOT 的键不含厂商）
//	11 报告文本必须点名 canonical、两个数、行号、主机名（人靠它回页面核对）
//	12 冲突顺序 = 首次出现顺序（报告可复现）
//	13 三个不同价 ⇒ 3 个都在，且 Distinct=3

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

func px(f float64) *float64 { return &f }

// cand 造一条**已解析出 canonical 名**的候选 —— 判据要的就是解析之后那一段，
// 所以这里直接按 `resolvedCanonical` 的契约（warnings 里的
// `resolved to canonical "X"` 子串）摆形状，而不是从头跑解析。
// canon 为空串时不加那条 warning，对应「没解析出来」。
func rcand(canon, vendor, display string, in, out *float64, line int) vendorprice.Candidate {
	c := vendorprice.Candidate{
		Vendor: vendor, Model: display, Input: in, Output: out,
		Currency: "USD", Unit: vendorprice.UnitPer1M,
		Confidence: vendorprice.ConfidenceTableRow,
		SourceURL:  "https://docs.example.invalid/pricing",
		Row:        "| " + display + " |", LineNo: line,
	}
	if canon != "" {
		c.Warnings = []string{fmt.Sprintf("resolved to canonical %q (score 100.00)", canon)}
	}
	return c
}

func collisionFor(t *testing.T, cs []canonicalCollision, canon string) canonicalCollision {
	t.Helper()
	for _, c := range cs {
		if c.Canonical == canon {
			return c
		}
	}
	t.Fatalf("no conflict reported for canonical %q (got %d: %v)", canon, len(cs), canonicalListOf(cs))
	return canonicalCollision{}
}

func canonicalListOf(cs []canonicalCollision) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Canonical)
	}
	return out
}

// 1 —— 撞名的两行整组扣下，两行都可见。
func TestConflictingPricesForOneCanonicalAreAllWithheld(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(5), px(25), 41),
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(10), px(50), 52),
	}
	kept, conflicts := withholdConflictingPrices(ready)

	if len(kept) != 0 {
		t.Errorf("a canonical with two different prices must have BOTH rows withheld, %d row(s) "+
			"survived — whoever survives becomes the baseline price by accident of table order",
			len(kept))
	}
	if len(conflicts) != 1 {
		t.Fatalf("want exactly 1 conflict, got %d (%v)", len(conflicts), canonicalListOf(conflicts))
	}
	c := collisionFor(t, conflicts, "claude-opus-4-8")
	if c.Rows != 2 || c.Distinct != 2 || len(c.Prices) != 2 {
		t.Errorf("conflict must account for both rows as 2 distinct prices, got rows=%d distinct=%d prices=%d",
			c.Rows, c.Distinct, len(c.Prices))
	}
	// 出处必须跟着走：扣下不是丢钱，是「等人裁决」。
	for i, want := range []int{41, 52} {
		if i < len(c.Prices) && c.Prices[i].LineNo != want {
			t.Errorf("prices[%d].line_no = %d, want %d — the withheld row lost its provenance, "+
				"so a human cannot go back to the page to pick one", i, c.Prices[i].LineNo, want)
		}
	}
	if !strings.Contains(c.Reason, "claude-opus-4-8") {
		t.Errorf("reason must name the canonical it is blocking, got: %s", c.Reason)
	}
}

// 2 —— 同价重复不是冲突。
func TestIdenticalDuplicateIsNotAConflict(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("claude-sonnet-4-5", "anthropic", "Claude Sonnet 4.5", px(3), px(15), 10),
		rcand("claude-sonnet-4-5", "anthropic", "Claude Sonnet 4.5", px(3), px(15), 11),
	}
	kept, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 0 {
		t.Errorf("two rows with the SAME price vector are not a price conflict (the number is "+
			"unambiguous); got %v", canonicalListOf(conflicts))
	}
	if len(kept) != 2 {
		t.Errorf("identical duplicates must be left in place for buildDraft's dup guard, got %d kept",
			len(kept))
	}
}

// 3 —— nil ≠ 0。
func TestNilAndZeroAreNotTheSamePriceSignature(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("x", "v", "X", nil, px(25), 1),   // 没报输入价
		rcand("x", "v", "X", px(0), px(25), 2), // 输入价是 0
	}
	_, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 1 {
		t.Fatalf("\"no input price\" and \"input price is 0\" are different claims; got %d conflicts",
			len(conflicts))
	}
	if !strings.Contains(conflicts[0].Reason, "input") {
		t.Errorf("reason must name the field that differs, got: %s", conflicts[0].Reason)
	}
}

// 3b —— **只有输出价不同**也算冲突。
//
// 这条是变异 C2（「指纹只比 input」）的承重处。删掉它之后，C2 是**绿的**：
// anthropic 那个真实形状（5/25 vs 10/50）输入价也不同，所以「只比 input」
// 照样抓得住 —— 于是变异看起来通过了，而实际上「输出价单独不同」这一类
// 已经漏网。SSOT 的输出价列是独立的一列，只看输入价等于放它过。
func TestOutputOnlyDifferenceIsAConflict(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("x", "v", "X", px(5), px(25), 1),
		rcand("x", "v", "X", px(5), px(50), 2), // 只有输出价不同
	}
	_, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 1 {
		t.Fatalf("the SSOT has a separate output price column; a differing output price is a "+
			"conflict even when the input price matches. Got %d conflicts", len(conflicts))
	}
	if !strings.Contains(conflicts[0].Reason, "output") {
		t.Errorf("reason must name output, got: %s", conflicts[0].Reason)
	}
	if strings.Contains(conflicts[0].Reason, "input") {
		t.Errorf("reason must not claim the input price differs when it does not: %s",
			conflicts[0].Reason)
	}
}

// 4 —— 币种不同。
func TestCurrencyDifferenceIsAConflict(t *testing.T) {
	a := rcand("x", "v", "X", px(5), px(25), 1)
	b := rcand("x", "v", "X", px(5), px(25), 2)
	b.Currency = "CNY"
	_, conflicts := withholdConflictingPrices([]vendorprice.Candidate{a, b})
	if len(conflicts) != 1 {
		t.Fatalf("same numbers in different currencies is a conflict (the SSOT column is money, "+
			"not a bare number); got %d", len(conflicts))
	}
	if !strings.Contains(conflicts[0].Reason, "currency") {
		t.Errorf("reason must name currency, got: %s", conflicts[0].Reason)
	}
}

// 5 —— 单位不同。
func TestUnitDifferenceIsAConflict(t *testing.T) {
	a := rcand("x", "v", "X", px(0.002), px(0.002), 1)
	b := rcand("x", "v", "X", px(0.002), px(0.002), 2)
	b.Unit = "per_image"
	_, conflicts := withholdConflictingPrices([]vendorprice.Candidate{a, b})
	if len(conflicts) != 1 {
		t.Fatalf("the same number under two different units is a conflict; got %d", len(conflicts))
	}
	if !strings.Contains(conflicts[0].Reason, "unit") {
		t.Errorf("reason must name unit, got: %s", conflicts[0].Reason)
	}
}

// 6 —— 分档不同**不是**冲突（刻意的设计，钉住它）。
func TestTierDifferenceIsNotAConflict(t *testing.T) {
	a := rcand("x", "v", "X", px(5), px(25), 1)
	a.Tier, a.ProductTier = "Short context", "standard"
	b := rcand("x", "v", "X", px(5), px(25), 2)
	b.Tier, b.ProductTier = "Long context", "batch"
	_, conflicts := withholdConflictingPrices([]vendorprice.Candidate{a, b})
	if len(conflicts) != 0 {
		t.Errorf("same price under two tiers is unambiguous — the baseline column has exactly one " +
			"correct value, so reporting it as a conflict would train the reader to ignore the field")
	}
}

// 7 —— 没解析出 canonical 的行原样放过。
func TestCandidatesWithoutCanonicalNameAreLeftAlone(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("", "v", "Unknown A", px(1), px(2), 1),
		rcand("", "v", "Unknown B", px(3), px(4), 2),
	}
	kept, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 0 {
		t.Errorf("rows with no canonical name must not be grouped under the empty key and "+
			"reported as conflicting with each other; got %v", canonicalListOf(conflicts))
	}
	if len(kept) != 2 {
		t.Errorf("unresolved rows belong to the Unresolved bucket (the problem is the name, not "+
			"the price) and must pass through untouched; got %d kept", len(kept))
	}
}

// 8 —— 不误伤：不同 canonical 的同价行都留下。
func TestDifferentCanonicalsWithTheSamePriceBothSurvive(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(5), px(25), 1),
		rcand("claude-sonnet-4-5", "anthropic", "Claude Sonnet 4.5", px(5), px(25), 2),
	}
	kept, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 0 {
		t.Errorf("two different models priced the same is the normal case, not a conflict; got %v",
			canonicalListOf(conflicts))
	}
	if len(kept) != 2 {
		t.Errorf("both rows must survive, got %d", len(kept))
	}
}

// 9 —— ★ 对账恒等式。扣下不等于丢钱。
func TestConflictAccountingLosesNoRow(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("a", "v", "A", px(1), px(1), 1),
		rcand("b", "v", "B", px(2), px(2), 2),
		rcand("b", "v", "B", px(9), px(9), 3), // 撞 b
		rcand("c", "v", "C", px(3), px(3), 4),
		rcand("c", "v", "C", px(3), px(3), 5), // 同价重复
		rcand("d", "v", "D", px(4), px(4), 6),
		rcand("d", "v", "D", px(4), px(4), 7), // 同价重复
		rcand("d", "v", "D", px(8), px(8), 8), // 撞 d
		rcand("", "v", "E", px(5), px(5), 9),  // 未解析
	}
	kept, conflicts := withholdConflictingPrices(ready)

	withheld := 0
	for _, c := range conflicts {
		withheld += c.Rows
	}
	if got, want := len(kept)+withheld, len(ready); got != want {
		t.Errorf("row accounting broken: %d kept + %d withheld = %d, but %d rows went in. "+
			"A row that is neither kept nor listed in ambiguous_canonical_prices has been lost — "+
			"and a lost price is invisible, which is worse than a wrong one",
			len(kept), withheld, got, want)
	}
	if len(kept) != 4 {
		t.Errorf("want a, c, c-dup, unresolved = 4 kept, got %d", len(kept))
	}
	if len(conflicts) != 2 {
		t.Errorf("want 2 conflicts (b and d), got %d: %v", len(conflicts), canonicalListOf(conflicts))
	}
}

// 10 —— 跨厂商声明冲突也算（SSOT 的键是 canonical_name，不含厂商）。
func TestCrossVendorDisagreementOnOneCanonicalIsAConflict(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(5), px(25), 1),
		rcand("claude-opus-4-8", "openrouter", "Claude Opus 4.8", px(6), px(30), 2),
	}
	kept, conflicts := withholdConflictingPrices(ready)
	if len(kept) != 0 || len(conflicts) != 1 {
		t.Errorf("two vendors quoting different prices for one canonical is exactly the case the "+
			"SSOT cannot represent; got %d kept, %d conflicts", len(kept), len(conflicts))
	}
	c := conflicts[0]
	vendors := map[string]bool{}
	for _, p := range c.Prices {
		vendors[p.Vendor] = true
	}
	if !vendors["anthropic"] || !vendors["openrouter"] || len(vendors) != 2 {
		t.Errorf("the conflict entry must carry both vendors' rows with provenance, got %v", vendors)
	}
}

// 11 —— 报告文本要能让人回页面核对。
func TestConflictReportNamesTheRowsAndTheHost(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(5), px(25), 41),
		rcand("claude-opus-4-8", "anthropic", "Claude Opus 4.8", px(10), px(50), 52),
	}
	_, conflicts := withholdConflictingPrices(ready)
	got := conflicts[0].Report()

	for _, want := range []string{"claude-opus-4-8", "5", "25", "10", "50", "41", "52", "anthropic"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q; a human cannot resolve the conflict without it:\n%s", want, got)
		}
	}
	// 主机名：source_url 的 scheme/www 之后那一段，是人最常用的检索键。
	if !strings.Contains(got, "docs.example.invalid") {
		t.Errorf("report must name the snapshot host so the reader knows which page to open:\n%s", got)
	}
}

// 12 —— 顺序可复现。
func TestConflictOrderFollowsFirstAppearance(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("zzz", "v", "Z", px(1), px(1), 1),
		rcand("zzz", "v", "Z", px(2), px(2), 2),
		rcand("aaa", "v", "A", px(1), px(1), 3),
		rcand("aaa", "v", "A", px(2), px(2), 4),
	}
	_, conflicts := withholdConflictingPrices(ready)
	if len(conflicts) != 2 || conflicts[0].Canonical != "zzz" || conflicts[1].Canonical != "aaa" {
		t.Errorf("conflicts must be reported in first-appearance order (z before a), got %v",
			canonicalListOf(conflicts))
	}
}

// 13 —— 三个不同的价，三个都在。
func TestThreeDistinctPricesForOneCanonicalAreAllListed(t *testing.T) {
	ready := []vendorprice.Candidate{
		rcand("x", "v", "X", px(5), px(25), 1),
		rcand("x", "v", "X", px(10), px(50), 2),
		rcand("x", "v", "X", px(5), px(25), 3), // 与第一条同价
		rcand("x", "v", "X", px(20), px(100), 4),
	}
	kept, conflicts := withholdConflictingPrices(ready)
	if len(kept) != 0 {
		t.Errorf("all 4 rows belong to the blocked canonical, %d survived", len(kept))
	}
	c := collisionFor(t, conflicts, "x")
	if c.Rows != 4 || c.Distinct != 3 || len(c.Prices) != 3 {
		t.Errorf("want rows=4 distinct=3 prices=3, got rows=%d distinct=%d prices=%d",
			c.Rows, c.Distinct, len(c.Prices))
	}
}
