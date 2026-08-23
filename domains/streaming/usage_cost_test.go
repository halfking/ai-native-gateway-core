package streaming

import (
	"math"
	"testing"
)

func TestAssignRequestCostUSD(t *testing.T) {
	prompt := 1000
	completion := 500
	priceIn := 2.5
	priceOut := 15.0

	costUSD, costDisplay, costCurrency := AssignRequestCost(CostPriceInput{
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
		PriceIn:          &priceIn,
		PriceOut:         &priceOut,
		Currency:         "USD",
	})
	if costUSD == nil {
		t.Fatal("expected cost_usd for USD offer")
	}
	if costDisplay != nil || costCurrency != nil {
		t.Fatalf("USD path must not set display/currency: display=%v currency=%v", costDisplay, costCurrency)
	}
	want := 0.0025 + 0.0075 // 1000@2.5/M + 500@15/M
	if math.Abs(*costUSD-want) > 1e-9 {
		t.Fatalf("cost_usd=%v want %v", *costUSD, want)
	}
}

func TestAssignRequestCostCNYWithFX(t *testing.T) {
	prompt := 7200
	priceIn := 7.2
	priceOut := 0.0

	costUSD, costDisplay, costCurrency := AssignRequestCost(CostPriceInput{
		PromptTokens: &prompt,
		PriceIn:      &priceIn,
		PriceOut:     &priceOut,
		Currency:     "CNY",
	})
	if costUSD == nil || costDisplay == nil || costCurrency == nil {
		t.Fatalf("CNY path must populate all fields: usd=%v display=%v currency=%v", costUSD, costDisplay, costCurrency)
	}
	if *costCurrency != "CNY" {
		t.Fatalf("currency=%q want CNY", *costCurrency)
	}
	if math.Abs(*costDisplay-0.05184) > 1e-6 {
		t.Fatalf("display=%v want 0.05184", *costDisplay)
	}
	if math.Abs(*costUSD-0.0072) > 1e-6 {
		t.Fatalf("usd KPI=%v want 0.0072 (display/7.2)", *costUSD)
	}
}

func TestAssignRequestCostZeroPriceNil(t *testing.T) {
	prompt := 10
	zero := 0.0
	costUSD, costDisplay, costCurrency := AssignRequestCost(CostPriceInput{
		PromptTokens: &prompt,
		PriceIn:      &zero,
		PriceOut:     &zero,
		Currency:     "USD",
	})
	if costUSD != nil || costDisplay != nil || costCurrency != nil {
		t.Fatalf("zero prices must leave costs nil: usd=%v display=%v currency=%v", costUSD, costDisplay, costCurrency)
	}
}

func TestAssignRequestCostNoTokensNil(t *testing.T) {
	priceIn := 1.0
	costUSD, costDisplay, costCurrency := AssignRequestCost(CostPriceInput{
		PriceIn:  &priceIn,
		Currency: "USD",
	})
	if costUSD != nil || costDisplay != nil || costCurrency != nil {
		t.Fatal("missing tokens must not assign cost")
	}
}
