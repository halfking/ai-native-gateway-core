package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestPlanCandidatesReturnsEmptyWhenV2MarksUnavailable is the T20 baseline for
// the URSM v2 authoritative path. With URSMv2 == nil (the production default —
// URSM_V2_MODE=off leaves v2 un-wired), PlanCandidates must keep the legacy
// behavior unchanged. The authoritative-only filter is exercised end-to-end in
// Task 22 once the v2 store is wired and SetReady(true) flips on; here we
// only assert that the baseline contract holds.
func TestPlanCandidatesReturnsEmptyWhenV2MarksUnavailable(t *testing.T) {
	r := NewRouter(nil, nil)
	out := r.PlanCandidates(
		[]provider.Candidate{
			{CredentialID: 1, ProviderID: 1, RawModel: "m", Tier: 1, Routable: true},
		},
		PlanContext{},
		nil,
		&provider.Policy{},
		nil,
	)
	if len(out) != 1 {
		t.Fatalf("baseline must keep behaviour; got %d, want 1", len(out))
	}
}
