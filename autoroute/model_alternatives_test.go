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
			{CredentialID: 1, CanonicalID: 1, CanonicalName: "primary", Tags: []string{"code"}, SuccessRate: 0.99},
			{CredentialID: 2, CanonicalID: 2, CanonicalName: "replacement", Tags: []string{"code"}, SuccessRate: 0.95},
		},
		lastRefresh: time.Now(),
	}
	decider := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	got, err := decider.RecommendModelAlternatives(context.Background(), ModelAlternativeRequest{
		Task:        TaskCode,
		Profile:     ProfileSmart,
		TriedModels: []string{"primary"},
	})
	if err != nil {
		t.Fatalf("RecommendModelAlternatives: %v", err)
	}
	if want := []string{"replacement"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alternatives = %v, want %v", got, want)
	}
}
