package autoroute

import (
	"testing"
)

// TestFallbackSnapshotAvailability_FiltersUnavailable verifies that
// fallbackSnapshotAvailability excludes candidates with non-empty
// UnavailableReason.
//
// Background (F10, 2026-11-17 auto 模式可用性轮):
//
// The snapshot (idx.entries) is already filtered by refreshIndexSQL's WHERE
// clause, which applies node_probe / lifecycle / manual_disabled gates. So
// the UnavailableReason field only reflects binding/provider_model layer
// unavailability.
//
// This test confirms that fallbackSnapshotAvailability correctly excludes
// candidates with non-empty UnavailableReason, which is the only filter we
// can apply on the snapshot (because it doesn't carry probe/lifecycle state).
func TestFallbackSnapshotAvailability_FiltersUnavailable(t *testing.T) {
	all := []Candidate{
		{
			CanonicalID:       1,
			CanonicalName:     "model-a",
			UnavailableReason: "", // available
		},
		{
			CanonicalID:       2,
			CanonicalName:     "model-b",
			UnavailableReason: "binding_unavailable", // unavailable
		},
		{
			CanonicalID:       3,
			CanonicalName:     "model-c",
			UnavailableReason: "", // available
		},
		{
			CanonicalID:       4,
			CanonicalName:     "model-d",
			UnavailableReason: "provider_model_unavailable", // unavailable
		},
	}

	filtered := fallbackSnapshotAvailability(all)

	// Should keep only candidates with empty UnavailableReason
	if len(filtered) != 2 {
		t.Errorf("expected 2 filtered candidates, got %d", len(filtered))
	}

	// Check that only model-a and model-c remain
	names := make(map[string]bool)
	for _, c := range filtered {
		names[c.CanonicalName] = true
	}

	if !names["model-a"] {
		t.Error("expected model-a to be included")
	}
	if !names["model-c"] {
		t.Error("expected model-c to be included")
	}
	if names["model-b"] {
		t.Error("expected model-b to be excluded (binding_unavailable)")
	}
	if names["model-d"] {
		t.Error("expected model-d to be excluded (provider_model_unavailable)")
	}
}

// TestFallbackSnapshotAvailability_EmptyInput verifies that an empty input
// returns an empty output without panic.
func TestFallbackSnapshotAvailability_EmptyInput(t *testing.T) {
	filtered := fallbackSnapshotAvailability(nil)
	if len(filtered) != 0 {
		t.Errorf("expected empty result for nil input, got %d candidates", len(filtered))
	}

	filtered = fallbackSnapshotAvailability([]Candidate{})
	if len(filtered) != 0 {
		t.Errorf("expected empty result for empty input, got %d candidates", len(filtered))
	}
}

// TestFallbackSnapshotAvailability_AllAvailable verifies that when all
// candidates have empty UnavailableReason, all are kept.
func TestFallbackSnapshotAvailability_AllAvailable(t *testing.T) {
	all := []Candidate{
		{CanonicalID: 1, CanonicalName: "model-a", UnavailableReason: ""},
		{CanonicalID: 2, CanonicalName: "model-b", UnavailableReason: ""},
		{CanonicalID: 3, CanonicalName: "model-c", UnavailableReason: ""},
	}

	filtered := fallbackSnapshotAvailability(all)

	if len(filtered) != 3 {
		t.Errorf("expected all 3 candidates to be kept, got %d", len(filtered))
	}
}

// TestFallbackSnapshotAvailability_AllUnavailable verifies that when all
// candidates have non-empty UnavailableReason, none are kept.
func TestFallbackSnapshotAvailability_AllUnavailable(t *testing.T) {
	all := []Candidate{
		{CanonicalID: 1, CanonicalName: "model-a", UnavailableReason: "binding_unavailable"},
		{CanonicalID: 2, CanonicalName: "model-b", UnavailableReason: "provider_disabled"},
		{CanonicalID: 3, CanonicalName: "model-c", UnavailableReason: "manual"},
	}

	filtered := fallbackSnapshotAvailability(all)

	if len(filtered) != 0 {
		t.Errorf("expected all candidates to be filtered out, got %d", len(filtered))
	}
}
