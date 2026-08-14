package autoroute

import (
	"context"
	"math"
	"testing"
)

// popularityTestCandidates seeds two otherwise-identical models; the only
// differentiators are PopularityScore (from credential_model_bindings.
// routing_tier, derived in scanIndexRow) and featured status (from
// routing_policy.featured_models).
func popularityTestCandidates() []Candidate {
	return []Candidate{
		{CanonicalID: 1, CanonicalName: "plain-model", RawModel: "plain-model",
			TaskMatchScore: 0.9, SuccessRate: 0.95, P95LatencyMs: 1000,
			ProviderCategory: "official", PopularityScore: 10},
		{CanonicalID: 2, CanonicalName: "hot-featured-model", RawModel: "hot-featured-model",
			TaskMatchScore: 0.9, SuccessRate: 0.95, P95LatencyMs: 1000,
			ProviderCategory: "official", PopularityScore: 100},
	}
}

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestRecommendV2WithHints_PopularityWeight_OffIsByteIdentical pins the RT-3
// completion gate: with UsePopularityWeight off (the default), composites,
// order and breakdowns are exactly the pre-RT-3 values.
func TestRecommendV2WithHints_PopularityWeight_OffIsByteIdentical(t *testing.T) {
	prev := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(prev)

	idx := NewIndex()
	seedCandidatesForIndex(idx, popularityTestCandidates())
	idx.SetFeaturedModelsForTest([]string{"hot-featured-model"})

	got := idx.RecommendV2WithHints(context.Background(), TaskChat,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{})

	if len(got) != 2 {
		t.Fatalf("flag off: want both candidates, got %d", len(got))
	}
	// Identical inputs ⇒ identical composites; popularity must not leak in.
	if !floatEq(got[0].Breakdown.Composite, got[1].Breakdown.Composite) {
		t.Errorf("flag off: composites must stay identical (%.4f vs %.4f)",
			got[0].Breakdown.Composite, got[1].Breakdown.Composite)
	}
	for _, sc := range got {
		if sc.Breakdown.PopularityBoost != 0 {
			t.Errorf("flag off: PopularityBoost must be 0, got %.4f", sc.Breakdown.PopularityBoost)
		}
	}
}

// TestRecommendV2WithHints_PopularityWeight_OnBoostsHotAndFeatured verifies
// RT-3 wiring: popularity_score and featured_models feed an additive ordering
// weight on top of the composite, and the boost is recorded per candidate.
func TestRecommendV2WithHints_PopularityWeight_OnBoostsHotAndFeatured(t *testing.T) {
	prev := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
		UsePopularityWeight:      true,
		PopularityWeight:         5,
		FeaturedBonus:            5,
	})
	defer SetGlobalFeatureFlagsForTest(prev)

	idx := NewIndex()
	seedCandidatesForIndex(idx, popularityTestCandidates())
	idx.SetFeaturedModelsForTest([]string{"hot-featured-model"})

	got := idx.RecommendV2WithHints(context.Background(), TaskChat,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{})
	if len(got) != 2 {
		t.Fatalf("flag on: want both candidates, got %d", len(got))
	}

	byName := map[string]ScoredCandidate{}
	for _, sc := range got {
		byName[sc.Candidate.CanonicalName] = sc
	}
	hot := byName["hot-featured-model"]
	plain := byName["plain-model"]

	// hot: 5 * (100/100) popularity + 5 featured = 10; plain: 5 * (10/100) = 0.5.
	if !floatEq(hot.Breakdown.PopularityBoost, 10) {
		t.Errorf("hot featured boost = %.4f, want 10", hot.Breakdown.PopularityBoost)
	}
	if !floatEq(plain.Breakdown.PopularityBoost, 0.5) {
		t.Errorf("plain boost = %.4f, want 0.5", plain.Breakdown.PopularityBoost)
	}
	// The boosted composite must beat the identical-baseline plain candidate.
	if hot.Breakdown.Composite <= plain.Breakdown.Composite {
		t.Errorf("hot+featured composite (%.4f) must beat plain (%.4f)",
			hot.Breakdown.Composite, plain.Breakdown.Composite)
	}
	if got[0].Candidate.CanonicalName != "hot-featured-model" {
		t.Errorf("winner = %s, want hot-featured-model", got[0].Candidate.CanonicalName)
	}
}
