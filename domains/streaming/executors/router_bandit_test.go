package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestBanditOrder_PressureFactorReadsLiveLoad (regression, 2026-09-10 audit
// R9 candidate 6):
//
// banditOrder's pressure factor used to read the Limiter's credential
// semaphore directly. dispatch_v2 deliberately bypasses that semaphore
// (AcquireAllNoCredLayer), so Used() is always 0 in production and the
// factor was pinned at 1.0 — wiring the Bandit (main.go) would have let a
// historically strong credential absorb traffic until saturation, i.e. the
// 245 m3 93/7 skew all over again.
//
// The fix routes the factor through calculateConcurrencyScore (the same
// LiveLoad signal source as P2C scoring). This test pins: a saturated
// credential with a dominant bandit score must lose to an idle one.
//
// Determinism: cred 21 gets 1000 successes → Sample ≈ 0.845 (reliability
// Beta(1001,1), speed≈0.98, intel 0.505); cred 42 is unproven → prior-only
// Sample ∈ (0.30, 0.71). Pre-fix both factors are 1.0, so 21 always wins;
// post-fix 21's factor is 1-19/20=0.05 (score ≈0.042), so 42 always wins.
func TestBanditOrder_PressureFactorReadsLiveLoad(t *testing.T) {
	limit := 20
	cBest := provider.Candidate{
		ProviderID: 14, CredentialID: 21, RawModel: "MiniMax-M3",
		ConcurrencyLimit: &limit,
	}
	cIdle := provider.Candidate{
		ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3",
		ConcurrencyLimit: &limit,
	}

	bandit := credential.NewBanditScorer()
	for i := 0; i < 1000; i++ {
		bandit.RecordSuccess("21", 10)
	}

	ll := &fakeLiveLoad{
		concurrent: map[credModelKey]int64{
			{CredID: 21, Model: "MiniMax-M3"}: 19, // 19/20 saturated
			{CredID: 42, Model: "MiniMax-M3"}: 0,  // idle
		},
	}
	r := &Router{Bandit: bandit, LiveLoad: ll}

	ordered := r.banditOrder(context.Background(), []provider.Candidate{cBest, cIdle})
	if ordered[0].CredentialID != 42 {
		t.Fatalf("expected idle credential 42 first (saturated 21 must be damped by LiveLoad pressure), got %d first", ordered[0].CredentialID)
	}
}

// TestBanditOrder_LegacyLimiterPathStillDampens guards the legacy dispatch
// path (LiveLoad nil): the Limiter semaphore remains a valid pressure source
// there (AcquireAll acquires it), so a saturated credential must still be
// damped. Passes identically before and after the calculateConcurrencyScore
// routing — it pins the fallback semantics of the shared scorer.
func TestBanditOrder_LegacyLimiterPathStillDampens(t *testing.T) {
	bandit := credential.NewBanditScorer()
	for i := 0; i < 1000; i++ {
		bandit.RecordSuccess("21", 10)
	}

	limiter := credential.NewWithLimits(10, 10, 2, 2)
	defer limiter.Stop()
	if !limiter.Credential(14, 21).TryAcquire() {
		t.Fatal("setup: token 1")
	}
	if !limiter.Credential(14, 21).TryAcquire() {
		t.Fatal("setup: token 2")
	}

	r := &Router{Bandit: bandit, Limiter: limiter} // LiveLoad nil → legacy path
	cBest := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "MiniMax-M3"}
	cIdle := provider.Candidate{ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3"}

	ordered := r.banditOrder(context.Background(), []provider.Candidate{cBest, cIdle})
	if ordered[0].CredentialID != 42 {
		t.Fatalf("expected idle credential 42 first (limiter-saturated 21 must be damped), got %d first", ordered[0].CredentialID)
	}
}
