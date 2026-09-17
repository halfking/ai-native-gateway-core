package taskprofile

import "testing"

// suggest_test.go — table-driven coverage of the pure suggestion engine
// (design doc §5.2). Uses production knob values except where a test
// deliberately tightens them.

func withKnobs(t *testing.T, rate float64, minSamples int, bump float64) {
	oldRate, oldSamples, oldBump := correctionEscalationRate, correctionMinSamples, escalationMinConfidenceBump
	correctionEscalationRate, correctionMinSamples, escalationMinConfidenceBump = rate, minSamples, bump
	t.Cleanup(func() {
		correctionEscalationRate, correctionMinSamples, escalationMinConfidenceBump = oldRate, oldSamples, oldBump
	})
}

func TestSuggest_RegistryBaseline(t *testing.T) {
	cases := []struct {
		taskType string
		wantTier string
	}{
		{"documentation", TierC},
		{"coding", TierB},
		{"architecture", TierA},
	}
	for _, tc := range cases {
		got := Suggest(tc.taskType, 0.99, nil)
		if got.Tier != tc.wantTier || got.TierSource != "registry" {
			t.Errorf("Suggest(%s) = (%s, %s), want (%s, registry)",
				tc.taskType, got.Tier, got.TierSource, tc.wantTier)
		}
	}
}

func TestSuggest_UnknownTaskTypeSafeDefault(t *testing.T) {
	got := Suggest("mystery-type", 0.99, nil)
	if got.Tier != TierB || got.TierSource != "registry" {
		t.Fatalf("unknown type = (%s, %s), want (tier-b, registry)", got.Tier, got.TierSource)
	}
	if len(got.FallbackTiers) == 0 {
		t.Fatal("unknown type must carry a fallback chain")
	}
}

func TestSuggest_LowConfidenceEscalatesToTierA(t *testing.T) {
	got := Suggest("documentation", 0.5, nil) // documentation min_conf 0.85
	if got.Tier != TierA || got.TierSource != "confidence_escalation" {
		t.Fatalf("low confidence = (%s, %s), want (tier-a, confidence_escalation)",
			got.Tier, got.TierSource)
	}
}

func TestSuggest_CorrectionDensityEscalatesTier(t *testing.T) {
	stats := map[string]CorrectionStat{
		// 6 corrections, 4 corrected away → rate 0.667 ≥ 0.30, samples ≥ 5.
		"documentation": {TaskType: "documentation", Total: 6, Agrees: 2, Corrected: 4, CorrectionRate: 0.667},
		// 6 corrections, all confirmed → no escalation.
		"summary": {TaskType: "summary", Total: 6, Agrees: 6, CorrectionRate: 0},
		// Rate above threshold but below min samples → noise, no escalation.
		"coding": {TaskType: "coding", Total: 3, Agrees: 1, Corrected: 2, CorrectionRate: 0.667},
	}
	got := Suggest("documentation", 0.99, stats)
	if got.Tier != TierB || got.TierSource != "correction_escalation" {
		t.Fatalf("documentation = (%s, %s), want (tier-b, correction_escalation) — c→b bump",
			got.Tier, got.TierSource)
	}
	if got.MinConfidence <= 0.85 {
		t.Fatalf("escalation must tighten min_confidence above 0.85, got %.2f", got.MinConfidence)
	}
	if got.CorrectionStats == nil || got.CorrectionStats.Total != 6 {
		t.Fatalf("correction stats not surfaced: %+v", got.CorrectionStats)
	}

	if s := Suggest("summary", 0.99, stats); s.Tier != TierC || s.TierSource != "registry" {
		t.Fatalf("confirmed type must stay on registry tier: (%s, %s)", s.Tier, s.TierSource)
	}
	if s := Suggest("coding", 0.99, stats); s.Tier != TierB || s.TierSource != "registry" {
		t.Fatalf("below-min-samples type must stay on registry tier: (%s, %s)", s.Tier, s.TierSource)
	}
}

func TestSuggest_EscalationCeilsAtTierA(t *testing.T) {
	withKnobs(t, 0.30, 5, 0.05)
	stats := map[string]CorrectionStat{
		"architecture": {TaskType: "architecture", Total: 10, Agrees: 2, Corrected: 8, CorrectionRate: 0.8},
	}
	got := Suggest("architecture", 0.99, stats)
	if got.Tier != TierA {
		t.Fatalf("tier-a must stay tier-a under escalation, got %s", got.Tier)
	}
}

func TestSuggest_ZeroTotalStatIgnored(t *testing.T) {
	stats := map[string]CorrectionStat{
		"coding": {TaskType: "coding", Total: 0, CorrectionRate: 1},
	}
	if s := Suggest("coding", 0.99, stats); s.TierSource != "registry" || s.CorrectionStats != nil {
		t.Fatalf("zero-total stat must be ignored, got %+v", s)
	}
}

func TestSuggest_FallbacksCopiedNotAliased(t *testing.T) {
	got := Suggest("coding", 0.99, nil)
	got.FallbackTiers[0] = "mutated"
	again := Suggest("coding", 0.99, nil)
	if again.FallbackTiers[0] == "mutated" {
		t.Fatal("Suggest aliases the registry fallback slice — caller mutation leaks")
	}
}
