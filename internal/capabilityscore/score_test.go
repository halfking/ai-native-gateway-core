package capabilityscore

import "testing"

func TestCalculateUsesVersionOneWeights(t *testing.T) {
	level := func(value int) *int { return &value }
	breakdown := Calculate(Inputs{
		IntelligenceLevel:  level(10),
		ContextLevel:       level(10),
		ModalityFitLevel:   level(10),
		ResponseSpeedLevel: level(10),
		PriceEfficiency:    level(10),
	})
	if breakdown.CapabilityScore != 100 {
		t.Fatalf("score = %v, want 100", breakdown.CapabilityScore)
	}
	if len(breakdown.UnknownDimensions) != 0 {
		t.Fatalf("unknown dimensions = %v", breakdown.UnknownDimensions)
	}
}

func TestCalculateMarksUnknownInputs(t *testing.T) {
	breakdown := Calculate(Inputs{})
	if len(breakdown.UnknownDimensions) != 5 {
		t.Fatalf("unknown dimensions = %v", breakdown.UnknownDimensions)
	}
	if breakdown.CapabilityScore != 0 {
		t.Fatalf("score = %v, want 0 for incomplete profile", breakdown.CapabilityScore)
	}
}

func TestSupportsAllNormalizesCapabilities(t *testing.T) {
	profile := Profile{ModalityCaps: []string{"Vision", " tool_use "}}
	if !SupportsAll(profile, []string{"vision", "tool_use"}) {
		t.Fatal("expected required capabilities to match")
	}
	if SupportsAll(profile, []string{"audio"}) {
		t.Fatal("unexpected audio support")
	}
}

func TestSimilarityDistanceRequiresComparableDimensions(t *testing.T) {
	if _, comparable := SimilarityDistance(Profile{}, Profile{}); comparable {
		t.Fatal("profiles without levels must not be comparable")
	}

	level := func(value int) *int { return &value }
	distance, comparable := SimilarityDistance(
		Profile{IntelligenceLevel: level(8), ContextLevel: level(8)},
		Profile{IntelligenceLevel: level(7), ContextLevel: level(8)},
	)
	if !comparable {
		t.Fatal("profiles with levels must be comparable")
	}
	if distance <= 0 || distance >= 10 {
		t.Fatalf("distance = %v, want a small non-zero distance", distance)
	}
}
