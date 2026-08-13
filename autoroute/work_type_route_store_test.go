package autoroute

import (
	"testing"
	"time"
)

func TestWorkTypeRouteStore_ApplyBoostGroupsRoutesByL1TaskType(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{
		byTaskType: map[string][]WorkTypeRoute{
			"code": {
				{WorkTypeKey: "code_gen", L1TaskType: "code", CanonicalName: "preferred-model", Tier: "primary", Weight: 1},
				{WorkTypeKey: "code_review", L1TaskType: "code", CanonicalName: "secondary-model", Tier: "secondary", Weight: 1},
			},
		},
		LoadedAt: time.Now(),
	})

	candidates := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "natural-winner"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "preferred-model"}, Breakdown: ScoringBreakdown{Composite: 75}},
		{Candidate: Candidate{CanonicalName: "secondary-model"}, Breakdown: ScoringBreakdown{Composite: 74}},
	}

	got := store.ApplyBoost(candidates, "code")
	if got[0].Candidate.CanonicalName != "preferred-model" {
		t.Fatalf("primary route should win after boost, got %q", got[0].Candidate.CanonicalName)
	}
	if got[0].Breakdown.Composite != 97.5 {
		t.Fatalf("primary composite: got %v, want 97.5", got[0].Breakdown.Composite)
	}
	if got[1].Candidate.CanonicalName != "natural-winner" {
		t.Fatalf("natural winner should remain second, got %q", got[1].Candidate.CanonicalName)
	}
	if got[2].Breakdown.Composite != 85.1 {
		t.Fatalf("secondary composite: got %v, want 85.1", got[2].Breakdown.Composite)
	}
}

func TestWorkTypeRouteStore_ApplyBoostDoesNotMatchWorkTypeKeyDirectly(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{
		byTaskType: map[string][]WorkTypeRoute{
			"code": {{WorkTypeKey: "code_gen", L1TaskType: "code", CanonicalName: "preferred-model", Tier: "primary"}},
		},
	})

	candidates := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "natural-winner"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "preferred-model"}, Breakdown: ScoringBreakdown{Composite: 75}},
	}
	got := store.ApplyBoost(candidates, "code_gen")
	if got[0].Candidate.CanonicalName != "natural-winner" {
		t.Fatalf("store must accept L1 task type rather than work type key, got %q", got[0].Candidate.CanonicalName)
	}
}

func TestWorkTypeRouteStore_ApplyBoostFallbackDoesNotChangeOrder(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{
		byTaskType: map[string][]WorkTypeRoute{
			"chat": {{CanonicalName: "fallback-model", Tier: "fallback"}},
		},
	})
	candidates := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "natural-winner"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "fallback-model"}, Breakdown: ScoringBreakdown{Composite: 80}},
	}
	got := store.ApplyBoost(candidates, "chat")
	if got[0].Candidate.CanonicalName != "natural-winner" || got[1].Breakdown.Composite != 80 {
		t.Fatalf("fallback must not change candidate ordering: %+v", got)
	}
}

func TestWorkTypeRouteStore_ApplyBoostCanonicalNameNormalization(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {{CanonicalName: " Preferred-Model ", Tier: "primary", Weight: 1}},
	}})

	got := store.ApplyBoost([]ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "preferred-model"},
		Breakdown: ScoringBreakdown{Composite: 100},
	}}, "code")
	if !got[0].Breakdown.RouteBoostApplied || got[0].Breakdown.Composite != 130 {
		t.Fatalf("normalized canonical route was not applied: %+v", got[0].Breakdown)
	}
}

func TestWorkTypeRouteStore_ApplyBoostHonorsMinScore(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {{CanonicalName: "preferred-model", Tier: "primary", MinScore: 90}},
	}})

	below := store.ApplyBoost([]ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "preferred-model"},
		Breakdown: ScoringBreakdown{Composite: 89},
	}}, "code")
	if below[0].Breakdown.RouteBoostApplied || below[0].Breakdown.Composite != 89 {
		t.Fatalf("route below min_score should not be boosted: %+v", below[0].Breakdown)
	}

	at := store.ApplyBoost([]ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "preferred-model"},
		Breakdown: ScoringBreakdown{Composite: 90},
	}}, "code")
	if !at[0].Breakdown.RouteBoostApplied || at[0].Breakdown.Composite != 117 {
		t.Fatalf("route at min_score should be boosted: %+v", at[0].Breakdown)
	}
}

func TestWorkTypeRouteStore_ApplyBoostIsIdempotent(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {{CanonicalName: "preferred-model", Tier: "primary", Weight: 1}},
	}})
	candidates := []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "preferred-model"},
		Breakdown: ScoringBreakdown{Composite: 100},
	}}
	store.ApplyBoost(candidates, "code")
	store.ApplyBoost(candidates, "code")
	if candidates[0].Breakdown.Composite != 130 {
		t.Fatalf("route boost must be idempotent, got %v", candidates[0].Breakdown.Composite)
	}
}

func TestWorkTypeRouteStore_VersionStartsAtZero(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	if got := store.Version(); got != 0 {
		t.Fatalf("new store version: got %d, want 0", got)
	}
}

func TestWorkTypeRouteStore_LoadedAt(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	if !store.LoadedAt().IsZero() {
		t.Fatal("new store must have zero LoadedAt")
	}
	now := time.Now().UTC().Round(0)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{}, LoadedAt: now})
	if got := store.LoadedAt(); !got.Equal(now) {
		t.Fatalf("LoadedAt: got %v, want %v", got, now)
	}
}
