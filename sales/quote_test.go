package sales

// quote_test.go —— 纯单元测试（2026-11-05 审计 A9：sales 包此前零纯单元
// 测试，无 DSN 时全部 skip ⇒ 纯算术缺陷拦不住）。本文件钉三件事：
//  1. A1：cents_per_credit 的 A-14 单位语义（分/积分 ⇒ ×cents 得分、÷100 折元）
//     —— 本文件在时，把 precomputeLines 的 ÷100 删掉会当场红；
//  2. 决议 C 的折扣正交：折后价只出现在预计算，基础价轴（RateIn/RateOut）
//     原样透传，毛利按折后算；
//  3. 币种未知拒绝不默认 USD（红线）：CostUSD=0 + confidence=unknown_currency，
//     且版本置信档取行级最差档。
import (
	"math"
	"testing"
)

func ptrOf(f float64) *float64 { return &f }

// TestPrecomputeLinesA14Unit —— A1 的回归钉：14000 credits × 20 分/积分 =
// 280000 分 = 2800 元 ⇒ ÷7.2 ≈ $388.89。若有人把 ÷100 删掉（当元用），
// 读数会虚大 100×（$38888.9）⇒ 精确断言拦截。
func TestPrecomputeLinesA14Unit(t *testing.T) {
	rateIn := 100.0
	rateOut := 200.0
	items := []QuoteItemInput{{
		CanonicalName: "model-a",
		EstTokensIn:   100_000_000,
		EstTokensOut:  20_000_000,
		CostInPer1M:   ptrOf(1.0),
		CostOutPer1M:  ptrOf(2.0),
		CostCurrency:  "USD",
		RateIn:        &rateIn,
		RateOut:       &rateOut,
		DiscountPct:   0,
		Confidence:    "trusted",
	}}
	lines, conf := precomputeLines(items, 7.2, 20) // centsPerCredit=20 分/积分
	if conf != "trusted" {
		t.Fatalf("confidence = %q, want trusted", conf)
	}
	wantUSD := 14000 * 20 / 100 / 7.2 // = 388.888…
	if math.Abs(lines[0].RevenueUSD-wantUSD) > 1e-9 {
		t.Fatalf("RevenueUSD = %.6f, want %.6f (A-14: cents_per_credit=分/积分 ⇒ ÷100 折元)", lines[0].RevenueUSD, wantUSD)
	}
	// 毛利闭环：成本 $140 ⇒ margin = 388.89-140 = 248.89 ⇒ pct ≈ 64%。
	wantMargin := lines[0].RevenueUSD - 140.0
	if math.Abs(lines[0].MarginUSD-wantMargin) > 1e-9 {
		t.Fatalf("MarginUSD = %.6f, want %.6f", lines[0].MarginUSD, wantMargin)
	}
	if lines[0].MarginPct == nil || math.Abs(*lines[0].MarginPct-wantMargin/lines[0].RevenueUSD*100) > 1e-6 {
		t.Fatalf("MarginPct = %v, want ≈ %.2f", lines[0].MarginPct, wantMargin/lines[0].RevenueUSD*100)
	}
}

// TestPrecomputeLinesDiscountOrthogonal —— 决议 C：折扣只进预计算，基础价轴不动。
func TestPrecomputeLinesDiscountOrthogonal(t *testing.T) {
	rateIn := 100.0
	rateOut := 200.0
	items := []QuoteItemInput{{
		EstTokensIn:  1_000_000,
		EstTokensOut: 0,
		CostInPer1M:  ptrOf(1.0),
		CostCurrency: "USD",
		RateIn:       &rateIn,
		RateOut:      &rateOut,
		DiscountPct:  25,
		Confidence:   "trusted",
	}}
	lines, _ := precomputeLines(items, 7.2, 100)
	// 折后 in = ceil(100×0.75) = 75 credits ⇒ 收入 75×100/100 = 75 分… cents=100
	// ⇒ 75×100/100 = 75 元 ⇒ ÷7.2 = 10.4167 USD。
	want := 75.0 * 100 / 100 / 7.2
	if math.Abs(lines[0].RevenueUSD-want) > 1e-9 {
		t.Fatalf("discounted RevenueUSD = %.6f, want %.6f", lines[0].RevenueUSD, want)
	}
	// 基础价轴透传不动（对账面要求：成交不改价）。
	if lines[0].RateIn == nil || *lines[0].RateIn != 100 || lines[0].RateOut == nil || *lines[0].RateOut != 200 {
		t.Fatalf("base rate axis must stay at 100/200, got %+v", lines[0])
	}
	if lines[0].DiscountPct != 25 {
		t.Fatalf("DiscountPct = %v, want 25 (两轴分列)", lines[0].DiscountPct)
	}
}

// TestPrecomputeLinesUnknownCurrency —— 币种未知拒绝不默认 USD：成本 0 + 降档，
// 且版本置信档取行级最差（trusted 行 + unknown 行 ⇒ 版本 unknown）。
func TestPrecomputeLinesUnknownCurrency(t *testing.T) {
	rateIn := 100.0
	items := []QuoteItemInput{
		{EstTokensIn: 1_000_000, CostCurrency: "USD", CostInPer1M: ptrOf(1.0), RateIn: &rateIn, Confidence: "trusted"},
		{EstTokensIn: 1_000_000, CostCurrency: "???", CostInPer1M: ptrOf(1.0), RateIn: &rateIn, Confidence: "trusted"},
	}
	lines, conf := precomputeLines(items, 7.2, 100)
	if lines[1].CostUSD != 0 || lines[1].Confidence != "unknown_currency" {
		t.Fatalf("unknown-currency line = cost %.2f conf %q, want 0/unknown_currency", lines[1].CostUSD, lines[1].Confidence)
	}
	if conf != "unknown_currency" {
		t.Fatalf("version confidence = %q, want unknown_currency (门不是分：一行不可信整单降档)", conf)
	}
}

// TestRedLineVerdictDimensions —— DealHub 三维的方向与边界（A 组审计点：
// 毛利 < 触发 / 折扣 > 触发 / 金额 >= 触发；0=关闭该维）。
func TestRedLineVerdictDimensions(t *testing.T) {
	cases := []struct {
		name                   string
		marginPct, disc, total float64
		wantTriggers           int
		wantDims               map[string]bool
	}{
		{"in-policy", 64, 0, 388, 0, nil},
		{"margin exactly at floor not triggered", 30, 0, 100, 0, nil},
		{"margin below floor", 29.9, 0, 100, 1, map[string]bool{"margin_floor": true}},
		{"discount above depth", 50, 10.5, 100, 1, map[string]bool{"discount_depth": true}},
		{"discount at depth not triggered", 50, 10, 100, 0, nil},
		{"amount at threshold triggered", 50, 0, 500, 1, map[string]bool{"order_amount": true}},
		{"all three", 10, 50, 900, 3, map[string]bool{"margin_floor": true, "discount_depth": true, "order_amount": true}},
	}
	for _, tc := range cases {
		v := redLineVerdict(tc.marginPct, tc.disc, tc.total)
		if len(v.Triggers) != tc.wantTriggers {
			t.Errorf("%s: triggers = %d (%v), want %d", tc.name, len(v.Triggers), v.Triggers, tc.wantTriggers)
			continue
		}
		if v.AutoApprove != (tc.wantTriggers == 0) {
			t.Errorf("%s: autoApprove = %v", tc.name, v.AutoApprove)
		}
		for _, tr := range v.Triggers {
			if !tc.wantDims[tr.Dimension] {
				t.Errorf("%s: unexpected dimension %q", tc.name, tr.Dimension)
			}
		}
	}
	// 注：「0=关闭该维」的 spec 语义需要 settings 存储注入，不在纯单元
	// 范围（阈值读数面由 spec_sales.go 的默认值与 fallback 一致性保障：
	// R72f 先例——quote.go 的 fallback 与 spec 默认值逐字对齐）。
}

// TestMarginPctOfClamp —— 2026-11-08 审计轮 realdb 腿抓出的 22003 根修钉：
// 收入趋零时毛利比值爆炸（$0.02 收入 vs $140 成本 ⇒ -721300%）必须被钳在
// 945/946 numeric(8,4) 列界内，而不是让版本 INSERT 硬失败。
func TestMarginPctOfClamp(t *testing.T) {
	if p := marginPctOf(-139.98, 0.0194); p == nil || *p > -9999.9999 {
		t.Errorf("explode-negative must clamp to column bound, got %v", p)
	}
	if p := marginPctOf(0, 0); p != nil {
		t.Errorf("zero revenue ⇒ NULL (no revenue no margin), got %v", *p)
	}
	if p := marginPctOf(50, 100); p == nil || *p != 50 {
		t.Errorf("normal path unchanged, got %v", p)
	}
	if p := marginPctOf(-101, 38.9); p == nil || *p > -259 {
		t.Errorf("ordinary negative margin passes through, got %v", p)
	}
}

// TestWithholdNamesFactored —— ListSellable 与派生共用的整组 withhold 判据
// （抽出后两处永不漂移的同源钉）。
func TestWithholdNamesFactored(t *testing.T) {
	p1, p2 := "1.0", "2.0"
	rows := []SellableRow{
		{CanonicalName: "a", SupplierInPer1M: &p1, SupplierOutPer1M: &p2},
		{CanonicalName: "a", SupplierInPer1M: &p2, SupplierOutPer1M: &p1},
		{CanonicalName: "b", SupplierInPer1M: &p1, SupplierOutPer1M: &p2},
		{CanonicalName: "b", SupplierInPer1M: &p1, SupplierOutPer1M: &p2}, // 同价重复
	}
	got := withholdNames(rows)
	if !got["a"] {
		t.Errorf("multi-price group must withhold")
	}
	if got["b"] {
		t.Errorf("same-price duplicates must not withhold")
	}
}

// TestWithholdNamesSkipsNullPrices —— 2026-11-12 复审审计 P1-1 钉：NULL 价行
// 不是价格变体（NULL≠0——parseNum(nil)=0 把它当 0 价是违反红线的计数）。
// trusted 行 + empty 档 NULL 行的同名组不得判 withhold（211 实测 8/12 可售
// 模型被这个误判挡在建单门外的根修）。
func TestWithholdNamesSkipsNullPrices(t *testing.T) {
	p1, p2 := "2.0", "4.0"
	rows := []SellableRow{
		{CanonicalName: "e", SupplierInPer1M: &p1, SupplierOutPer1M: &p2},
		{CanonicalName: "e", SupplierInPer1M: &p1, SupplierOutPer1M: &p2}, // 同价重复
		{CanonicalName: "e"}, // NULL 价（empty 档）
		{CanonicalName: "e"}, // NULL 价（empty 档）
	}
	if got := withholdNames(rows); got["e"] {
		t.Errorf("trusted+NULL same-name group must NOT withhold（NULL 价不是变体）")
	}
}
