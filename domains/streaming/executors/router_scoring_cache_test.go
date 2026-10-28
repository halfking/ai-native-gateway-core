package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

// router_scoring_cache_test.go — P1-B-③ (audit 2026-10-28 §4): prompt-cache
// affinity score dimension. Signal = Candidate.RecentCacheRate (migration 915
// recent_cache_hit_rate); penalty = 1 − hit rate; no signal ⇒ neutral 0
// (fail-open, same as rsr cold start); weight default 0 ⇒ composite
// byte-identical to pre-P1-B-③.

func cacheCand(rate *float64, samples int) provider.Candidate {
	return provider.Candidate{CredentialID: 1, RecentCacheRate: rate, RecentCacheSamples: samples}
}

func TestCalculateCacheAffinityPenalty(t *testing.T) {
	// Full hit ⇒ no penalty; zero hit ⇒ max penalty; linear in between.
	assert.InDelta(t, 0.0, calculateCacheAffinityPenalty(cacheCand(f64p(1.0), 50)), 1e-12)
	assert.InDelta(t, 1.0, calculateCacheAffinityPenalty(cacheCand(f64p(0.0), 50)), 1e-12)
	assert.InDelta(t, 0.3, calculateCacheAffinityPenalty(cacheCand(f64p(0.7), 50)), 1e-12)

	// No signal ⇒ neutral (the dimension must not punish data-less candidates).
	assert.Zero(t, calculateCacheAffinityPenalty(cacheCand(nil, 50)), "nil rate = no signal")
	assert.Zero(t, calculateCacheAffinityPenalty(cacheCand(f64p(0.8), 9)), "thin samples = no signal")
	assert.Zero(t, calculateCacheAffinityPenalty(provider.Candidate{CredentialID: 2}), "no fields at all")

	// Degenerate negative rate (provider quirk) reads as measured-zero.
	assert.InDelta(t, 1.0, calculateCacheAffinityPenalty(cacheCand(f64p(-0.2), 50)), 1e-12)
	// >1 is impossible by construction (SQL clamps per-row); treat as full hit.
	assert.Zero(t, calculateCacheAffinityPenalty(cacheCand(f64p(1.5), 50)))
}

// TestCacheAffinityDimensionOffIsByteIdentical pins the off contract:
// CacheAffinityWeight=0 (the default) ⇒ composite equals a weights struct
// with the field absent-equivalent, bit-for-bit.
func TestCacheAffinityDimensionOffIsByteIdentical(t *testing.T) {
	c := provider.Candidate{
		CredentialID:       1,
		BillingMode:        "per_token",
		Currency:           "USD",
		PriceInPer1M:       f64p(3),
		PriceOutPer1M:      f64p(9),
		RecentCacheRate:    f64p(0.9),
		RecentCacheSamples: 100,
	}
	weights := DefaultLoadScoreWeights()
	assert.Zero(t, weights.CacheAffinityWeight, "default must be 0 (observe first)")

	off := calculateLoadScore(c, nil, context.Background(), weights, StrategyInput{})
	// Simulate pre-P1-B-③ by evaluating a candidate with NO cache signal at
	// all — with weight 0 the two must be identical (the term contributes 0).
	noSignal := c
	noSignal.RecentCacheRate = nil
	noSignal.RecentCacheSamples = 0
	legacy := calculateLoadScore(noSignal, nil, context.Background(), weights, StrategyInput{})
	assert.Equal(t, legacy, off, "weight 0 ⇒ cache signal on/off must not move the composite")
}

// TestCacheAffinityPenaltyMovesComposite pins the on-path behavior: weight > 0
// ⇒ a high-hit candidate is preferred over an equal-otherwise zero-hit one,
// and the delta is exactly rate-gap × weight.
func TestCacheAffinityPenaltyMovesComposite(t *testing.T) {
	hit := cacheCand(f64p(0.9), 100)
	miss := cacheCand(f64p(0.0), 100)

	weights := DefaultLoadScoreWeights()
	const w = 0.2
	weights.CacheAffinityWeight = w

	sHit := calculateLoadScore(hit, nil, context.Background(), weights, StrategyInput{})
	sMiss := calculateLoadScore(miss, nil, context.Background(), weights, StrategyInput{})
	assert.InDelta(t, (1.0-0.0)*w-(1.0-0.9)*w, sMiss-sHit, 1e-9,
		"composite gap = (1−0)×w − (1−0.9)×w")
	assert.Less(t, sHit, sMiss, "higher cache hit must score lower (preferred)")
}
