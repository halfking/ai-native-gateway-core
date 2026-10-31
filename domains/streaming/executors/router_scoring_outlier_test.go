package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// router_scoring_outlier_test.go — P1-A (audit 2026-10-28 §4 G3): pool-level
// success-rate outlier penalty. A chronically sick node (success rate far
// below pool peers but never enough consecutive failures to trip the
// NodeState/breaker death faces) is invisible to every existing dimension —
// this pins the pool-stats computation, the outlier predicate with its
// depth/samples guards, and the byte-identical-off contract.

func f64p(v float64) *float64 { return &v }

func outlierCand(credID int, r float64, samples int) provider.Candidate {
	return provider.Candidate{CredentialID: credID, RecentSuccessRate: f64p(r), RecentSamples: samples}
}

func TestComputeSuccessRatePoolStats(t *testing.T) {
	// Three deep candidates with well-separated rates: mean 0.90,
	// deviations 0.05 / 0.00 / 0.05 ⇒ MAD = 1/30.
	pool := []provider.Candidate{
		outlierCand(1, 0.95, 100),
		outlierCand(2, 0.90, 80),
		outlierCand(3, 0.85, 50),
	}
	stats := ComputeSuccessRatePoolStats(pool)
	require.NotNil(t, stats)
	assert.InDelta(t, 0.90, stats.Mean, 1e-9)
	assert.InDelta(t, 0.10/3.0, stats.MAD, 1e-9)
	assert.Equal(t, 3, stats.Depth)

	// Shallow pool (2 qualified) ⇒ nil — a two-node pool has no statistics.
	shallow := []provider.Candidate{outlierCand(1, 0.95, 100), outlierCand(2, 0.50, 100)}
	assert.Nil(t, ComputeSuccessRatePoolStats(shallow), "depth<3 must disable the dimension")

	// Unqualified samples (nil rate / thin samples) don't count toward depth.
	thin := []provider.Candidate{
		outlierCand(1, 0.95, 100),
		outlierCand(2, 0.50, 29), // one below the 30-sample floor
		{CredentialID: 3},
		outlierCand(4, 0.90, 100),
	}
	assert.Nil(t, ComputeSuccessRatePoolStats(thin), "unqualified rows must not pad the pool depth")

	// Empty pool ⇒ nil.
	assert.Nil(t, ComputeSuccessRatePoolStats(nil))
}

func TestSuccessRateOutlierPenalty(t *testing.T) {
	// Pool: 0.95 / 0.90 / 0.85 (+ one thin row to prove guards) ⇒
	// threshold = 0.90 − 3×(0.10/3) = 0.80.
	pool := []provider.Candidate{
		outlierCand(1, 0.95, 100),
		outlierCand(2, 0.90, 80),
		outlierCand(3, 0.85, 50),
		outlierCand(4, 0.60, 10), // chronically sick BUT samples too thin
	}
	stats := ComputeSuccessRatePoolStats(pool)
	require.NotNil(t, stats)

	// Above threshold ⇒ 0.
	assert.Zero(t, successRateOutlierPenalty(outlierCand(1, 0.95, 100), stats))
	// Exactly at threshold (0.80 ≤ 0.80) ⇒ outlier.
	assert.InDelta(t, outlierPenaltyStep, successRateOutlierPenalty(outlierCand(5, 0.80, 40), stats), 1e-12)
	// Far below ⇒ outlier.
	assert.InDelta(t, outlierPenaltyStep, successRateOutlierPenalty(outlierCand(6, 0.60, 100), stats), 1e-12)
	// Thin samples ⇒ never an outlier (the thin sick row above).
	assert.Zero(t, successRateOutlierPenalty(pool[3], stats))
	// No rate signal ⇒ 0.
	assert.Zero(t, successRateOutlierPenalty(provider.Candidate{CredentialID: 7}, stats))
	// nil stats ⇒ dimension off.
	assert.Zero(t, successRateOutlierPenalty(outlierCand(6, 0.60, 100), nil))
	// Shallow pool stats ⇒ 0 even for a terrible rate.
	shallow := &SuccessRatePoolStats{Mean: 0.9, MAD: 0.01, Depth: 2}
	assert.Zero(t, successRateOutlierPenalty(outlierCand(6, 0.10, 100), shallow))
}

// TestOutlierDimensionOffIsByteIdentical pins the audit contract: weight 0
// (the default) ⇒ composite equals the pre-P1-A value exactly, and the
// strategy input being unwired (nil stats) with a nonzero weight also
// contributes 0 (fail-open).
func TestOutlierDimensionOffIsByteIdentical(t *testing.T) {
	pool := []provider.Candidate{
		outlierCand(1, 0.95, 100),
		outlierCand(2, 0.90, 80),
		outlierCand(3, 0.20, 100), // extreme outlier
	}
	stats := ComputeSuccessRatePoolStats(pool)
	require.NotNil(t, stats)

	weights := DefaultLoadScoreWeights()
	weights.OutlierWeight = 0
	c := pool[2]
	offScore := calculateLoadScore(c, nil, context.Background(), weights, StrategyInput{SuccessRatePoolStats: stats})

	// Recompute without the field at all — must match the weight-0 score
	// bit-for-bit (维度关闭 = 未接线).
	legacyScore := calculateLoadScore(c, nil, context.Background(), weights, StrategyInput{})
	assert.Equal(t, legacyScore, offScore, "OutlierWeight=0 must be byte-identical to unwired")
}

// TestOutlierPenaltyMovesComposite pins the on-path behavior: weight > 0 +
// wired stats ⇒ the outlier candidate's composite rises by exactly
// step×weight; clean candidates are untouched.
func TestOutlierPenaltyMovesComposite(t *testing.T) {
	pool := []provider.Candidate{
		outlierCand(1, 0.95, 100),
		outlierCand(2, 0.90, 80),
		outlierCand(3, 0.85, 50),
	}
	stats := ComputeSuccessRatePoolStats(pool)
	require.NotNil(t, stats)

	weights := DefaultLoadScoreWeights()
	const w = 0.1
	weights.OutlierWeight = w

	sick := outlierCand(9, 0.50, 100) // below 0.80 threshold
	base := calculateLoadScore(sick, nil, context.Background(), weights, StrategyInput{})
	with := calculateLoadScore(sick, nil, context.Background(), weights, StrategyInput{SuccessRatePoolStats: stats})
	assert.InDelta(t, base+outlierPenaltyStep*w, with, 1e-12)

	clean := outlierCand(1, 0.95, 100)
	baseClean := calculateLoadScore(clean, nil, context.Background(), weights, StrategyInput{})
	withClean := calculateLoadScore(clean, nil, context.Background(), weights, StrategyInput{SuccessRatePoolStats: stats})
	assert.Equal(t, baseClean, withClean, "clean candidates must be untouched")
}
