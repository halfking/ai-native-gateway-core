package streaming

// `CalcCost` / `AssignRequestCost` 的判据 —— 目标第二半「**准确控制模型的实际
// 成本**」里唯一把 token 变成钱的那一步。
//
// # 为什么需要这一批（2026-10-06）
//
// 既有判据（usage_cost_test.go，4 条）覆盖的是 **AssignRequestCost 的币种与
// nil 语义**：USD、CNY+FX、零价→nil、无 token→nil。
//
// 而 **`CalcCost` 里那两段 cache 扣减、以及负值出口，零判据覆盖** —— 本文件
// 第一次跑就证明了这一点：先写完这 4 条既有判据全绿，再加本文件，第一条
// 负值判据就抓出真库里 1,628 行的负成本。
//
// ⚠️ 顺带更正一句我起初写进本文件头的话：原写「全仓零判据覆盖」是**过头**，
//   被同一次 `go test` 输出里的 4 条既有判据当场证伪。准确说法是上面这句：
//   **cache 分支与负值分支没有判据**。写「零覆盖」会让下一个人以为 USD/币种
//   那几条也不存在，从而重复劳动或误删。
//
// 与本会话早先那条同族教训一致（基准价 sync、核实 dueTargets 都是
// 「判据全绿、核心动作没跑过」）。
//
// # 承重的三件事
//
//	① **无价格 ⇒ nil，不是 0**。nil 与 0 在账上是两件相反的事：0 断言
//	   「这次免费」，nil 承认「算不出来」。健康检查
//	   supplier_price_missing_from_cost 依赖的正是「零价 ⇒ 无成本记录」这条语义；
//	   一旦它改成 return 0，那条检查的整个前提就变成假绿。
//	② **负成本必须被拦住**（2026-10-06 真库实测出来的缺陷，见下）。
//	③ **对照组**：正常的 OpenAI 口径（prompt 含 cache）必须照常算出正数，
//	   免得「一律返回 nil」也能让 ① ② 全绿 —— 那是把成本控制整个关掉。
//
// # 缺陷是怎么被发现的（实测，不是推理）
//
// 真库（127.0.0.1:5432，全程只读）：
//
//	request_logs 里 cost_usd < 0 的行                    = 1,628
//	其中满足 cache_read_tokens > prompt_tokens 的        = 1,628（全部）
//	该口径的行数                                       = 11,837
//	这批行里 cache 占 token 的比例                      = 96.2%
//	                                                    （4,530,529 vs prompt 179,144）
//	全部来自同一家 provider                            = apiclaude（Anthropic 协议的中转）
//	30 天负成本合计                                     = -$4.79
//
// 根因：公式里「cache 从 prompt 里减掉」这一段**假定 prompt_tokens 含 cache
// token**（OpenAI 口径 prompt_tokens ⊇ cached_tokens）。Anthropic 口径相反：
// input_tokens **不含** cache_read_input_tokens
// （internal/ir/response.go 抽的就是 CacheReadInputTokens）。
// 同一份公式喂两种口径 ⇒ cache 一大，promptCost 就被减成负数，整段 cache
// 流量在账上≈免费。
//
// 真正的修法是按协议归一化口径（会改动已记账的金额，属运营决定）；
// 本文件钉住的是「**不再产出负数**」这条安全下限，以及 nil/0 的语义。

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

// ① 无价格 ⇒ nil。零价与「无价」必须同一个出口。
func TestCalcCost_noPriceReturnsNilNotZero(t *testing.T) {
	cases := []struct {
		name string
		in   CostInput
	}{
		{"两个价都是 0", CostInput{PromptTokens: f64(1000), PriceIn: f64(0), PriceOut: f64(0)}},
		{"两个价都是 nil", CostInput{PromptTokens: f64(1000)}},
		{"价与 token 都缺", CostInput{}},
		{"只有 prompt 有 token、价全 nil", CostInput{PromptTokens: f64(5)}},
	}
	for _, tc := range cases {
		got := CalcCost(tc.in)
		if got != nil {
			t.Errorf("%s: CalcCost=%v，期望 nil（=算不出来）。返回 0 会断言「这次免费」，"+
				"那是谎，而且 supplier_price_missing_from_cost 检查的整个前提就变成假绿",
				tc.name, *got)
		}
	}
}

// ② 负成本被拦住。承重的一条。
func TestCalcCost_neverReturnsNegative(t *testing.T) {
	// 这一组就是真库里 apiclaude 的形态：prompt 很少、cache 极多。
	// 41×15.24 − 41550×15.24 + 41550×1.10 = -623,958.24（per-1M 未除）
	cases := []struct {
		name string
		in   CostInput
	}{
		{
			name: "Anthropic 口径：cache 远大于 prompt",
			in: CostInput{
				PromptTokens: f64(41), CompletionTokens: f64(390), CacheReadTokens: f64(41550),
				PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10),
			},
		},
		{
			name: "cache 略大于 prompt",
			in: CostInput{
				PromptTokens: f64(100), CompletionTokens: f64(10), CacheReadTokens: f64(200),
				PriceIn: f64(2.0), PriceOut: f64(2.0), CacheReadPrice: f64(0.2),
			},
		},
		{
			name: "cache 价高于输入价且 cache 大于 prompt",
			in: CostInput{
				PromptTokens: f64(10), CompletionTokens: f64(0), CacheReadTokens: f64(1000),
				PriceIn: f64(1.0), PriceOut: f64(1.0), CacheReadPrice: f64(99.0),
			},
		},
		{
			name: "cache 写入量大于 prompt",
			in: CostInput{
				PromptTokens: f64(5), CompletionTokens: f64(5), CacheWriteTokens: f64(500),
				PriceIn: f64(3.0), PriceOut: f64(3.0), CacheWritePrice: f64(3.75),
			},
		},
	}
	for _, tc := range cases {
		got := CalcCost(tc.in)
		if got == nil {
			continue // 期望的出口：算不出来
		}
		if *got < 0 {
			t.Errorf("%s: CalcCost 返回了负成本 %v —— 负成本不是「便宜」是算错了。"+
				"它会流进台账，任何求和都被污染", tc.name, *got)
		}
	}
	// ★ 子集护栏把真库那个形态**算成了正数**，所以上面这段现在一个都不该红。
	//   这不是「守卫失效了」，而是守卫前面多了一道更靠前的护栏（见下面那条
	//   专门判据）。本条现在守的是**更后面**那道：真正的负价。
	wantPositive := (41*15.24 + 41550*1.10 + 390*75) / 1_000_000.0
	got := calcCostWithConvention(CostInput{
		PromptTokens: f64(41), CompletionTokens: f64(390), CacheReadTokens: f64(41550),
		PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10),
	}, PromptConventionUnset)
	if got == nil {
		t.Fatal("the Anthropic-shaped input must now compute a real cost — the subset guard " +
			"treats it as parallel buckets instead of subtracting 41550×15.24")
	}
	if math.Abs(*got-wantPositive) > 1e-8 {
		t.Errorf("cost=%v want %v (41×15.24 + 41550×1.10 + 390×75)", *got, wantPositive)
	}

	// 守卫仍然承重：单价为负（831 的 CHECK 尚未上生产时可能存在）时必须 nil，
	// 不能把一个负成本写进台账。
	negPrice := calcCostWithConvention(CostInput{
		PromptTokens: f64(1000), CompletionTokens: f64(0),
		PriceIn: f64(-3.0), PriceOut: f64(15.0),
	}, PromptConventionUnset)
	if negPrice != nil {
		t.Errorf("a negative unit price must yield nil, got %v", *negPrice)
	}
	// 负的缓存价（手工改库 'NaN'/负值 的老路径）：要让总成本真的翻负，
	// 负项必须压过正项 —— 所以 cache 要**大于** prompt（2026-10-06 实测踩到：
	// 我第一版用 prompt=1000 / cache=500 / cachePrice=-1，算出 0.0025 是**正数**，
	// 守卫不触发，而判据却期望 nil ⇒ 那条判据写错了，不是代码错了）。
	negCache := calcCostWithConvention(CostInput{
		PromptTokens: f64(100), CacheReadTokens: f64(2000),
		PriceIn: f64(3.0), PriceOut: f64(15.0), CacheReadPrice: f64(-1.0),
	}, ConventionPromptExcludesCache)
	if negCache != nil {
		t.Errorf("a negative cache price dominating the prompt cost must yield nil, got %v", *negCache)
	}
}

// TestCalcCost_subsetGuardStopsTheOriginalOvercharge 钉住那道**更靠前**的护栏。
//
// 背景（2026-10-06）：负成本的根因是公式把「cache ⊆ prompt」当成了恒真。
// 光靠在结果上拦负数不够 —— 那只让成本变成「算不出来」，而正确答案其实是
// 一个**正数**（prompt 与 cache 并列，各自计费）。本条钉住的是后者。
func TestCalcCost_subsetGuardStopsTheOriginalOvercharge(t *testing.T) {
	// 真库形态（apiclaude，cache 是 prompt 的 1012 倍）
	in := CostInput{
		PromptTokens: f64(41), CompletionTokens: f64(390), CacheReadTokens: f64(41550),
		PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10),
	}
	got := calcCostWithConvention(in, PromptConventionUnset)
	if got == nil {
		t.Fatal("must compute, not bail")
	}
	// 未设防的旧算法会算出这个负数（量具自证：证明这个输入形状真的会翻负）
	oldWay := (41*15.24 - 41550*15.24 + 41550*1.10 + 390*75) / 1_000_000.0
	if !(oldWay < 0) {
		t.Fatalf("量具自证失败：旧算法在这个输入上算出 %v（>=0），"+
			"说明这个判据钉的不是真实发生的形态", oldWay)
	}
	if *got <= 0 {
		t.Errorf("cost=%v must be positive — the subset guard should turn this shape into a "+
			"real charge, not into nil", *got)
	}
}

// ③ 对照组：正常口径必须照常算出正数。少了它，「一律返回 nil」也能让 ①② 全绿。
func TestCalcCost_normalUsageStillBills(t *testing.T) {
	// OpenAI 口径：prompt_tokens 含 cached_tokens。100 万 in + 20 万 out。
	got := CalcCost(CostInput{
		PromptTokens: f64(1_000_000), CompletionTokens: f64(200_000),
		PriceIn: f64(3.0), PriceOut: f64(15.0),
	})
	if got == nil {
		t.Fatal("正常口径被判成「算不出来」—— 这不是保守，是把成本控制整个关掉了")
	}
	want := 3.0 + 200_000*15.0/1_000_000.0
	if math.Abs(*got-want) > 1e-6 {
		t.Errorf("cost=%v，期望 %v（100 万×$3 + 20 万×$15）", *got, want)
	}
}

// ③b OpenAI 口径下 cache 应当拿到折扣价而不是被原价计一次。
// 100 万 prompt，其中 80 万是 cache read（价 $0.30），剩 20 万按 $3 计。
func TestCalcCost_cacheReadGetsDiscountPrice(t *testing.T) {
	got := CalcCost(CostInput{
		PromptTokens: f64(1_000_000), CompletionTokens: f64(0), CacheReadTokens: f64(800_000),
		PriceIn: f64(3.0), PriceOut: f64(15.0), CacheReadPrice: f64(0.30),
	})
	if got == nil {
		t.Fatal("OpenAI 口径的 cache read 被判成算不出来")
	}
	want := 200_000*3.0/1_000_000.0 + 800_000*0.30/1_000_000.0 // 0.6 + 0.24
	if math.Abs(*got-want) > 1e-6 {
		t.Errorf("cost=%v，期望 %v —— cache 必须按折扣价算，不能按原价再减一次", *got, want)
	}
}

// AssignRequestCost：无价时三个返回值全 nil（telemetry 于是记 cost_usd IS NULL）。
// 非 USD 时写 native 显示值 + 按固定汇率折 USD。
func TestAssignRequestCost_semantics(t *testing.T) {
	intp := func(v int) *int { return &v }

	// 无价 ⇒ 全 nil
	usd, display, curr := AssignRequestCost(CostPriceInput{
		PromptTokens: intp(1000), PriceIn: f64(0), PriceOut: f64(0), Currency: "USD",
	})
	if usd != nil || display != nil || curr != nil {
		t.Errorf("无价时应全 nil，实得 usd=%v display=%v curr=%v", usd, display, curr)
	}

	// USD ⇒ 只写 usd
	usd, display, curr = AssignRequestCost(CostPriceInput{
		PromptTokens: intp(1_000_000), PriceIn: f64(3.0), PriceOut: f64(0), Currency: "USD",
	})
	if usd == nil || math.Abs(*usd-3.0) > 1e-6 {
		t.Errorf("USD 报价应写 usd=3.0，实得 %v", usd)
	}
	if display != nil || curr != nil {
		t.Errorf("USD 报价不该写 display/currency，实得 %v / %v", display, curr)
	}

	// CNY ⇒ native 显示值 + USD KPI
	usd, display, curr = AssignRequestCost(CostPriceInput{
		PromptTokens: intp(1_000_000), PriceIn: f64(21.6), PriceOut: f64(0), Currency: "CNY",
	})
	if usd == nil || display == nil || curr == nil {
		t.Fatalf("CNY 报价应同时写 usd/display/currency，实得 %v/%v/%v", usd, display, curr)
	}
	if math.Abs(*display-21.6) > 1e-6 {
		t.Errorf("display 应是 native 价 21.6，实得 %v", *display)
	}
	if wantUSD := math.Round(21.6/CNYToUSDFX*1e8) / 1e8; math.Abs(*usd-wantUSD) > 1e-8 {
		t.Errorf("usd=%v，期望按 %v 折算的 %v", *usd, CNYToUSDFX, wantUSD)
	}
	if *curr != "CNY" {
		t.Errorf("currency=%q，期望 CNY", *curr)
	}

	// 真成本形态（apiclaude：cache 是 prompt 的 1012 倍）⇒ 算出**正数**。
	// 这条是 2026-10-06 口径修复的结果：先前这个形态算出负成本、或被守卫
	// 拦成 nil（=那一整段 4.5M cache 流量在账上免费）。现在它是一笔真账。
	usd, display, curr = AssignRequestCost(CostPriceInput{
		PromptTokens: intp(41), CompletionTokens: intp(390), CacheReadTokens: intp(41550),
		PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10), Currency: "USD",
	})
	wantReal := (41*15.24 + 41550*1.10 + 390*75) / 1_000_000.0
	if usd == nil || math.Abs(*usd-wantReal) > 1e-8 {
		t.Errorf("the Anthropic-shaped real request should be charged %v, got %v", wantReal, usd)
	}
	if display != nil || curr != nil {
		t.Errorf("USD pricing should not write display/currency, got %v / %v", display, curr)
	}
}

// ── 口径显式化（2026-10-06）────────────────────────────────────────────
//
// 负成本的**真修法**不是把负数拦住，而是让公式知道上游是哪种口径。
// 本节钉住三种口径各自的行为，以及「零值必须等于原有行为」这条不变量。
//
// 为什么零值必须退回 OpenAI 口径：改了默认口径就是**静默改动已记账的金额**。
// 显式声明口径是运营决定，本轮不自动开启。

// ① 零值 = 不知道 = 原有行为。这条不变量承重：任何「顺手把默认改成 Anthropic
//
//	口径」的重构都会红。
func TestCalcCost_unsetConventionBehavesLikeOpenAI(t *testing.T) {
	in := CostInput{
		PromptTokens: f64(1_000_000), CacheReadTokens: f64(800_000),
		PriceIn: f64(3.0), PriceOut: f64(15.0), CacheReadPrice: f64(0.30),
	}
	viaZero := calcCostWithConvention(in, PromptConventionUnset)
	if viaZero == nil {
		t.Fatal("unset convention returned nil on a normal OpenAI-shaped request")
	}
	// 显式 OpenAI 口径必须给出同一个数
	viaOpenAI := calcCostWithConvention(in, ConventionPromptIncludesCache)
	if viaOpenAI == nil || *viaOpenAI != *viaZero {
		t.Errorf("unset=%v and explicit OpenAI=%v must agree — the zero value has to be the "+
			"legacy behavior, otherwise every already-booked amount silently changes",
			viaZero, viaOpenAI)
	}
}

// ② Anthropic 口径：cache 与 prompt 并列 ⇒ 各自按自己的单价计，**不减原价**。
// 这才是该口径下正确的算式。
func TestCalcCost_anthropicConventionDoesNotSubtract(t *testing.T) {
	// prompt 41 / cache 41550 / p_in 15.24 / p_cache 1.10 / p_out 75
	//   正确 = 41*15.24 + 41550*1.10 + 390*75  （per 1M）
	//   错误 = 同上再减 41550*15.24          ⇒ 负数
	got := calcCostWithConvention(CostInput{
		PromptTokens: f64(41), CompletionTokens: f64(390), CacheReadTokens: f64(41550),
		PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10),
	}, ConventionPromptExcludesCache)
	if got == nil {
		t.Fatal("Anthropic convention must compute a real cost, not bail out")
	}
	want := (41*15.24 + 41550*1.10 + 390*75) / 1_000_000.0
	if math.Abs(*got-want) > 1e-8 {
		t.Errorf("cost=%v, want %v (41×15.24 + 41550×1.10 + 390×75, no cache subtraction)", *got, want)
	}
	if *got < 0 {
		t.Errorf("Anthropic convention produced a negative cost %v", *got)
	}
}

// ③ 三种口径在同一输入上的分工：unset == OpenAI，且三者都非负。
//
//	承重的是「三档都不产生负数」—— 那是这个缺陷真正的底线。
func TestCalcCost_allConventionsStayNonNegative(t *testing.T) {
	inputs := []CostInput{
		{PromptTokens: f64(41), CompletionTokens: f64(390), CacheReadTokens: f64(41550),
			PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10)},
		{PromptTokens: f64(100), CompletionTokens: f64(10), CacheReadTokens: f64(200),
			PriceIn: f64(2.0), PriceOut: f64(2.0), CacheReadPrice: f64(0.2)},
		{PromptTokens: f64(1_000_000), CompletionTokens: f64(0), CacheReadTokens: f64(800_000),
			PriceIn: f64(3.0), PriceOut: f64(15.0), CacheReadPrice: f64(0.30)},
		{PromptTokens: f64(1_000_000), CompletionTokens: f64(200_000),
			PriceIn: f64(3.0), PriceOut: f64(15.0)},
	}
	convs := []CacheTokenConvention{
		PromptConventionUnset,
		ConventionPromptIncludesCache,
		ConventionPromptExcludesCache,
	}
	for i, in := range inputs {
		for _, c := range convs {
			got := calcCostWithConvention(in, c)
			if got == nil {
				t.Errorf("input %d / convention %s: nil — it should compute, not bail", i, c)
				continue
			}
			if *got < 0 {
				t.Errorf("input %d / convention %s: negative cost %v", i, c, *got)
			}
		}
	}
}

// ④ Convention 的 String() 被日志与判据引用 ⇒ 拼错的枚举值必须显形而不是空串。
func TestCacheTokenConvention_String(t *testing.T) {
	cases := map[CacheTokenConvention]string{
		PromptConventionUnset:         "unset",
		ConventionPromptIncludesCache: "prompt_includes_cache",
		ConventionPromptExcludesCache: "prompt_excludes_cache",
		CacheTokenConvention(99):      "unset",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("CacheTokenConvention(%d).String()=%q want %q", int(c), got, want)
		}
	}
}

// ⑤ AssignRequestCost 必须把 Convention 传下去 —— 它是 handler 唯一能声明口径的入口。
//
//	少了这一句，上面四条对纯函数全绿，而**生产路径**永远拿不到口径。
func TestAssignRequestCost_passesConventionThrough(t *testing.T) {
	intp := func(v int) *int { return &v }
	base := CostPriceInput{
		PromptTokens: intp(41), CompletionTokens: intp(390), CacheReadTokens: intp(41550),
		PriceIn: f64(15.24), PriceOut: f64(75), CacheReadPrice: f64(1.10), Currency: "USD",
	}
	// unset ⇒ 子集护栏把真库那个形态算成**同一个正数**（不是 nil、不是负数）。
	// 这条是「默认值不静默改变已记账金额」的体现：unset 与显式 Anthropic
	// 口径在这个输入上同解。
	usd, _, _ := AssignRequestCost(base)
	if usd == nil {
		t.Fatal("unset convention must still charge the Anthropic-shaped input " +
			"(the subset guard makes it a positive cost, not a bail-out)")
	}
	want := (41*15.24 + 41550*1.10 + 390*75) / 1_000_000.0
	if math.Abs(*usd-want) > 1e-8 {
		t.Errorf("unset cost=%v want %v", *usd, want)
	}
	// 显式 Anthropic 口径 ⇒ 算出正数
	withConv := base
	withConv.Convention = ConventionPromptExcludesCache
	usd2, _, _ := AssignRequestCost(withConv)
	if usd2 == nil {
		t.Fatal("AssignRequestCost dropped the Convention — the production path can never " +
			"declare the upstream token convention")
	}
	if math.Abs(*usd2-want) > 1e-8 {
		t.Errorf("cost=%v want %v", *usd2, want)
	}
	// ★ unset 与显式 Anthropic 口径在这个输入上必须**同解** —— 子集护栏
	//   让「没说清口径」也能算出正确金额，所以默认值不需要阻塞上线。
	if math.Abs(*usd2-*usd) > 1e-8 {
		t.Errorf("unset=%v vs explicit Anthropic=%v must agree on the real-world shape", *usd, *usd2)
	}
}

// ── teeth T5 的补强：口径真的被透传了（2026-10-06）────────────────────
//
// 变异实测：把 AssignRequestCost 里的 `}, in.Convention)` 换成
// `}, PromptConventionUnset)`，**上面 14 条判据全绿**。
//
// 根因：我用来区分「unset 与显式 Anthropic 口径」的那个输入
// （prompt 41 / cache 41550）里 **cache ⊄ prompt**（subset=false）⇒ 子集护栏
// 已经让它在两种口径下同解 ⇒ 传不传 Convention 观察不到差别。
// 判据的缺口是：**它没挑一个能区分两者的输入**。
//
// 这条用 subset=true 的输入（cache 是 prompt 的子集）：此时两种口径相差
// 恰好 cache×priceIn，非零。
func TestAssignRequestCost_conventionActuallyReachesCalcCost(t *testing.T) {
	intp := func(v int) *int { return &v }
	// prompt 1000 / cache 500 —— cache ⊆ prompt，两种口径下算法不同。
	in := CostPriceInput{
		PromptTokens: intp(1000), CompletionTokens: intp(0), CacheReadTokens: intp(500),
		PriceIn: f64(3.0), PriceOut: f64(15.0), CacheReadPrice: f64(0.30), Currency: "USD",
	}
	unset, _, _ := AssignRequestCost(in)
	if unset == nil {
		t.Fatal("unset must compute")
	}
	// OpenAI 口径：(1000-500)×3.0 + 500×0.30 = 1500 + 150 = 1650 / 1e6
	wantOpenAI := (1000*3.0 - 500*3.0 + 500*0.30) / 1_000_000.0
	if math.Abs(*unset-wantOpenAI) > 1e-8 {
		t.Errorf("unset=%v want %v (OpenAI convention)", *unset, wantOpenAI)
	}

	anthropic := in
	anthropic.Convention = ConventionPromptExcludesCache
	got, _, _ := AssignRequestCost(anthropic)
	if got == nil {
		t.Fatal("explicit Anthropic convention must compute")
	}
	// Anthropic 口径：1000×3.0 + 500×0.30 = 3000 + 150 = 3150 / 1e6
	wantAnthropic := (1000*3.0 + 500*0.30) / 1_000_000.0
	if math.Abs(*got-wantAnthropic) > 1e-8 {
		t.Errorf("Anthropic convention=%v want %v (no original-price deduction)", *got, wantAnthropic)
	}
	// ★ 承重：两者必须**不同**。相等就说明 Convention 根本没被传下去
	//   （teeth T5 实测过这个形态：传不传都全绿）。
	if math.Abs(*got-*unset) < 1e-9 {
		t.Errorf("unset=%v and Anthropic=%v are identical — the Convention field is not "+
			"reaching CalcCost, so declaring the upstream convention is a no-op", *unset, *got)
	}
}

// ── 「Convention 已铺好但生产侧没人设」是**声明过的缺口**，不是能用的功能 ──
//
// 2026-10-06 实测：把二进制构建出来之后 `strings` 量到
// `prompt_excludes_cache` / `prompt_includes_cache` 在产物里是 **0** ——
// 因为 `CacheTokenConvention.String()` 只有判据在调，生产代码零调用者；
// 而 `Convention` 虽然铺到了 `CostPriceInput`，handler 的
// `AssignRequestCost` 调用**不传**它 ⇒ 永远是零值。
//
// 这正是本会话反复遇到的那个形状：机制做完了，但**没有人能打开它**。
// 本条把它钉成一条**显式的、有人知道**的缺口，而不是一句注释。

// ① 日志必须说出它用的是哪一档假定（否则读日志的人以为已归一化过了）。
func TestCalcCost_negativeGuardLogsTheAssumedConvention(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	// 负的输入价 ⇒ 守卫必然触发（构造一个不依赖 cache 口径的触发条件）。
	calcCostWithConvention(CostInput{
		PromptTokens: f64(1000), CompletionTokens: f64(0),
		PriceIn: f64(-3.0), PriceOut: f64(15.0),
	}, PromptConventionUnset)

	out := buf.String()
	if !strings.Contains(out, "assumed_convention") {
		t.Errorf("the negative-cost warning must state which token convention it assumed; "+
			"without it a reader assumes the upstream protocol was already normalized. Log:\n%s", out)
	}
	if !strings.Contains(out, "unset") {
		// 注：`unset` 现在仍然是**生产可达**的 —— CacheTokenConventionForProtocol
		// 对非 anthropic-messages 协议（含空）返回 PromptConventionUnset，
		// 而零值分支必须老实说「不知道」。2026-10-06 之前这条注释写的是
		// 「那就是当前生产状态：没人设 Convention」，接线后那句话已不成立。
		t.Errorf("the log must say `unset` for the zero value — a zero value means "+
			"\"unknown, fell back to the original behaviour\", and production still reaches "+
			"it (every non-anthropic-messages protocol). Log:\n%s", out)
	}
}

// ② 生产侧的口径**由上游协议带下来**（2026-10-06 起）—— 钉住新契约。
//
// ★ 这条判据原来叫 TestConvention_productionHasNoCallerYet，断言「**没有**
// 生产调用方设置 Convention」。它自己的注释写着：
//
//	「将来真的接上了（比如按 binding 存一列、或按协议推断），它会红 ——
//	  那是**应该**红的：届时请把这里的期望改成新的数，并补上「谁在什么时候
//	  设它」的说明。」
//
// 2026-10-06 正是那一天：按**协议**接上了（不是按 binding 存列）。下面就是
// 按它自己的指示改写后的契约。**请不要把它改回「没有调用方」** —— 那等于
// 把 Anthropic 流量按 OpenAI 口径算回去。
//
// # 谁在什么时候设它
//
//	domains/streaming/handler.go 的 AssignRequestCost 调用处（唯一的生产调用点）
//	  Convention: CacheTokenConventionForProtocol(result.Candidate.Protocol)
//
//	协议与四个价字段来自候选 SQL 的**同一行**（provider/client.go:1129），
//	且该 SQL 有 `COALESCE(p.protocol,'') <> ''`（:1140）保证非空。
//	判定逻辑见 domains/streaming/usage.go 的 CacheTokenConventionForProtocol。
//
// # 判定依据（真库 127.0.0.1:5432，只读）
//
//	providers.protocol 三个取值、零 NULL（54/4/2 家）
//	cache > prompt 的异常完全集中在 anthropic-messages：
//	  openai-completions  0.0% (75,461 行)
//	  anthropic-messages 88.1% (12,112 行，cache/prompt 均值 57.03)
//	改动影响面：只有「cache ⊆ prompt 且协议=anthropic」的近 30 天 **8 行 / $5.28**。
//
// # 为什么钉「必须由协议推导」而不是「必须等于某个常量」
//
//	写死 `Convention: ConventionPromptExcludesCache` 会让**所有**上游都按
//	Anthropic 口径算 —— 包括那 5,958 行 openai-completions（$744.39，扣减本来
//	是对的）。这条断言专门抓那种偷懒写法。
func TestConvention_productionCallerIsProtocolDerived(t *testing.T) {
	h, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	hsrc := string(h)
	i := strings.Index(hsrc, "AssignRequestCost(CostPriceInput{")
	if i < 0 {
		t.Fatal("AssignRequestCost call site not found in handler.go — " +
			"this judgment pins that specific call site")
	}
	call := hsrc[i:min(i+900, len(hsrc))]
	if !strings.Contains(call, "Convention:") {
		t.Errorf("handler.go 的生产调用点又不传 Convention 了 —— 那会让 Anthropic 流量" +
			"回到按 OpenAI 口径扣 cache（真库：88.1%% 的 anthropic 行 cache>prompt）。" +
			"若确定要退回人工声明，请同时把 usage.go 里「按协议判定」那段注释改掉，" +
			"别让代码和注释各说一套")
	}
	if !strings.Contains(call, "CacheTokenConventionForProtocol(result.Candidate.Protocol)") {
		t.Errorf("Convention 必须由**上游协议**推导，不能写死常量。写死 "+
			"ConventionPromptExcludesCache 会让 5,958 行 openai-completions "+
			"（$744.39，扣减本来是对的）也按 Anthropic 口径算。调用点现状：\n%s", call)
	}

	// usage.go 里仍然不允许出现「构造时写死档位」的字面量赋值 ——
	// 判定集中在 CacheTokenConventionForProtocol 那个函数里。
	f, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatalf("read usage.go: %v", err)
	}
	if i := strings.Index(string(f), "Convention: "); i >= 0 {
		src := string(f)
		t.Errorf("usage.go 里出现了 `Convention: ` 字面量赋值（%q）—— "+
			"档位应当只由 CacheTokenConventionForProtocol 推导，别在别处再写死一次",
			src[i:min(i+60, len(src))])
	}
}
