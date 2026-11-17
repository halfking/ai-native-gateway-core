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

func ptrI(v int64) *int64 { return &v }

// ── 2026-11-17 新增：现价口径的行为门（全局基准不得冒充「当前零售价」）──
//
// ★ 这组门守的是一个**已经在生产露过脸的缺陷**：
//   211 线上 aion-2.0 市场价 5.34 CNY，而我方该模型**没有自己的零售价**
//   ⇒ 旧实现回落到全局基准（600 credits × 0.82 折扣 = 492 credits = 0.0492 CNY）
//   ⇒ delta 算出 **10759%** 的假偏离，并把保守阈值过滤污染成一个笑话。
//   ⇒ 下面的用例**直接复现生产数字**，而不是造一组抽象数。

// TestPricingSuggestionRateIsOwn —— 「自有价」判据：必须与 pickCreditRate 同源。
func TestPricingSuggestionRateIsOwn(t *testing.T) {
	if pricingSuggestionRateIsOwn(nil) {
		t.Fatal("nil 存储值不是自有价")
	}
	// ★ 存了 0 视作「未设」（pickCreditRate: 值>0 才算）⇒ 不是自有价。
	if pricingSuggestionRateIsOwn(ptrI(0)) {
		t.Fatal("存储值 0 视作未设（实际取全局基准）⇒ 不能算自有价，否则假偏离复活")
	}
	if !pricingSuggestionRateIsOwn(ptrI(1)) {
		t.Fatal("存储值 1 应算自有价")
	}
	if !pricingSuggestionRateIsOwn(ptrI(1200)) {
		t.Fatal("存储值 1200 应算自有价")
	}
}

// TestPricingSuggestionCurrentPricesForFallbackIsNotOwn ——
// 兜底值可以带着，但**必须**被标成「不是自有价」。
func TestPricingSuggestionCurrentPricesForFallbackIsNotOwn(t *testing.T) {
	const globalCredits = 492 // 600 × 0.82（211 生产全局基准）
	// 场景一：模型**没有**存储价 ⇒ 有效价 = 全局基准，但显式性必须为 false。
	got := pricingSuggestionCurrentPricesFor(false, nil, false, nil, globalCredits, globalCredits)
	if got.In == nil || *got.In != globalCredits {
		t.Fatalf("无存储价时有效价应回落全局基准 %d，实得 %v", globalCredits, got.In)
	}
	if got.Out == nil || *got.Out != globalCredits {
		t.Fatalf("无存储价时有效价应回落全局基准 %d，实得 %v", globalCredits, got.Out)
	}
	if got.InExplicit || got.OutExplicit {
		t.Fatalf("全局基准兜底**不得**标成自有价（否则现价栏又变回兜底 ⇒ 假偏离复活）：%+v", got)
	}
	// 场景二：模型有存储价 ⇒ 有效价取存储值，且显式性为 true。
	got = pricingSuggestionCurrentPricesFor(true, ptrI(534), true, ptrI(1200), globalCredits, globalCredits)
	if got.In == nil || *got.In != 534 || got.Out == nil || *got.Out != 1200 {
		t.Fatalf("有存储价时应取存储值 534/1200，实得 %v/%v", got.In, got.Out)
	}
	if !got.InExplicit || !got.OutExplicit {
		t.Fatalf("有存储价必须标成自有价：%+v", got)
	}
	// 场景三：存储了 0 ⇒ 有效价回落到全局基准，且**不是**自有价。
	got = pricingSuggestionCurrentPricesFor(false, ptrI(0), false, ptrI(0), globalCredits, globalCredits)
	if got.InExplicit || got.OutExplicit {
		t.Fatalf("存储值 0 时不得标成自有价：%+v", got)
	}
}

// TestPricingSuggestionApplyCurrentSkipsFallback ——
// ★ 核心行为门：现价栏只认模型自有价；兜底值即便存在也不写进去。
func TestPricingSuggestionApplyCurrentSkipsFallback(t *testing.T) {
	const centsPerCredit = 0.01 // 211 生产：1 credit = 0.01 CNY
	// 兜底价（全局基准 492 credits）与真实市场价无关 —— 绝不能进现价栏。
	fallback := pricingSuggestionCurrentPrices{
		In: ptrI(492), Out: ptrI(492),
		InExplicit: false, OutExplicit: false,
	}
	var row PricingSuggestionRow
	pricingSuggestionApplyCurrent(&row, fallback, centsPerCredit, "CNY")
	if row.CurrentCreditsIn != nil || row.CurrentCreditsOut != nil {
		t.Fatalf("无自有价时现价栏必须为空（前端显示「—」），实得 %v/%v",
			row.CurrentCreditsIn, row.CurrentCreditsOut)
	}
	if row.CurrentCNYIn != nil || row.CurrentCNYOut != nil {
		t.Fatalf("无自有价时现价 CNY 必须为空，实得 %v/%v", row.CurrentCNYIn, row.CurrentCNYOut)
	}
	if row.CurrentBasis != "none" {
		t.Fatalf("现价来源应为 none，实得 %q", row.CurrentBasis)
	}
	// 有自有价 ⇒ 写进去，并换算成 CNY。
	// ★ 量纲（211 生产口径）：CNY = credits × cents_per_credit / 100
	//   （cents_per_credit = 0.01 ⇒ 1 credit = 0.0001 CNY）
	//   ⇒ 50000 credits = 5.00 CNY（1M tokens 的市价量级）。
	var own PricingSuggestionRow
	pricingSuggestionApplyCurrent(&own, pricingSuggestionCurrentPrices{
		In: ptrI(50000), Out: ptrI(120000),
		InExplicit: true, OutExplicit: true,
	}, centsPerCredit, "CNY")
	if own.CurrentCreditsIn == nil || *own.CurrentCreditsIn != 50000 {
		t.Fatalf("自有价应写入现价栏，实得 %v", own.CurrentCreditsIn)
	}
	if own.CurrentCNYIn == nil || math.Abs(*own.CurrentCNYIn-5.0) > 1e-9 {
		t.Fatalf("现价 CNY 换算错：50000 × 0.01 / 100 应为 5.00，实得 %v", own.CurrentCNYIn)
	}
	if own.CurrentCNYOut == nil || math.Abs(*own.CurrentCNYOut-12.0) > 1e-9 {
		t.Fatalf("现价 CNY 换算错：120000 × 0.01 / 100 应为 12.00，实得 %v", own.CurrentCNYOut)
	}
	if own.CurrentBasis != "explicit" {
		t.Fatalf("现价来源应为 explicit，实得 %q", own.CurrentBasis)
	}
	// 只有一轴有自有价 ⇒ partial（且另一轴保持为空）。
	var partial PricingSuggestionRow
	pricingSuggestionApplyCurrent(&partial, pricingSuggestionCurrentPrices{
		In: ptrI(50000), Out: ptrI(492),
		InExplicit: true, OutExplicit: false,
	}, centsPerCredit, "CNY")
	if partial.CurrentBasis != "partial" {
		t.Fatalf("单轴自有价的现价来源应为 partial，实得 %q", partial.CurrentBasis)
	}
	if partial.CurrentCreditsOut != nil || partial.CurrentCNYOut != nil {
		t.Fatalf("无自有价的轴必须保持为空，实得 %v/%v",
			partial.CurrentCreditsOut, partial.CurrentCNYOut)
	}
}

// TestPricingSuggestionNoFakeDeltaOnProductionNumbers ——
// ★ 用 **211 生产的真实数字**复现假偏离：修前 delta = +10759%（荒谬）。
func TestPricingSuggestionNoFakeDeltaOnProductionNumbers(t *testing.T) {
	const centsPerCredit = 0.01
	marketCNY := 5.34 // aion-2.0 的市场最低价（CNY/1M）
	// 该模型没有自己的零售价 ⇒ 有效价是全局基准 492 credits。
	current := pricingSuggestionCurrentPrices{
		In: ptrI(492), Out: ptrI(492),
		InExplicit: false, OutExplicit: false,
	}
	var row PricingSuggestionRow
	row.SuggestedCNYIn = ptrF(marketCNY)
	row.SuggestedCNYOut = ptrF(marketCNY)
	pricingSuggestionApplyCurrent(&row, current, centsPerCredit, "CNY")
	row.DeltaPctIn = pricingDeltaPct(row.CurrentCNYIn, row.SuggestedCNYIn)
	row.DeltaPctOut = pricingDeltaPct(row.CurrentCNYOut, row.SuggestedCNYOut)
	if row.DeltaPctIn != nil || row.DeltaPctOut != nil {
		t.Fatalf("无自有现价时**不得**产出幅度（修前这里会算出 +10759%% 的假偏离），实得 %v/%v",
			row.DeltaPctIn, row.DeltaPctOut)
	}
	// 对照：若**真有**自己的价（5.00 CNY），幅度必须照算（口径没被误伤）。
	var own PricingSuggestionRow
	own.SuggestedCNYIn = ptrF(marketCNY)
	pricingSuggestionApplyCurrent(&own, pricingSuggestionCurrentPrices{
		In: ptrI(50000), Out: ptrI(50000),
		InExplicit: true, OutExplicit: true,
	}, centsPerCredit, "CNY")
	own.DeltaPctIn = pricingDeltaPct(own.CurrentCNYIn, own.SuggestedCNYIn)
	if own.DeltaPctIn == nil {
		t.Fatal("有自有现价时必须产出幅度（修口径不能把真幅度也修没了）")
	}
	if math.Abs(*own.DeltaPctIn-6.8) > 1e-6 {
		t.Fatalf("幅度算错：5.34/5.00-1 应为 6.8%%，实得 %v", *own.DeltaPctIn)
	}
}

// TestPricingSuggestionDecision —— 可执行性判定矩阵。
func TestPricingSuggestionDecision(t *testing.T) {
	sug := ptrI(534)
	none := (*int64)(nil)
	delta := 20.0

	// ① 没产出建议价 ⇒ 不可执行。
	if ok, _ := pricingSuggestionDecision(none, none, true, &delta, nil, nil); ok {
		t.Fatal("无建议价必须不可执行")
	}
	// ② 无自有现价 + 保守阈值（>0）⇒ **不参与**（没有偏离可比）。
	if ok, reason := pricingSuggestionDecision(sug, sug, false, nil, nil, ptrF(50.0)); ok {
		t.Fatal("无自有现价 + 阈值模式必须不参与（否则用全局基准当偏离）")
	} else if reason == "" {
		t.Fatal("跳过必须带原因（审计要看得懂为什么没动）")
	}
	// ③ 无自有现价 + 无阈值 ⇒ **可执行**（首次定价是主场景）。
	if ok, reason := pricingSuggestionDecision(sug, sug, false, nil, nil, nil); !ok {
		t.Fatalf("无自有现价 + 无阈值应可执行（首次定价），却跳过：%s", reason)
	}
	// ④ 有自有现价 + 幅度超阈值 ⇒ 可执行；幅度不足 ⇒ 跳过。
	if ok, _ := pricingSuggestionDecision(sug, sug, true, &delta, nil, ptrF(50.0)); ok {
		t.Fatal("幅度 20% 达不到阈值 50% ⇒ 应跳过")
	}
	if ok, _ := pricingSuggestionDecision(sug, sug, true, &delta, nil, ptrF(20.0)); !ok {
		t.Fatal("幅度 20% 达到阈值 20% ⇒ 应可执行")
	}
	// ⑤ 有自有现价但一个幅度都算不出来 ⇒ 跳过（无从比较）。
	if ok, _ := pricingSuggestionDecision(sug, sug, true, nil, nil, nil); ok {
		t.Fatal("无幅度可比时必须跳过")
	}
	// ⑥ 负幅度（跌价）同样要能过阈值。
	down := -30.0
	if ok, _ := pricingSuggestionDecision(sug, sug, true, &down, nil, ptrF(20.0)); !ok {
		t.Fatal("跌价 -30% 也应达到阈值 20%（跌价同样要提示）")
	}
}
