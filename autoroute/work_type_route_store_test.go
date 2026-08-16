package autoroute

import (
	"reflect"
	"testing"
	"time"
)

func TestWorkTypeRouteStore_TierFailoverModelsPreservesTierAndScoreOrder(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "primary-a", Tier: "primary"},
			{CanonicalName: "primary-b", Tier: "primary"},
			{CanonicalName: "secondary", Tier: "secondary"},
			{CanonicalName: "fallback", Tier: "fallback"},
			{CanonicalName: "primary-b", Tier: "secondary"},
		},
	}})

	got := store.TierFailoverModels([]ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "unconfigured"}, Breakdown: ScoringBreakdown{Composite: 100}},
		{Candidate: Candidate{CanonicalName: "fallback"}, Breakdown: ScoringBreakdown{Composite: 95}},
		{Candidate: Candidate{CanonicalName: "primary-b"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "secondary"}, Breakdown: ScoringBreakdown{Composite: 85}},
		{Candidate: Candidate{CanonicalName: "primary-a"}, Breakdown: ScoringBreakdown{Composite: 80}},
	}, "code")
	want := []string{"primary-b", "primary-a", "secondary", "fallback"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tier failover plan: got %v, want %v", got, want)
	}
}

func TestWorkTypeRouteStore_TierFailoverModelsStartsAtFirstEligibleTier(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "primary", Tier: "primary", MinScore: 95},
			{CanonicalName: "secondary", Tier: "secondary"},
			{CanonicalName: "fallback", Tier: "fallback"},
		},
	}})

	got := store.TierFailoverModels([]ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "primary"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "secondary"}, Breakdown: ScoringBreakdown{Composite: 70}},
		{Candidate: Candidate{CanonicalName: "fallback"}, Breakdown: ScoringBreakdown{Composite: 60}},
	}, "code")
	want := []string{"secondary", "fallback"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tier failover plan after min_score: got %v, want %v", got, want)
	}
}

func TestWorkTypeRouteStore_ApplyTierPolicyKeepsOnlyHighestAvailableTier(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "primary-a", Tier: "primary"},
			{CanonicalName: "primary-b", Tier: "primary", MinScore: 95},
			{CanonicalName: "secondary-a", Tier: "secondary"},
			{CanonicalName: "fallback-a", Tier: "fallback"},
		},
	}})

	got := store.ApplyTierPolicy([]ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "secondary-a"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "primary-b"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "primary-a"}, Breakdown: ScoringBreakdown{Composite: 70}},
		{Candidate: Candidate{CanonicalName: "fallback-a"}, Breakdown: ScoringBreakdown{Composite: 100}},
	}, "code")
	if len(got) != 1 || got[0].Candidate.CanonicalName != "primary-a" || got[0].Breakdown.RouteTier != "primary" {
		t.Fatalf("strict primary tier selection: got %+v", got)
	}
}

func TestWorkTypeRouteStore_ApplyTierPolicyFallsBackBelowMinScore(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "primary", Tier: "primary", MinScore: 95},
			{CanonicalName: "secondary", Tier: "secondary"},
		},
	}})
	got := store.ApplyTierPolicy([]ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "primary"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "secondary"}, Breakdown: ScoringBreakdown{Composite: 50}},
	}, "code")
	if len(got) != 1 || got[0].Candidate.CanonicalName != "secondary" || got[0].Breakdown.RouteTier != "secondary" {
		t.Fatalf("min_score should open secondary tier: got %+v", got)
	}
}

func TestWorkTypeRouteStore_ApplyTierPolicyEvaluatesRoutesIndependently(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{WorkTypeKey: "code_gen", CanonicalName: "shared", Tier: "primary", MinScore: 95},
			{WorkTypeKey: "code_review", CanonicalName: "shared", Tier: "secondary"},
		},
	}})
	got := store.ApplyTierPolicy([]ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "shared"},
		Breakdown: ScoringBreakdown{Composite: 80},
	}}, "code")
	if len(got) != 1 || got[0].Breakdown.RouteTier != "secondary" {
		t.Fatalf("each work-type route must keep its own min_score: got %+v", got)
	}
}

func TestWorkTypeRouteStore_ApplyTierPolicyWithWorkTypeIsolatesRoutes(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{
		byTaskType: map[string][]WorkTypeRoute{
			"code": {
				{WorkTypeKey: "code_gen", CanonicalName: "generator", Tier: "primary"},
				{WorkTypeKey: "code_review", CanonicalName: "reviewer", Tier: "primary"},
			},
		},
		byWorkTypeKey: map[string][]WorkTypeRoute{
			"code_gen":    {{WorkTypeKey: "code_gen", CanonicalName: "generator", Tier: "primary"}},
			"code_review": {{WorkTypeKey: "code_review", CanonicalName: "reviewer", Tier: "primary"}},
		},
		workTypeL1: map[string]string{"code_gen": "code", "code_review": "code"},
	})

	candidates := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "generator"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "reviewer"}, Breakdown: ScoringBreakdown{Composite: 90}},
	}
	got := store.ApplyTierPolicyWithWorkType(candidates, "code", "code_gen", nil)
	if len(got) != 1 || got[0].Candidate.CanonicalName != "generator" {
		t.Fatalf("exact work type must not consume sibling routes: got %+v", got)
	}
}

func TestWorkTypeRouteStore_ResolveWorkTypeRequiresSupportedEnabledMapping(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{workTypeL1: map[string]string{
		"code_gen": "code",
		"custom":   "custom_task",
	}})
	if got, ok := store.ResolveWorkType(" code_gen "); !ok || got != "code" {
		t.Fatalf("valid work type: got %q, %t", got, ok)
	}
	if _, ok := store.ResolveWorkType("custom"); ok {
		t.Fatal("unsupported L1 mapping must be rejected")
	}
}

func TestWorkTypeRouteStore_ApplyTierPolicyPreservesExplicitPin(t *testing.T) {
	store := NewWorkTypeRouteStore(nil)
	store.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "primary", Tier: "primary"},
			{CanonicalName: "pinned", Tier: "secondary"},
		},
	}})
	got := store.ApplyTierPolicyWithPins([]ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "primary"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "pinned"}, Breakdown: ScoringBreakdown{Composite: 10}},
	}, "code", []string{"pinned"})
	if len(got) != 2 {
		t.Fatalf("explicit pin must remain available: got %+v", got)
	}
}

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
