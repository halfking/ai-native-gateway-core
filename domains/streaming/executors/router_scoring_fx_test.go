package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

// router_scoring_fx_test.go — P1-C (audit 2026-10-28 §4): CNY offers fold to
// USD before the cost-cap division. Previously a ¥60/1M CNY offer scored as
// 2× the $30/1M premium frontier (max penalty) while billing booked ~$8.3.

func costCand(currency string, in, out float64) provider.Candidate {
	return provider.Candidate{
		BillingMode:      "per_token",
		Currency:         currency,
		PriceInPer1M:     f64p(in),
		PriceOutPer1M:    f64p(out),
		StandardizedName: "gpt-4",
	}
}

func TestCalculateCostPenalty_CNYSameRulerAsBilling(t *testing.T) {
	// ¥216/1M blended ÷ 7.2 = $30/1M = exactly at the USD cap ⇒ the same
	// penalty as a $30 USD offer (previously 1.0 max penalty).
	cny := costCand("CNY", 66, 150) // blended 216
	usd := costCand("USD", 12, 18)  // blended 30

	assert.InDelta(t, 1.0, calculateCostPenalty(cny), 1e-9, "¥216/1M ≈ $30/1M must sit at the cap")
	assert.InDelta(t, 1.0, calculateCostPenalty(usd), 1e-9, "$30/1M sits at the cap")

	// Cheaper CNY offer: ¥72/1M blended ÷ 7.2 = $10 ⇒ 1/3 of cap.
	cheap := costCand("cny", 22, 50) // lowercase currency must match too
	assert.InDelta(t, 10.0/30.0, calculateCostPenalty(cheap), 1e-9)
}

func TestCalculateCostPenalty_USDUnchanged(t *testing.T) {
	// USD path is byte-identical to pre-P1-C.
	usd := costCand("USD", 15, 15) // blended 30
	assert.InDelta(t, 1.0, calculateCostPenalty(usd), 1e-9)

	half := costCand("", 7.5, 7.5) // empty currency = USD default
	assert.InDelta(t, 0.5, calculateCostPenalty(half), 1e-9)
}

func TestCalculateCostPenalty_UnknownStillMax(t *testing.T) {
	// RT-2 semantics preserved: missing prices = max penalty, not free.
	c := provider.Candidate{BillingMode: "per_token", Currency: "CNY"}
	assert.InDelta(t, 1.0, calculateCostPenalty(c), 1e-9)

	// Round-1 billing stays exempt regardless of currency.
	plan := provider.Candidate{BillingMode: "token_plan", Currency: "CNY", PriceInPer1M: f64p(100), PriceOutPer1M: f64p(100)}
	assert.Zero(t, calculateCostPenalty(plan))
}
