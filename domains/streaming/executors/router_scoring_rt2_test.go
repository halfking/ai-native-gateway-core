package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/modeliqdata"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// rt2Candidates seeds otherwise-identical credentials whose only
// differentiators are the RT-2 dimensions: blended unit price
// (PriceInPer1M + PriceOutPer1M, the shadow cost-optimized strategy input)
// and the model's standard IQ (AA Intelligence Index via modeliqdata).
func rt2Candidates() []provider.Candidate {
	cheapIn, cheapOut := 1.0, 2.0     // blended 3
	expensiveIn, expensiveOut := 10.0, 20.0 // blended 30
	return []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "claude-opus-4-8",
			P95LatencyMs: 500, SuccessRate: 0.95,
			PriceInPer1M: &cheapIn, PriceOutPer1M: &cheapOut},
		{CredentialID: 2, ProviderID: 1, RawModel: "gpt-3.5-turbo",
			P95LatencyMs: 500, SuccessRate: 0.95,
			PriceInPer1M: &expensiveIn, PriceOutPer1M: &expensiveOut},
		{CredentialID: 3, ProviderID: 1, RawModel: "totally-unknown-model-x",
			P95LatencyMs: 500, SuccessRate: 0.95,
			PriceInPer1M: nil, PriceOutPer1M: nil},
	}
}

// TestCalculateLoadScore_CostIQWeights_OffIsByteIdentical pins the RT-2
// completion gate: with CostWeight/IQWeight off (the default), the effective
// P2C load score is byte-identical to the pre-RT-2 composite — cost and IQ
// inputs must not leak into the score (doc 19 §3 ROUTE 权重变更门禁).
func TestCalculateLoadScore_CostIQWeights_OffIsByteIdentical(t *testing.T) {
	r := NewRouter(nil, nil)
	strat := NewP2CStrategy(r)
	in := StrategyInput{LoadScoreWeights: r.LoadScoreWeights}
	ctx := context.Background()

	cases := []struct {
		name string
		c    provider.Candidate
	}{
		{"cheap known price + known high IQ model", rt2Candidates()[0]},
		{"expensive known price + known low IQ model", rt2Candidates()[1]},
		{"unknown price + unknown IQ model", rt2Candidates()[2]},
		{"no dimensions at all", provider.Candidate{CredentialID: 9, ProviderID: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := strat.Score(ctx, tc.c, in)
			if err != nil {
				t.Fatalf("Score: %v", err)
			}
			// Legacy composite (pre-RT-2), derived from the same component
			// functions so the pin follows the shipped formula, not a copy.
			want := calculateConcurrencyScore(tc.c, r, ctx)*r.LoadScoreWeights.ConcurrencyWeight +
				calculateIdentityScore(tc.c, r)*r.LoadScoreWeights.IdentityWeight +
				(1.0-calculateLatencyScore(tc.c, r))*r.LoadScoreWeights.LatencyWeight +
				calculateQualityScore(tc.c)*r.LoadScoreWeights.QualityWeight +
				(1.0-calculateHeadroom(tc.c, r))*envFloat("LLM_GATEWAY_ROUTING_W_HEADROOM", 0.05) +
				capacityPenaltyForWeight(tc.c.Weight)*envFloat("LLM_GATEWAY_ROUTING_W_CAPACITY", 0.02)
			if got != want {
				t.Errorf("flag off: score = %.17g, want legacy composite %.17g", got, want)
			}
		})
	}

	// Identical candidates differing only in cost/IQ inputs must stay exactly
	// equal when the weights are off (no leakage in either direction).
	base := rt2Candidates()[0]
	for _, other := range rt2Candidates()[1:] {
		s0, _ := strat.Score(ctx, base, in)
		s1, _ := strat.Score(ctx, other, in)
		if s0 != s1 {
			t.Errorf("flag off: cost/IQ-only difference changed score (%.17g vs %.17g)", s0, s1)
		}
	}
}

// TestCalculateLoadScore_CostWeightOn_PrefersCheap verifies RT-2: when
// CostWeight > 0 the shadow cost dimension (blended in+out price per 1M,
// unknown price is NOT free) enters the effective P2C score as a penalty
// term, scaled by the weight.
func TestCalculateLoadScore_CostWeightOn_PrefersCheap(t *testing.T) {
	// Pin the soft cap so the table stays deterministic regardless of default
	// changes: blended price >= cap saturates at penalty 1.0.
	t.Setenv("LLM_GATEWAY_ROUTING_COST_CAP", "30")

	r := NewRouter(nil, nil)
	strat := NewP2CStrategy(r)
	base := DefaultLoadScoreWeights()
	base.CostWeight = 0.1 // RT-2 knob on; everything else identical
	in := StrategyInput{LoadScoreWeights: base}
	ctx := context.Background()

	cands := rt2Candidates()
	type result struct {
		name  string
		score float64
	}
	results := make([]result, len(cands))
	for i, c := range cands {
		sc, err := strat.Score(ctx, c, in)
		if err != nil {
			t.Fatalf("%s: %v", c.RawModel, err)
		}
		results[i] = result{c.RawModel, sc}
	}

	// cheap (blended 3 → penalty 0.1) < expensive (blended 30 → 1.0),
	// unknown price (max penalty 1.0) must not beat the cheap candidate.
	if !(results[0].score < results[1].score) {
		t.Errorf("cost weight on: cheap (%.4f) must beat expensive (%.4f)",
			results[0].score, results[1].score)
	}
	if results[2].score <= results[0].score {
		t.Errorf("cost weight on: unknown price (%.4f) must not beat cheap (%.4f) — unknown is not free",
			results[2].score, results[0].score)
	}
	// Quantitative pin: the deltas are CostWeight × cost penalty
	// (0.1 vs 1.0), so the expensive-vs-cheap gap pins both the weight
	// scaling and the normalization cap. InDelta because the gap passes
	// through the composite float summation (not bit-exact).
	wantGap := base.CostWeight * (1.0 - 3.0/30.0)
	gotGap := results[1].score - results[0].score
	if diff := gotGap - wantGap; diff < -1e-12 || diff > 1e-12 {
		t.Errorf("cost gap = %.17g, want %.17g (CostWeight × (1 - 3/30))", gotGap, wantGap)
	}
}

// TestCalculateLoadScore_IQWeightOn_PrefersHighIQ verifies RT-2: when
// IQWeight > 0 the model's standard IQ (AA Intelligence Index, the RT-1
// reference table) enters the effective P2C score — higher IQ → lower
// penalty. Unknown IQ is NEUTRAL (0.5 penalty, fail-open), so it must not
// outrank the high-IQ model nor be outranked-by-more-than-neutral.
func TestCalculateLoadScore_IQWeightOn_PrefersHighIQ(t *testing.T) {
	r := NewRouter(nil, nil)
	strat := NewP2CStrategy(r)
	base := DefaultLoadScoreWeights()
	base.IQWeight = 0.1 // RT-2 knob on; everything else identical
	in := StrategyInput{LoadScoreWeights: base}
	ctx := context.Background()

	cands := rt2Candidates()
	scores := make([]float64, len(cands))
	for i, c := range cands {
		sc, err := strat.Score(ctx, c, in)
		if err != nil {
			t.Fatalf("%s: %v", c.RawModel, err)
		}
		scores[i] = sc
	}

	// cands[0] = claude-opus-4-8 (high IQ), cands[1] = gpt-3.5-turbo (low IQ),
	// cands[2] = totally-unknown-model-x (neutral).
	if !(scores[0] < scores[1]) {
		t.Errorf("IQ weight on: high-IQ (%.4f) must beat low-IQ (%.4f)", scores[0], scores[1])
	}
	if !(scores[2] > scores[0]) {
		t.Errorf("IQ weight on: unknown IQ (%.4f) must not beat high-IQ (%.4f)", scores[2], scores[0])
	}
	// Quantitative pin: unknown penalty = 0.5 → the unknown-vs-high gap is
	// IQWeight × (0.5 - highIQPenalty); the low-vs-high gap pins
	// IQWeight × (lowIQPenalty - highIQPenalty) against the reference table.
	highIQ, highFound, _ := modeliqdata.LookupStandardIQ("claude-opus-4-8")
	lowIQ, lowFound, _ := modeliqdata.LookupStandardIQ("gpt-3.5-turbo")
	if !highFound || !lowFound {
		t.Fatalf("reference table sanity failed: high=%v low=%v", highFound, lowFound)
	}
	wantGapUnknown := base.IQWeight * (0.5 - (1.0 - highIQ/100.0))
	if diff := (scores[2] - scores[0]) - wantGapUnknown; diff < -1e-12 || diff > 1e-12 {
		t.Errorf("unknown-vs-high gap = %.17g, want %.17g", scores[2]-scores[0], wantGapUnknown)
	}
	wantGapLow := base.IQWeight * ((1.0 - lowIQ/100.0) - (1.0 - highIQ/100.0))
	if diff := (scores[1] - scores[0]) - wantGapLow; diff < -1e-12 || diff > 1e-12 {
		t.Errorf("low-vs-high gap = %.17g, want %.17g", scores[1]-scores[0], wantGapLow)
	}
}

// TestDefaultLoadScoreWeights_CostIQDefaultOffAndEnvWiring pins the RT-2
// deployment contract: the new weights default to zero (current behavior,
// effective score unchanged), and are wired via the router-scoring env
// convention (LLM_GATEWAY_ROUTING_W_*, like W_HEADROOM/W_CAPACITY).
func TestDefaultLoadScoreWeights_CostIQDefaultOffAndEnvWiring(t *testing.T) {
	// Default: both new weights off — NewRouter picks them up unchanged.
	w := DefaultLoadScoreWeights()
	if w.CostWeight != 0 {
		t.Errorf("default CostWeight = %v, want 0 (RT-2 off by default)", w.CostWeight)
	}
	if w.IQWeight != 0 {
		t.Errorf("default IQWeight = %v, want 0 (RT-2 off by default)", w.IQWeight)
	}
	r := NewRouter(nil, nil)
	if r.LoadScoreWeights.CostWeight != 0 || r.LoadScoreWeights.IQWeight != 0 {
		t.Errorf("NewRouter must default RT-2 weights to 0, got cost=%v iq=%v",
			r.LoadScoreWeights.CostWeight, r.LoadScoreWeights.IQWeight)
	}

	// Env wiring: the knobs follow the LLM_GATEWAY_ROUTING_W_* convention.
	t.Setenv("LLM_GATEWAY_ROUTING_W_COST", "0.15")
	t.Setenv("LLM_GATEWAY_ROUTING_W_IQ", "0.1")
	w = DefaultLoadScoreWeights()
	if w.CostWeight != 0.15 {
		t.Errorf("env CostWeight = %v, want 0.15", w.CostWeight)
	}
	if w.IQWeight != 0.1 {
		t.Errorf("env IQWeight = %v, want 0.1", w.IQWeight)
	}
	// Invalid values fall back to 0 (off), never panic.
	t.Setenv("LLM_GATEWAY_ROUTING_W_COST", "not-a-number")
	t.Setenv("LLM_GATEWAY_ROUTING_W_IQ", "")
	w = DefaultLoadScoreWeights()
	if w.CostWeight != 0 || w.IQWeight != 0 {
		t.Errorf("invalid env must fall back to 0, got cost=%v iq=%v", w.CostWeight, w.IQWeight)
	}
}
