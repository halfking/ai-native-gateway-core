package routing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routerFpSlotsStub struct {
	states map[string]*credentialfpslot.NodeState
}

func (s *routerFpSlotsStub) Enabled() bool { return true }

func (s *routerFpSlotsStub) Stats(_ context.Context, _ int, _ *int) (*int, *int, *int) {
	return nil, nil, nil
}

func (s *routerFpSlotsStub) GetNodeState(_ context.Context, credentialID int, model string) (*credentialfpslot.NodeState, error) {
	if s.states == nil {
		return nil, nil
	}
	return s.states[nodeKeyForTest(credentialID, model)], nil
}

func nodeKeyForTest(credentialID int, model string) string {
	return fmt.Sprintf("%d:%s", credentialID, model)
}

func TestRouterPlanCandidates_FiltersDisabledRouteNodes(t *testing.T) {
	router := NewRouter(NewStickyCache(), nil)
	router.FpSlots = &routerFpSlotsStub{states: map[string]*credentialfpslot.NodeState{
		nodeKeyForTest(100, "gpt-4"): {
			CredentialID:  100,
			Model:         "gpt-4",
			Disabled:      true,
			DisabledUntil: time.Now().Add(5 * time.Minute).Unix(),
			SlideWindow: []credentialfpslot.NodeRecord{
				{Success: false, Timestamp: time.Now().Unix()},
			},
		},
	}}

	policy := provider.DefaultPolicy()
	candidates := []provider.Candidate{
		{CredentialID: 100, ProviderID: 1, RawModel: "gpt-4", Tier: 1, Routable: true},
		{CredentialID: 200, ProviderID: 2, RawModel: "gpt-4", Tier: 1, Routable: true},
	}

	planned := router.PlanCandidates(candidates, nil, policy, nil)
	require.Len(t, planned, 1)
	assert.Equal(t, 200, planned[0].CredentialID)
}

func TestRouterPlanCandidates_FailsOpenWhenAllRouteNodesDisabled(t *testing.T) {
	router := NewRouter(NewStickyCache(), nil)
	disabledUntil := time.Now().Add(5 * time.Minute).Unix()
	router.FpSlots = &routerFpSlotsStub{states: map[string]*credentialfpslot.NodeState{
		nodeKeyForTest(100, "gpt-4"): {
			CredentialID:   100,
			Model:          "gpt-4",
			Disabled:       true,
			DisabledUntil:  disabledUntil,
			FailureCount:   3,
			DisabledReason: "consecutive 3 failures",
		},
		nodeKeyForTest(200, "gpt-4"): {
			CredentialID:   200,
			Model:          "gpt-4",
			Disabled:       true,
			DisabledUntil:  disabledUntil,
			FailureCount:   3,
			DisabledReason: "consecutive 3 failures",
		},
	}}

	policy := provider.DefaultPolicy()
	candidates := []provider.Candidate{
		{CredentialID: 100, ProviderID: 1, RawModel: "gpt-4", Tier: 1, Routable: true},
		{CredentialID: 200, ProviderID: 2, RawModel: "gpt-4", Tier: 1, Routable: true},
	}

	planned := router.PlanCandidates(candidates, nil, policy, nil)
	require.Len(t, planned, 2)
	assert.ElementsMatch(t, []int{100, 200}, []int{planned[0].CredentialID, planned[1].CredentialID})
}
