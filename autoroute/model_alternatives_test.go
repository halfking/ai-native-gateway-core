package autoroute

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestRecommendModelAlternativesUsesRecommendV2AndExcludesTried(t *testing.T) {
	oldFlags := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(oldFlags)

	idx := &Index{
		entries: []Candidate{
			{CredentialID: 1, CanonicalID: 1, CanonicalName: "claude-sonnet-4-5", Tags: []string{"code"}, SuccessRate: 0.99},
			{CredentialID: 2, CanonicalID: 2, CanonicalName: "claude-opus-4-8", Tags: []string{"code"}, SuccessRate: 0.95},
		},
		lastRefresh: time.Now(),
	}
	decider := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	got, err := decider.RecommendModelAlternatives(context.Background(), ModelAlternativeRequest{
		Task:         TaskCode,
		Profile:      ProfileSmart,
		InitialModel: "claude-sonnet-4-5",
		TriedModels:  []string{"claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("RecommendModelAlternatives: %v", err)
	}
	if want := []string{"claude-opus-4-8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alternatives = %v, want %v", got, want)
	}
}

func TestRecommendModelAlternativesRejectsQualityDowngradeAndUnknownQuality(t *testing.T) {
	oldFlags := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(oldFlags)

	idx := &Index{entries: []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "claude-opus-4-8", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "gpt-3.5-turbo", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 3, CanonicalID: 3, CanonicalName: "unknown-premium-model", Tags: []string{"code"}, SuccessRate: 0.99},
	}, lastRefresh: time.Now()}
	decider := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	_, err := decider.RecommendModelAlternatives(context.Background(), ModelAlternativeRequest{
		Task: TaskCode, Profile: ProfileSmart, InitialModel: "claude-opus-4-8",
		TriedModels: []string{"claude-opus-4-8"},
	})
	if err != ErrNoCandidates {
		t.Fatalf("error = %v, want ErrNoCandidates", err)
	}
}

func TestRecommendModelAlternativesRequiresTaskCapability(t *testing.T) {
	oldFlags := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(oldFlags)

	idx := &Index{entries: []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "claude-sonnet-4-5", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "claude-opus-4-8", Tags: []string{"creative"}, SuccessRate: 0.99},
	}, lastRefresh: time.Now()}
	decider := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	_, err := decider.RecommendModelAlternatives(context.Background(), ModelAlternativeRequest{
		Task: TaskCode, Profile: ProfileSmart, InitialModel: "claude-sonnet-4-5",
		TriedModels: []string{"claude-sonnet-4-5"},
	})
	if err != ErrNoCandidates {
		t.Fatalf("error = %v, want ErrNoCandidates", err)
	}
}

func TestRecommendModelAlternativesPrefersTaskCandidateListOrder(t *testing.T) {
	oldFlags := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(oldFlags)

	idx := &Index{entries: []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "claude-sonnet-4-5", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "claude-opus-4-8", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 3, CanonicalID: 3, CanonicalName: "claude-opus-4-6", Tags: []string{"code"}, SuccessRate: 0.99},
	}, lastRefresh: time.Now()}
	decider := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	got, err := decider.RecommendModelAlternatives(context.Background(), ModelAlternativeRequest{
		Task:            TaskCode,
		Profile:         ProfileSmart,
		InitialModel:    "claude-sonnet-4-5",
		TriedModels:     []string{"claude-sonnet-4-5"},
		PreferredModels: []string{"claude-opus-4-6", "claude-opus-4-8"},
	})
	if err != nil {
		t.Fatalf("RecommendModelAlternatives: %v", err)
	}
	if want := []string{"claude-opus-4-6", "claude-opus-4-8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alternatives = %v, want task candidate order %v", got, want)
	}
}
