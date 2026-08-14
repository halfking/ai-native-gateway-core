package autoroute

import (
	"context"
	"strings"
	"testing"
)

// gateTestCandidates seeds a two-model pool where the ONLY differentiator is
// the embedded standard-IQ reference value: gpt-3.5-turbo scores 3.0 and
// claude-opus-4-8 scores 61.4 on the Artificial Analysis Intelligence Index
// (0-100 accuracy-style composite, NOT a human IQ scale).
func gateTestCandidates() []Candidate {
	return []Candidate{
		{CanonicalID: 1, CanonicalName: "gpt-3.5-turbo", RawModel: "gpt-3.5-turbo",
			TaskMatchScore: 0.9, SuccessRate: 0.95, P95LatencyMs: 1000,
			ProviderCategory: "official"},
		{CanonicalID: 2, CanonicalName: "claude-opus-4-8", RawModel: "claude-opus-4-8",
			TaskMatchScore: 0.9, SuccessRate: 0.95, P95LatencyMs: 1000,
			ProviderCategory: "official"},
	}
}

// TestRecommendV2WithHints_StandardIQGate_OffIsByteIdentical pins the RT-1
// completion gate: with the flag off (the default), the recommendation output
// must be exactly what the pre-RT-1 path produced — same winner, same order,
// same composite, no filter notes.
func TestRecommendV2WithHints_StandardIQGate_OffIsByteIdentical(t *testing.T) {
	prev := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(prev)

	idx := NewIndex()
	seedCandidatesForIndex(idx, gateTestCandidates())

	var notes []string
	got := idx.RecommendV2WithHints(context.Background(), TaskChat,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{FilterNotes: &notes})

	if len(got) != 2 {
		t.Fatalf("flag off: want both candidates, got %d", len(got))
	}
	// Both models are otherwise identical, so the pre-RT-1 order is whatever
	// the (unchanged) scoring path yields; pin only that the low-IQ model is
	// still present and no note was recorded.
	if got[0].Candidate.CanonicalName != "gpt-3.5-turbo" && got[1].Candidate.CanonicalName != "gpt-3.5-turbo" {
		t.Error("flag off: low-IQ model must still be routable")
	}
	if len(notes) != 0 {
		t.Errorf("flag off: filter notes must be empty, got %v", notes)
	}
	if got[0].Breakdown.Composite != got[1].Breakdown.Composite {
		t.Errorf("flag off: identical candidates must keep identical composites (%.4f vs %.4f)",
			got[0].Breakdown.Composite, got[1].Breakdown.Composite)
	}
}

// TestRecommendV2WithHints_StandardIQGate_OnExcludesBelowThreshold verifies
// the enabled gate: the low-IQ candidate is excluded, the exclusion reason
// lands in the decision metadata (DecisionHints.FilterNotes), and unknown
// models stay routable (fail-open).
func TestRecommendV2WithHints_StandardIQGate_OnExcludesBelowThreshold(t *testing.T) {
	prev := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
		UseStandardIQGate:        true,
		MinStandardIQ:            50,
	})
	defer SetGlobalFeatureFlagsForTest(prev)

	cands := append(gateTestCandidates(),
		Candidate{CanonicalID: 3, CanonicalName: "internal-unknown-model",
			TaskMatchScore: 0.9, SuccessRate: 0.95, P95LatencyMs: 1000,
			ProviderCategory: "official"})

	idx := NewIndex()
	seedCandidatesForIndex(idx, cands)

	var notes []string
	got := idx.RecommendV2WithHints(context.Background(), TaskChat,
		ClassificationSignals{}, ProfileSmart, "", 3, DecisionHints{FilterNotes: &notes})

	if len(got) != 2 {
		t.Fatalf("gate on: want 2 survivors (high-IQ + unknown), got %d: %+v", len(got), got)
	}
	for _, sc := range got {
		if sc.Candidate.CanonicalName == "gpt-3.5-turbo" {
			t.Error("gate on: low-IQ model must be excluded")
		}
	}
	if len(notes) != 1 {
		t.Fatalf("gate on: want exactly one filter note, got %v", notes)
	}
	if !strings.Contains(notes[0], "standard_iq_below_min") ||
		!strings.Contains(notes[0], "gpt-3.5-turbo") {
		t.Errorf("filter note should name the gate and the model, got %q", notes[0])
	}
}

// TestRecommendV2WithHints_StandardIQGate_TenantPolicyOverridesGlobal pins
// the RT-1 tenant strategy: a routing_policy weights_json min_standard_iq
// entry for the caller's tenant wins over the global flag — enabling the
// gate for that tenant even when the platform default is off, and an
// explicit 0 turning it off for that tenant while the platform default is on.
func TestRecommendV2WithHints_StandardIQGate_TenantPolicyOverridesGlobal(t *testing.T) {
	tests := []struct {
		name         string
		globalGateOn bool
		globalMin    float64
		tenantMin    float64 // NaN-key absent simulated by hasTenantPolicy
		hasPolicy    bool
		wantExcluded bool
	}{
		{
			name: "tenant entry enables gate while global is off",
			globalGateOn: false, globalMin: 0,
			hasPolicy: true, tenantMin: 50,
			wantExcluded: true,
		},
		{
			name: "tenant entry 0 disables gate while global is on",
			globalGateOn: true, globalMin: 50,
			hasPolicy: true, tenantMin: 0,
			wantExcluded: false,
		},
		{
			name: "no tenant entry falls back to global off",
			globalGateOn: false, globalMin: 50,
			hasPolicy: false,
			wantExcluded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := GetFeatureFlags()
			SetGlobalFeatureFlagsForTest(&FeatureFlags{
				UseChannelQualityRouting: true,
				UseStandardIQGate:        tt.globalGateOn,
				MinStandardIQ:            tt.globalMin,
			})
			defer SetGlobalFeatureFlagsForTest(prev)

			idx := NewIndex()
			if tt.hasPolicy {
				idx.SetTenantIQPolicyForTest(map[string]float64{"tenant-a": tt.tenantMin})
			}
			idx.SetAffinityStore(nil, func(apiKeyID int) string {
				if apiKeyID == 42 {
					return "tenant-a"
				}
				return ""
			})
			seedCandidatesForIndex(idx, gateTestCandidates())

			var notes []string
			got := idx.RecommendV2WithHints(context.Background(), TaskChat,
				ClassificationSignals{}, ProfileSmart, "", 3,
				DecisionHints{ApiKeyID: 42, FilterNotes: &notes})

			excluded := len(got) == 1 && got[0].Candidate.CanonicalName == "claude-opus-4-8"
			if excluded != tt.wantExcluded {
				t.Errorf("low-IQ excluded = %v, want %v (got %d candidates)", excluded, tt.wantExcluded, len(got))
			}
			if tt.wantExcluded && len(notes) != 1 {
				t.Errorf("want one filter note, got %v", notes)
			}
			if !tt.wantExcluded && len(notes) != 0 {
				t.Errorf("gate inactive: notes must be empty, got %v", notes)
			}
		})
	}
}
