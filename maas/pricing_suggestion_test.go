package maas

// pricing_suggestion_test.go —— 定价建议引擎的行为门（2026-11-17）。
//
// ★ 为什么必须是**行为门**而不是文本门：
//   定价建议的核心风险有两类，都不是「函数名写没写对」能测出来的 ——
//     ① **换算口径错**（credits ⇄ 钱 反算错一个数量级 ⇒ 建议价整体离谱，
//        但代码照样「跑通了」）；
//     ② **红线越界**（把行情价自动写进计费面 / 把「无行情」当 0 价写下去）。
//   ⇒ 这里直接**喂数字、量输出**。

import (
	"math"
	"testing"
)

func TestCNYToCreditsRoundTrip(t *testing.T) {
	// 1 credit = 0.01 CNY（cents_per_credit = 1 分）
	// ⇒ 100 CNY / 1M tokens = 10000 credits / 1M
	const cents = 1.0
	cny := 100.0
	got := cnyToCredits(&cny, cents)
	if got == nil {
		t.Fatal("cnyToCredits 返回 nil（应产出换算值）")
	}
	if *got != 10000 {
		t.Fatalf("credits 换算错：100 CNY @ 1分/credit ⇒ 10000 credits，实得 %d", *got)
	}
	// 反算（与 market_price_reference.go 的 credits×cents/100 同源）
	back := float64(*got) * cents / 100.0
	if math.Abs(back-cny) > 1e-9 {
		t.Fatalf("往返不自洽：%v → %d credits → %v CNY", cny, *got, back)
	}
}

func TestCNYToCreditsNeverZeroOrNil(t *testing.T) {
	// ★ NULL≠0：0 credits 会被当成「免费」⇒ 夹到 1；缺参数 ⇒ nil（不产出）。
	zero := 0.0
	got := cnyToCredits(&zero, 1.0)
	if got == nil {
		t.Fatal("0 CNY 应夹成 1 credit（不是 nil：nil 会让建议消失，0 会变成免费）")
	}
	if *got != 1 {
		t.Fatalf("0 CNY 应夹成 1 credit，实得 %d", *got)
	}
	if cnyToCredits(nil, 1.0) != nil {
		t.Fatal("缺价必须返回 nil（未知 ≠ 免费）")
	}
	if cnyToCredits(&zero, 0) != nil {
		t.Fatal("cents_per_credit 缺失时必须返回 nil（不产出建议）")
	}
}

func TestApplyMarkup(t *testing.T) {
	cost := 200.0
	markup := 20.0
	got := applyMarkup(&cost, markup)
	if got == nil {
		t.Fatal("applyMarkup 返回 nil")
	}
	if math.Abs(*got-240) > 1e-9 {
		t.Fatalf("加成算错：200 × (1+20%%) 应为 240，实得 %v", *got)
	}
	// 加成率 ≤ -100 ⇒ 负价 ⇒ 不产出（不夹 0、不猜）
	if applyMarkup(&cost, -100) != nil {
		t.Fatal("markup=-100 应不产出建议（负价）")
	}
	if applyMarkup(&cost, -150) != nil {
		t.Fatal("markup=-150 应不产出建议（负价）")
	}
	if applyMarkup(nil, 20) != nil {
		t.Fatal("缺成本价必须返回 nil")
	}
}

func TestPricingSuggestionStrategyPick(t *testing.T) {
	row := pricingSuggestionMarketRow{
		PlatformCNYIn: ptrF(3.0), PlatformCNYOut: ptrF(15.0),
		AvgCNYIn: ptrF(5.0), AvgCNYOut: ptrF(20.0),
		MinCNYIn: ptrF(2.0), MinCNYOut: ptrF(9.0),
		FloorCNYIn: ptrF(10.0), FloorCNYOut: ptrF(40.0),
		SampleCount: 5,
	}
	// copy_platform ⇒ 取目标平台价
	in, out, basis, n := pricingSuggestionStrategyPick(row, PricingSuggestionRequest{
		Strategy: PricingStrategyCopyPlatform, PlatformID: "siliconflow"})
	if in == nil || *in != 3.0 || out == nil || *out != 15.0 {
		t.Fatalf("copy_platform 取价错：%v/%v", in, out)
	}
	if basis == "" || n != 1 {
		t.Fatalf("copy_platform 依据/样本数错：%q/%d", basis, n)
	}
	// average ⇒ 取均价
	in, out, _, _ = pricingSuggestionStrategyPick(row, PricingSuggestionRequest{Strategy: PricingStrategyAverage})
	if in == nil || *in != 5.0 || out == nil || *out != 20.0 {
		t.Fatalf("average 取价错：%v/%v", in, out)
	}
	// min ⇒ 取最低
	in, out, _, _ = pricingSuggestionStrategyPick(row, PricingSuggestionRequest{Strategy: PricingStrategyMin})
	if in == nil || *in != 2.0 || out == nil || *out != 9.0 {
		t.Fatalf("min 取价错：%v/%v", in, out)
	}
	// floor_markup ⇒ 地板 × (1+加成)
	markup := 25.0
	in, out, basis, n = pricingSuggestionStrategyPick(row, PricingSuggestionRequest{
		Strategy: PricingStrategyFloorMarkup, MarkupPct: &markup})
	if in == nil || math.Abs(*in-12.5) > 1e-9 || out == nil || math.Abs(*out-50) > 1e-9 {
		t.Fatalf("floor_markup 取价错：%v/%v（应 12.5/50）", in, out)
	}
	if basis == "" || n != 5 {
		t.Fatalf("floor_markup 依据/样本数错：%q/%d", basis, n)
	}
}

func TestValidatePricingStrategy(t *testing.T) {
	if err := validatePricingStrategy(PricingSuggestionRequest{Strategy: PricingStrategyCopyPlatform}); err == nil {
		t.Fatal("copy_platform 缺 platform_id 必须报错")
	}
	if err := validatePricingStrategy(PricingSuggestionRequest{
		Strategy: PricingStrategyCopyPlatform, PlatformID: "openai"}); err != nil {
		t.Fatalf("copy_platform 带 platform_id 应通过：%v", err)
	}
	if err := validatePricingStrategy(PricingSuggestionRequest{Strategy: PricingStrategyFloorMarkup}); err == nil {
		t.Fatal("floor_markup 缺 markup_pct 必须报错")
	}
	neg := -120.0
	if err := validatePricingStrategy(PricingSuggestionRequest{
		Strategy: PricingStrategyFloorMarkup, MarkupPct: &neg}); err == nil {
		t.Fatal("markup_pct < -100 必须报错（负价）")
	}
	if err := validatePricingStrategy(PricingSuggestionRequest{Strategy: "nope"}); err == nil {
		t.Fatal("未知策略必须报错")
	}
	for _, s := range []string{PricingStrategyAverage, PricingStrategyMin} {
		if err := validatePricingStrategy(PricingSuggestionRequest{Strategy: s}); err != nil {
			t.Fatalf("策略 %s 应通过：%v", s, err)
		}
	}
}

func TestPricingDeltaPctAndThreshold(t *testing.T) {
	cur := 100.0
	sug := 120.0
	d := pricingDeltaPct(&cur, &sug)
	if d == nil || math.Abs(*d-20) > 1e-9 {
		t.Fatalf("幅度算错：120/100-1 应为 20%%，实得 %v", d)
	}
	if pricingDeltaPct(nil, &sug) != nil {
		t.Fatal("缺当前价必须 nil（不产出幅度）")
	}
	if pricingDeltaPct(&cur, nil) != nil {
		t.Fatal("缺建议价必须 nil（不产出幅度）")
	}
	zero := 0.0
	if pricingDeltaPct(&zero, &sug) != nil {
		t.Fatal("当前价为 0 时必须 nil（除零）")
	}
	// 阈值（判据 = |幅度| ≥ 阈值 ⇒ 达到；阈值含边界）
	threshold := 20.0
	if !pricingDeltaMeaningful(d, nil, &threshold) {
		t.Fatal("幅度 20% 恰好等于阈值 20% ⇒ 应判为「达到阈值」（判据是 ≥）")
	}
	high := 50.0
	if !pricingDeltaMeaningful(&high, nil, &threshold) {
		t.Fatal("幅度 50% 应达到阈值 20%")
	}
	small := 5.0
	if pricingDeltaMeaningful(&small, nil, &threshold) {
		t.Fatal("幅度 5% 不应达到阈值 20%")
	}
	// 负幅度同样要能被阈值拦（跌价也要提示）
	down := -30.0
	if !pricingDeltaMeaningful(&down, nil, &threshold) {
		t.Fatal("幅度 -30% 应达到阈值 20%（跌价同样要提示）")
	}
	// 阈值 nil ⇒ 视作 0 ⇒ 任何非零幅度都算「有变化」
	if !pricingDeltaMeaningful(&small, nil, nil) {
		t.Fatal("阈值缺省时应视作 0 ⇒ 非零幅度即算有变化")
	}
	// 两轴都缺幅度 ⇒ 无变化
	if pricingDeltaMeaningful(nil, nil, nil) {
		t.Fatal("两轴都没有幅度 ⇒ 不算有变化")
	}
}

func ptrF(v float64) *float64 { return &v }
