package autoroute

// AF-15（2026-11-03 两规划合并审计 §1.3，W5-T1 验收门）：
//
//	「同一个没有价格」，autoroute 当它最便宜（blended<=0 → 100）、
//	executors 的 calculateCostPenalty 当它最大惩罚（nil 价 → 1.0）。
//	修法按 billing_mode 分档：round-1 套餐维持优待（两层原本就一致），
//	round-2 未知价给中性 —— 不再冒充最便宜。
//
// 本文件钉三件事：
//  1. 三代评分口径同断言：scorePrice（v1）与 scorePriceByCostContext
//     （v2 主链经 scoring_v2.go:55 与 simplified 共用）必须对同一候选
//     给出同一价格分；
//  2. round-1/round-2 分档：套餐未知价仍拿 100，按量未知价拿中性
//     （P75 有值 50 / 无池信号 80）；
//  3. 变异臂形态的回归断言：把 round-2 未知价改回 100 会红
//     （unknownPayg < 100），把 round-1 套餐改成中性也会红
//     （planUnknown == 100）。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestAF15_Round1UnknownPriceKeepsPreferential(t *testing.T) {
	ctx := CostContext{PriceP75: 500}
	for _, mode := range []string{"free", "token_plan", "code_plan", "agent_plan", "monthly"} {
		c := Candidate{BillingMode: mode}
		if got := scorePrice(c, ctx); got != 100 {
			t.Errorf("round-1 mode %q unknown price: got %.1f, want 100 (优待维持)", mode, got)
		}
		if got := scorePriceByCostContext(c, ctx); got != 100 {
			t.Errorf("round-1 mode %q unknown price (simplified): got %.1f, want 100", mode, got)
		}
		if r := provider.BillingRound(mode); r != 1 {
			t.Errorf("provider.BillingRound(%q) = %d, want 1", mode, r)
		}
	}
}

func TestAF15_Round2UnknownPriceIsNeutral(t *testing.T) {
	// 池有信号 ⇒ 中性 50（= P75 同价候选的分数，不是 100）
	signalCtx := CostContext{PriceP75: 500}
	for _, mode := range []string{"per_token", "token", "per_request", ""} {
		c := Candidate{BillingMode: mode}
		if got := scorePrice(c, signalCtx); got != 50 {
			t.Errorf("round-2 mode %q unknown price: got %.1f, want 50 (AF-15 中性)", mode, got)
		}
		if got := scorePriceByCostContext(c, signalCtx); got != 50 {
			t.Errorf("round-2 mode %q unknown price (simplified): got %.1f, want 50", mode, got)
		}
	}
	// 池无信号 ⇒ 沿用 no-baseline 中性 80
	noCtx := CostContext{}
	if got := scorePrice(Candidate{BillingMode: "per_token"}, noCtx); got != 80 {
		t.Errorf("round-2 unknown price with empty pool: got %.1f, want 80", got)
	}
	if got := scorePriceByCostContext(Candidate{BillingMode: "per_token"}, noCtx); got != 80 {
		t.Errorf("round-2 unknown price with empty pool (simplified): got %.1f, want 80", got)
	}
}

func TestAF15_GenerationsAgree(t *testing.T) {
	// v1 与 v2/simplified 的价格分必须逐位一致（同一候选、同一 CostContext）。
	cases := []Candidate{
		{BillingMode: "free"},
		{BillingMode: "token_plan"},
		{BillingMode: "per_token"},
		{BillingMode: ""},
		{BillingMode: "per_token", UnitPriceInPer1M: 3, UnitPriceOutPer1M: 9},
		{BillingMode: "per_token", UnitPriceInPer1M: 300, UnitPriceOutPer1M: 900},
	}
	ctxs := []CostContext{{}, {PriceP75: 500}, {PriceP75: 0.001}}
	for i, c := range cases {
		for j, ctx := range ctxs {
			v1 := scorePrice(c, ctx)
			v2 := scorePriceByCostContext(c, ctx)
			if v1 != v2 {
				t.Errorf("case %d ctx %d: v1=%.2f v2=%.2f (三代评分口径漂移)", i, j, v1, v2)
			}
		}
	}
}

func TestAF15_MutationArms(t *testing.T) {
	// 变异臂 1：若有人把 round-2 未知价改回 100（旧行为），这条红。
	ctx := CostContext{PriceP75: 500}
	unknownPayg := scorePriceByCostContext(Candidate{BillingMode: "per_token"}, ctx)
	if unknownPayg >= 100 {
		t.Errorf("MUTATION ARM: round-2 unknown price scored %.1f — the old `blended<=0 → 100` behavior is back", unknownPayg)
	}
	// 变异臂 2：若有人把 round-1 套餐也打成中性，这条红。
	planUnknown := scorePriceByCostContext(Candidate{BillingMode: "token_plan"}, ctx)
	if planUnknown != 100 {
		t.Errorf("MUTATION ARM: round-1 plan unknown price scored %.1f, want 100 — the round-1 preferential treatment was lost", planUnknown)
	}
	// 变异臂 3：若有人把「0/0 ⇒ 免费」臂加回 deriveIsFree，这条红。
	if deriveIsFree(Candidate{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0, BillingMode: "per_token"}) {
		t.Errorf("MUTATION ARM: deriveIsFree treats round-2 0/0 as free — the narrowed is_free semantics regressed")
	}
}
