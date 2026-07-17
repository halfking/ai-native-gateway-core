package autoroute

// embedding_route_test.go — tests for RecommendByModality (embeddings auto).

import (
	"testing"
)

func TestRecommendByModality_SelectsHighestComposite(t *testing.T) {
	idx := &Index{}
	idx.mu.Lock()
	idx.entries = []Candidate{
		// expensive but stable+fast
		{CanonicalName: "embed-premium", RawModel: "embed-premium", Modality: "embedding",
			SuccessRate: 0.99, P95LatencyMs: 80, UnitPriceInPer1M: 20, UnitPriceOutPer1M: 20},
		// mid
		{CanonicalName: "embed-mid", RawModel: "embed-mid", Modality: "embedding",
			SuccessRate: 0.95, P95LatencyMs: 200, UnitPriceInPer1M: 5, UnitPriceOutPer1M: 5},
		// cheap but slow + lower success
		{CanonicalName: "embed-cheap", RawModel: "embed-cheap", Modality: "embedding",
			SuccessRate: 0.85, P95LatencyMs: 500, UnitPriceInPer1M: 1, UnitPriceOutPer1M: 1},
		// non-embedding modality (must be filtered out)
		{CanonicalName: "chat-model", RawModel: "chat-model", Modality: "text",
			SuccessRate: 0.99, P95LatencyMs: 50},
		// unavailable (must be filtered out)
		{CanonicalName: "dead", RawModel: "dead", Modality: "embedding", UnavailableReason: "manual"},
	}
	idx.mu.Unlock()

	scored := idx.RecommendByModality("embedding", 3)
	if len(scored) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(scored))
	}
	// The 3-dim score weights stability(0.5)+speed(0.3)+price(0.2).
	// premium (0.99 success, 80ms, but $40 blended) vs mid (0.95, 200ms, $10):
	// premium wins on stability+speed but loses big on price. The winner is
	// whichever balances all three — we assert the ordering is deterministic
	// and that the winner has the highest composite, without hardcoding which
	// model wins (the formula is the contract, not a specific pick).
	best := scored[0].Breakdown.Composite
	for _, s := range scored[1:] {
		if s.Breakdown.Composite > best {
			t.Fatalf("not sorted: %s (%.1f) > winner (%.1f)",
				s.Candidate.CanonicalName, s.Breakdown.Composite, best)
		}
	}
	// Ensure chat-model and dead are not present
	for _, s := range scored {
		if s.Candidate.CanonicalName == "chat-model" || s.Candidate.CanonicalName == "dead" {
			t.Fatalf("filtered candidate leaked: %s", s.Candidate.CanonicalName)
		}
	}
}

func TestRecommendByModality_EmptyReturnsNil(t *testing.T) {
	idx := &Index{}
	idx.mu.Lock()
	idx.entries = []Candidate{
		{CanonicalName: "chat", Modality: "text"},
	}
	idx.mu.Unlock()
	if scored := idx.RecommendByModality("embedding", 3); scored != nil {
		t.Fatalf("want nil for no embedding candidates, got %d", len(scored))
	}
}

func TestRecommendByModality_FreeModelScoresHighOnPrice(t *testing.T) {
	idx := &Index{}
	idx.mu.Lock()
	idx.entries = []Candidate{
		// free model (price 0) should get price score 100
		{CanonicalName: "free-embed", RawModel: "free-embed", Modality: "embedding",
			SuccessRate: 0.95, P95LatencyMs: 100, UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0},
	}
	idx.mu.Unlock()
	scored := idx.RecommendByModality("embedding", 1)
	if len(scored) != 1 {
		t.Fatalf("want 1, got %d", len(scored))
	}
	if scored[0].Breakdown.PriceScore != 100 {
		t.Fatalf("free model price score: got %.1f, want 100", scored[0].Breakdown.PriceScore)
	}
}

func TestPercentileFloat_Empty(t *testing.T) {
	if v := percentileFloat(nil, 0.75); v != 0 {
		t.Fatalf("empty percentile: got %v, want 0", v)
	}
	if v := percentileFloat([]float64{1, 2, 3, 4}, 0.75); v != 3 {
		t.Fatalf("p75 of [1,2,3,4]: got %v, want 3", v)
	}
}

// findComposite returns the composite score for a named candidate.
func findComposite(scored []ScoredCandidate, name string) float64 {
	for _, s := range scored {
		if s.Candidate.CanonicalName == name {
			return s.Breakdown.Composite
		}
	}
	return -1
}
