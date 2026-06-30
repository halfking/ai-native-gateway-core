package admin

import (
	"reflect"
	"testing"
)

// PR-7 (2026-06-30): buildStateDistribution must flatten a breakdown
// slice into {state: count}. Frontend ProbeHealthDetailView.vue reads
// `state_distribution` for the 4 status badges (audit P0-10).
func TestBuildStateDistribution_EmptyBreakdown(t *testing.T) {
	got := buildStateDistribution(nil)
	if got == nil {
		t.Fatal("should return non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestBuildStateDistribution_SingleState(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 5},
	}
	got := buildStateDistribution(breakdown)
	want := map[string]int{"healthy": 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildStateDistribution_MultipleStates(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 5},
		{State: "failing", Count: 2},
		{State: "suspicious", Count: 1},
		{State: "probing", Count: 3},
	}
	got := buildStateDistribution(breakdown)
	want := map[string]int{
		"healthy":    5,
		"failing":    2,
		"suspicious": 1,
		"probing":    3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// PR-7: real-world state enum (from db/migrations/308) is the union of
// healthy / healthy_confirmed / available / failing / broken_confirmed /
// unavailable / recovering / suspicious / probing. The function must
// preserve all of them verbatim (frontend aggregates them).
func TestBuildStateDistribution_AllStateEnums(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 1},
		{State: "healthy_confirmed", Count: 1},
		{State: "available", Count: 1},
		{State: "failing", Count: 1},
		{State: "broken_confirmed", Count: 1},
		{State: "unavailable", Count: 1},
		{State: "recovering", Count: 1},
		{State: "suspicious", Count: 1},
		{State: "probing", Count: 1},
	}
	got := buildStateDistribution(breakdown)
	if len(got) != 9 {
		t.Errorf("expected 9 distinct states, got %d: %v", len(got), got)
	}
	for _, state := range []string{
		"healthy", "healthy_confirmed", "available",
		"failing", "broken_confirmed", "unavailable", "recovering",
		"suspicious", "probing",
	} {
		if got[state] != 1 {
			t.Errorf("state %q = %d, want 1", state, got[state])
		}
	}
}

// PR-7: regression — multiple breakdown rows with same state should
// aggregate (SUM), not last-wins. Audit P0-10 reports show distinct
// (state, priority) groups can collapse into same state bucket.
func TestBuildStateDistribution_DuplicateStatesAggregated(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Priority: "watchdog", Count: 3},
		{State: "healthy", Priority: "watchdog", Count: 2},
		{State: "healthy", Priority: "suspicious", Count: 1}, // unusual but defensive
	}
	got := buildStateDistribution(breakdown)
	if got["healthy"] != 6 {
		t.Errorf("expected healthy=6 (3+2+1), got %d", got["healthy"])
	}
}
