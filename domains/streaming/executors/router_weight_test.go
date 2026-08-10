package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestPlanCandidatesHonorsConfiguredWeights(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Weight: 90, Routable: true, SuccessRate: 0.9},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Weight: 10, Routable: true, SuccessRate: 0.9},
	}

	counts := map[int]int{}
	maxLightRun := 0
	lightRun := 0
	for i := 0; i < 100; i++ {
		planned := router.PlanCandidates(candidates, nil, policy, nil)
		if len(planned) != 2 {
			t.Fatalf("planned candidates = %d, want 2", len(planned))
		}
		counts[planned[0].CredentialID]++
		if planned[0].CredentialID == 2 {
			lightRun++
			if lightRun > maxLightRun {
				maxLightRun = lightRun
			}
		} else {
			lightRun = 0
		}
	}

	if counts[1] != 90 || counts[2] != 10 {
		t.Fatalf("weighted first-choice distribution = %v, want map[1:90 2:10]", counts)
	}
	if maxLightRun > 2 {
		t.Fatalf("light candidate was selected %d consecutive times, want at most 2", maxLightRun)
	}
}

func TestPlanCandidatesIsolatesWeightCountersByCandidateSet(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	modelX := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Weight: 1, Routable: true},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Weight: 1, Routable: true},
	}
	modelY := []provider.Candidate{
		{CredentialID: 3, ProviderID: 3, RawModel: "model-y", Tier: 1, Weight: 1, Routable: true},
		{CredentialID: 4, ProviderID: 4, RawModel: "model-y", Tier: 1, Weight: 1, Routable: true},
	}

	firstX := router.PlanCandidates(modelX, nil, policy, nil)
	firstY := router.PlanCandidates(modelY, nil, policy, nil)
	secondX := router.PlanCandidates(modelX, nil, policy, nil)
	if firstX[0].CredentialID != 1 || firstY[0].CredentialID != 3 || secondX[0].CredentialID != 2 {
		t.Fatalf("first choices = [%d %d %d], want [1 3 2]", firstX[0].CredentialID, firstY[0].CredentialID, secondX[0].CredentialID)
	}
}

func TestPromoteWeightedCandidatePreservesFailoverOrder(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Weight: 10},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Weight: 20},
		{CredentialID: 3, ProviderID: 3, RawModel: "model-x", Weight: 70},
	}

	planned := promoteWeightedCandidate(candidates, 30)
	want := []int{3, 1, 2}
	for i, credentialID := range want {
		if planned[i].CredentialID != credentialID {
			t.Fatalf("planned credential at %d = %d, want %d", i, planned[i].CredentialID, credentialID)
		}
	}
}

func TestPromoteWeightedCandidateIgnoresNonPositiveWeights(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Weight: 0},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Weight: -1},
		{CredentialID: 3, ProviderID: 3, RawModel: "model-x", Weight: 10},
	}

	for counter := uint64(0); counter < 20; counter++ {
		planned := promoteWeightedCandidate(candidates, counter)
		if planned[0].CredentialID != 3 {
			t.Fatalf("counter %d selected credential %d, want 3", counter, planned[0].CredentialID)
		}
	}
}

func TestPromoteWeightedCandidateKeepsHealthOrderWhenAllWeightsDisabled(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 3, ProviderID: 3, RawModel: "model-x", Weight: 0},
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Weight: 0},
	}

	planned := promoteWeightedCandidate(candidates, 1)
	if planned[0].CredentialID != 3 || planned[1].CredentialID != 1 {
		t.Fatalf("planned order = [%d %d], want [3 1]", planned[0].CredentialID, planned[1].CredentialID)
	}
}

func TestPlanCandidatesKeepsTierPriorityOverWeight(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Weight: 1, Routable: true},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 2, Weight: 1000, Routable: true},
	}

	for i := 0; i < 10; i++ {
		planned := router.PlanCandidates(candidates, nil, policy, nil)
		if planned[0].CredentialID != 1 {
			t.Fatalf("request %d selected tier-2 credential %d before tier-1", i, planned[0].CredentialID)
		}
	}
}

func TestPlanCandidatesAppliesStickyAfterWeight(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	stickyCredentialID := 2
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Weight: 1000, Routable: true},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Weight: 1, Routable: true},
	}

	planned := router.PlanCandidates(candidates, &stickyCredentialID, policy, nil)
	if planned[0].CredentialID != stickyCredentialID {
		t.Fatalf("sticky credential = %d, want %d", planned[0].CredentialID, stickyCredentialID)
	}
}

func TestPlanCandidatesKeepsPreferredBillingRoundOverWeight(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", BillingMode: "token_plan", Tier: 1, Weight: 1, Routable: true},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", BillingMode: "token", Tier: 1, Weight: 1000, Routable: true},
	}

	for i := 0; i < 10; i++ {
		planned := router.PlanCandidates(candidates, nil, policy, nil)
		if planned[0].CredentialID != 1 {
			t.Fatalf("request %d selected PAYG credential %d before preferred plan", i, planned[0].CredentialID)
		}
	}
}
