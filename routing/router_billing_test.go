package routing

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestPlanCandidates_PlanBillingBeforePayAsYouGo(t *testing.T) {
	price := 1.0
	router := NewRouter(nil, limiter.New())
	policy := provider.DefaultPolicy()
	policy.TierFallbackMax = 3

	candidates := []provider.Candidate{
		{
			CredentialID:      1,
			ProviderID:        10,
			Tier:              2,
			Weight:            100,
			BillingMode:       "token",
			PriceInPer1M:      &price,
			Routable:          true,
			LifecycleStatus:   "active",
			AvailabilityState: "ready",
			QuotaState:        "ok",
			CircuitState:      "closed",
		},
		{
			CredentialID:      2,
			ProviderID:        20,
			Tier:              2,
			Weight:            50,
			BillingMode:       "token_plan",
			PriceInPer1M:      &price,
			Routable:          true,
			LifecycleStatus:   "active",
			AvailabilityState: "ready",
			QuotaState:        "ok",
			CircuitState:      "closed",
		},
		{
			CredentialID:      3,
			ProviderID:        30,
			Tier:              1,
			Weight:            200,
			BillingMode:       "free",
			Routable:          true,
			LifecycleStatus:   "active",
			AvailabilityState: "ready",
			QuotaState:        "ok",
			CircuitState:      "closed",
		},
	}

	ordered := router.PlanCandidates(candidates, nil, policy, nil)
	if len(ordered) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ordered))
	}
	if ordered[0].BillingMode != "free" {
		t.Fatalf("first candidate billing_mode = %q, want free", ordered[0].BillingMode)
	}
	if ordered[1].BillingMode != "token_plan" {
		t.Fatalf("second candidate billing_mode = %q, want token_plan", ordered[1].BillingMode)
	}
	if ordered[2].BillingMode != "token" {
		t.Fatalf("third candidate billing_mode = %q, want token (PAYG)", ordered[2].BillingMode)
	}
}

func TestCompareCandidatePriority_BillingRoundFirst(t *testing.T) {
	score := 5.0
	lowScore := 1.0
	a := provider.Candidate{BillingMode: "token_plan", CompositeScore: score, CredentialID: 1}
	b := provider.Candidate{BillingMode: "token", CompositeScore: lowScore, CredentialID: 2}
	if !CompareCandidatePriority(a, b) {
		t.Fatal("token_plan should sort before PAYG even with higher composite score")
	}
}
