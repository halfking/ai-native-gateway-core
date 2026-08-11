package autoroute

import (
	"context"
	"testing"
	"time"
)

// End-to-end wiring test: with an AffinityStore in mode=on attached to the
// index, RecommendV2WithHints must actually fold affinity into the composite
// for a well-evidenced candidate, and leave the explore bucket untouched.
//
// This is the test that would have caught "the store is wired but never read"
// — the exact failure mode the first iteration of this feature shipped with.
func TestRecommendV2WithHints_AffinityEndToEnd(t *testing.T) {
	now := time.Now()
	store := newTestAffinityStore(AffinityOn, []AffinityRecord{{
		TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 7,
		CanonicalModel: "fav-model", Affinity: 88, SampleCount: 500,
		Confidence: 0.94, LastSampledAt: now,
	}})

	idx := NewIndex()
	idx.SetAffinityStore(store, nil)
	// Seed the candidate pool with two models: the affinity-favourite (id 7)
	// and a neutral one (id 8). Both otherwise identical so affinity is the
	// only differentiator.
	seedCandidatesForIndex(idx, []Candidate{
		{CanonicalID: 7, CanonicalName: "fav-model", TaskMatchScore: 0.5,
			SuccessRate: 0.95, P95LatencyMs: 1000, ProviderCategory: "official"},
		{CanonicalID: 8, CanonicalName: "other-model", TaskMatchScore: 0.5,
			SuccessRate: 0.95, P95LatencyMs: 1000, ProviderCategory: "official"},
	})

	// Non-explore request: affinity applies. The favourite must win and its
	// breakdown must carry the affinity. Use an id that hashes OUT of the
	// explore bucket (verified non-explore at ratio 0.10).
	got := idx.RecommendV2WithHints(context.Background(), TaskCode,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{RequestID: "req-2"})
	if len(got) == 0 {
		t.Fatal("no candidates returned")
	}
	if got[0].Candidate.CanonicalID != 7 {
		t.Errorf("affinity favourite should win, got %s", got[0].Candidate.CanonicalName)
	}
	if !got[0].Breakdown.AffinityApplied {
		t.Error("winner's AffinityApplied should be true when affinity is active")
	}
	if got[0].Breakdown.Affinity != 88 {
		t.Errorf("winner affinity = %.2f, want 88 (stored, within clamp)", got[0].Breakdown.Affinity)
	}

	// Explore request: affinity is computed but NOT applied, so the composite
	// must equal the 4-dimension score. Use a request id that hashes into the
	// explore bucket at ratio 0.10 — find one.
	var exploreID string
	for i := 0; i < 2000; i++ {
		id := "explore-req-" + itoa(i)
		if ShouldExplore(id, 0.10) {
			exploreID = id
			break
		}
	}
	if exploreID == "" {
		t.Skip("could not find an explore-bucket id")
	}
	exploreGot := idx.RecommendV2WithHints(context.Background(), TaskCode,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{RequestID: exploreID})

	// Recompute the 4-dimension baseline for the winner for direct comparison.
	var baseline ScoredCandidate
	for _, sc := range exploreGot {
		if sc.Candidate.CanonicalID == got[0].Candidate.CanonicalID {
			baseline = sc
			break
		}
	}
	if baseline.Candidate.CanonicalID == 0 {
		t.Fatal("explore winner not found in results")
	}
	if baseline.Breakdown.AffinityApplied {
		t.Error("explore request must not apply affinity")
	}
	// Affinity is still recorded (so the shadow/exploration data exists), but
	// the composite must be the un-scaled 4-dimension value.
	fourDim := ScoreWithChannelQuality(baseline.Candidate, TaskCode,
		recommendCostContext(idx.entries), 0)
	if diff := baseline.Breakdown.Composite - fourDim.Composite; absf(diff) > 1e-9 {
		t.Errorf("explore composite %.6f != 4-dim %.6f (diff %.6f)",
			baseline.Breakdown.Composite, fourDim.Composite, diff)
	}
}

// shadow mode: even the non-explore path must not change the winner relative to
// affinity-off, but affinity_score is recorded for observation.
func TestRecommendV2WithHints_ShadowModeDoesNotReorder(t *testing.T) {
	now := time.Now()
	// The favourite has a strong stored affinity but a WEAKER task match than
	// "other". In off and shadow modes affinity does not apply, so the stronger
	// match must win. Only in mode=on (tested separately) should affinity
	// overcome the match gap.
	recs := []AffinityRecord{{
		TaskType: TaskCode, CanonicalID: 7, CanonicalModel: "fav",
		Affinity: 90, SampleCount: 500, LastSampledAt: now,
	}}

	for _, mode := range []AffinityMode{AffinityOff, AffinityShadow} {
		store := newTestAffinityStore(mode, recs)
		idx := NewIndex()
		idx.SetAffinityStore(store, nil)
		// Two otherwise-identical candidates that differ only in TaskMatchScore,
		// so the match dimension alone decides the winner when affinity is inert.
		seedCandidatesForIndex(idx, []Candidate{
			{CanonicalID: 7, CanonicalName: "fav", Tags: []string{"code"},
				TaskMatchScore: 0.5, SuccessRate: 0.95, P95LatencyMs: 1000, ProviderCategory: "official"},
			{CanonicalID: 8, CanonicalName: "other", Tags: []string{"code", "programming"},
				TaskMatchScore: 1.0, SuccessRate: 0.95, P95LatencyMs: 1000, ProviderCategory: "official"},
		})
		got := idx.RecommendV2WithHints(context.Background(), TaskCode,
			ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{RequestID: "req-2"})
		if len(got) == 0 {
			t.Fatalf("mode=%s: no candidates returned", mode)
		}
		if got[0].Candidate.CanonicalID != 8 {
			t.Errorf("mode=%s: affinity reordered the winner; expected other (id 8) to win on stronger match",
				mode)
		}
	}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// seedCandidatesForIndex populates the index's candidate list without going
// through the DB refresh path, so scoring tests run hermetically.
func seedCandidatesForIndex(idx *Index, cs []Candidate) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries = cs
	byCanonical := make(map[int][]*Candidate, len(cs))
	for i := range cs {
		c := cs[i]
		byCanonical[c.CanonicalID] = append(byCanonical[c.CanonicalID], &cs[i])
		_ = c
	}
	idx.byCanonical = byCanonical
}
