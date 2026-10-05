package streaming

import (
	"testing"
)

// CacheTokenConventionForProtocol 的判据（2026-10-06）。
//
// # 契约
//
//	anthropic-messages  → ConventionPromptExcludesCache（cache ∥ prompt）
//	其他一切（含空）    → PromptConventionUnset（= 原有 OpenAI 行为）
//
// # 为什么「其他一切 → Unset」是安全边界而不是省事
//
// 返回 Unset 时 calcCostWithConvention 走的就是改动前的分支，**逐字节相同**。
// 所以这个函数在结构上不可能影响那 56 家非 Anthropic 供应商的已记账金额 ——
// 它只改本该改的那一个协议。判据把这条边界钉住：任何非 anthropic 的取值
// （包括大小写变体、带空白的、未来新增的协议名）都必须落回 Unset。
//
// # 真实库依据（127.0.0.1:5432，只读）
//
//	providers.protocol 只有三个取值、零 NULL：
//	  openai-completions 54 / anthropic-messages 4 / openai-responses 2
//	cache > prompt 的异常完全集中在 anthropic-messages：
//	  openai-completions  0.0% (75,461 行)
//	  anthropic-messages 88.1% (12,112 行，cache/prompt 均值 57.03)
//	⇒ 协议是完美判别式，不是弱相关。
//
// 钱的方向（近 30 天 claude% 且有 cache 的流量，用实际报价）：
//	  凭据 17（11,392 行）现记 −$2.86；OpenAI 口径 +$12.47；Anthropic 口径 +$45.81
//	  凭据 31（57 行，prompt 177 / cache 1,162,318）现记 −$1.65；
//	    OpenAI 口径仍 −$11.38；Anthropic 口径 +$6.05
//	⇒ 只有 Anthropic 口径在两个凭据上都给出正数。

func TestCacheTokenConventionForProtocol(t *testing.T) {
	cases := []struct {
		protocol string
		want     CacheTokenConvention
		why      string
	}{
		{"anthropic-messages", ConventionPromptExcludesCache, "Anthropic：input_tokens 不含 cache_read_input_tokens"},
		{"ANTHROPIC-MESSAGES", ConventionPromptExcludesCache, "大小写不敏感"},
		{"  anthropic-messages  ", ConventionPromptExcludesCache, "带空白"},
		{"openai-completions", PromptConventionUnset, "OpenAI：prompt_tokens 含 cached_tokens ⇒ 原有扣减正确"},
		{"openai-responses", PromptConventionUnset, "同上，即使它那 27 行里有 51.9% cache>prompt —— 样本不足以下结论，不猜"},
		{"", PromptConventionUnset, "空协议不猜"},
		{"   ", PromptConventionUnset, "纯空白不猜"},
		{"anthropic", PromptConventionUnset, "**前缀不算**：名字不完全等于 anthropic-messages 就不声明"},
		{"anthropic-completions", PromptConventionUnset, "**后缀不算**：这是另一个东西"},
		{"future-protocol-v9", PromptConventionUnset, "未来新增协议落回原有行为，而不是被猜成 Anthropic"},
	}
	for _, c := range cases {
		if got := CacheTokenConventionForProtocol(c.protocol); got != c.want {
			t.Errorf("CacheTokenConventionForProtocol(%q) = %v, want %v —— %s",
				c.protocol, got, c.want, c.why)
		}
	}
}

// TestConventionStringRoundTrips 钉住 String() 与枚举的对应，
// 避免日志里打不出「到底是哪一档」而无法排查。
func TestConventionStringRoundTrips(t *testing.T) {
	if s := ConventionPromptExcludesCache.String(); s != "prompt_excludes_cache" {
		t.Errorf("ConventionPromptExcludesCache.String() = %q", s)
	}
	if s := PromptConventionUnset.String(); s != "unset" {
		t.Errorf("PromptConventionUnset.String() = %q", s)
	}
}

// TestConventionsDifferOnlyWhenCacheIsSubsetOfPrompt 是本判据的承重用例。
//
// ★ 2026-10-06 修正：这条用例**原来的前提是假的**，已重写。
// 原用例断言「cache 远大于 prompt 时 OpenAI 口径会算出负数」——
// 实测**不会**，因为 calcCostWithConvention 里那道
//
//	subset := cacheReadCount <= promptCount
//
// 护栏已经在 cache > prompt 时**跳过扣减**了（那道护栏正是它自己为了防超扣
// 加的）。所以两套口径在 cache > prompt 时**结果相同**。
//
// 真正的分界在**反方向**：cache ⊆ prompt 时，OpenAI 口径要扣减原价、
// Anthropic 口径不扣 —— 这个差就是接线要修的东西。
func TestConventionsDifferOnlyWhenCacheIsSubsetOfPrompt(t *testing.T) {
	// ── 形态 A：cache ⊆ prompt（扣减生效）——两套口径**必须不同** ──
	prompt, completion, cacheRead := 1000.0, 200.0, 300.0
	priceIn, priceOut, cachePrice := 10.0, 25.0, 5.0
	base := CostInput{
		PromptTokens: &prompt, CompletionTokens: &completion, CacheReadTokens: &cacheRead,
		PriceIn: &priceIn, PriceOut: &priceOut, CacheReadPrice: &cachePrice,
	}
	openai := calcCostWithConvention(base, PromptConventionUnset)
	anthropic := calcCostWithConvention(base, ConventionPromptExcludesCache)
	if openai == nil || anthropic == nil {
		t.Fatalf("两套口径都不该返回 nil：openai=%v anthropic=%v", openai, anthropic)
	}
	// OpenAI：先按原价减 cache 再按缓存价入账 = (1000-300)*10 + 300*5 + 200*25
	wantOpenAI := (1000-300)*10.0 + 300*5.0 + 200*25.0
	// Anthropic：并列，不扣原价 = 1000*10 + 300*5 + 200*25
	wantAnthropic := 1000*10.0 + 300*5.0 + 200*25.0
	if diff := *openai - wantOpenAI/1e6; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("OpenAI 口径 = %f, want %f", *openai, wantOpenAI/1e6)
	}
	if diff := *anthropic - wantAnthropic/1e6; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("Anthropic 口径 = %f, want %f", *anthropic, wantAnthropic/1e6)
	}
	if *anthropic <= *openai {
		t.Fatalf("Anthropic(%f) 应大于 OpenAI(%f)：它不做原价扣减", *anthropic, *openai)
	}

	// ── 形态 B：cache > prompt（护栏跳过扣减）——两套口径**必须相同** ──
	bigPrompt, bigCompletion, bigCache := 177.0, 4765.0, 1162318.0
	baseB := CostInput{
		PromptTokens: &bigPrompt, CompletionTokens: &bigCompletion, CacheReadTokens: &bigCache,
		PriceIn: &priceIn, PriceOut: &priceOut, CacheReadPrice: &cachePrice,
	}
	oB := calcCostWithConvention(baseB, PromptConventionUnset)
	aB := calcCostWithConvention(baseB, ConventionPromptExcludesCache)
	if oB == nil || aB == nil {
		t.Fatalf("形态 B 两套口径都不该返回 nil：openai=%v anthropic=%v", oB, aB)
	}
	if *oB != *aB {
		t.Fatalf("cache > prompt 时两套口径应相同（护栏已跳过扣减）：openai=%f anthropic=%f", *oB, *aB)
	}
	if *oB <= 0 {
		t.Fatalf("形态 B 的成本 = %f, want > 0 —— 负数只可能来自**负的缓存价**，"+
			"而不是口径（那正是 831 那四个非负 CHECK 要拦的东西）", *oB)
	}
}

// TestSubsetGuardPreventsNegativeCost 单独钉住那道护栏。
//
// 真库 2026-10-06 读数：cost_usd < 0 的行共 4 组、全部满足
// `cache_read_tokens > prompt_tokens`（分组计数 649/593/15/2，cache_subset_of_prompt 全为 0），
// 缓存价本身是正的 5。而**现网二进制是 10-04 构建的，不含 subset 护栏** ——
// 那就是负成本的来源。工作区（含护栏）跑同一形态得到的是正数。
func TestSubsetGuardPreventsNegativeCost(t *testing.T) {
	// 真库凭据 31 的真实形态：prompt 3 / cache 23,463
	prompt, completion, cacheRead := 3.0, 20.0, 23463.0
	priceIn, priceOut, cachePrice := 15.0, 50.0, 5.0
	got := calcCostWithConvention(CostInput{
		PromptTokens: &prompt, CompletionTokens: &completion, CacheReadTokens: &cacheRead,
		PriceIn: &priceIn, PriceOut: &priceOut, CacheReadPrice: &cachePrice,
	}, PromptConventionUnset)
	if got == nil {
		t.Fatal("含 subset 护栏时这一形态必须算出成本（现网二进制没有护栏，算了负数）")
	}
	if *got <= 0 {
		t.Fatalf("成本 = %f, want > 0（护栏跳过了原价扣减）", *got)
	}
}
