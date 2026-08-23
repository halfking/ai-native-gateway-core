// 2026-08-23 hzx-2 audit: regression tests for the all-candidates-cooling
// fallback path added to Router.planCandidates.
//
// We exercise chooseLeastCooledCandidate directly with a fake FpSlots
// (the function is the only public surface that matters for the fallback)
// and verify the selection picks the smallest DisabledUntil.

package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFpSlots satisfies the Router.FpSlots interface without touching Redis.
type stubFpSlots struct {
	states []*credentialfpslot.NodeState
	err    error
}

func (s *stubFpSlots) Enabled() bool { return true }
func (s *stubFpSlots) Stats(_ context.Context, _ int, _ *int) (*int, *int, *int) {
	return nil, nil, nil
}
func (s *stubFpSlots) GetNodeState(_ context.Context, _ int, _ string) (*credentialfpslot.NodeState, error) {
	return nil, nil
}
func (s *stubFpSlots) GetNodeStatesBatch(_ context.Context, keys []credentialfpslot.NodeStateKey) ([]*credentialfpslot.NodeState, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*credentialfpslot.NodeState, len(keys))
	for i := range keys {
		if i < len(s.states) {
			out[i] = s.states[i]
		} else {
			out[i] = nil
		}
	}
	return out, nil
}

func newCoolingFallbackTestRouter(fpSlots *stubFpSlots) *Router {
	return &Router{FpSlots: fpSlots}
}

func TestChooseLeastCooledCandidate_PicksSoonest(t *testing.T) {
	now := time.Now().Unix()
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
		{CredentialID: 3, RawModel: "minimax-m3"},
	}
	stub := &stubFpSlots{
		states: []*credentialfpslot.NodeState{
			{Disabled: true, DisabledUntil: now + 600}, // 10min
			{Disabled: true, DisabledUntil: now + 60},  // 1min — winner
			{Disabled: true, DisabledUntil: now + 300}, // 5min
		},
	}
	r := newCoolingFallbackTestRouter(stub)
	got := r.chooseLeastCooledCandidate(candidates)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.CredentialID, "should pick the soonest DisabledUntil")
}

func TestChooseLeastCooledCandidate_PrefersNoCooldown(t *testing.T) {
	now := time.Now().Unix()
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	stub := &stubFpSlots{
		states: []*credentialfpslot.NodeState{
			{Disabled: true, DisabledUntil: now + 300},
			nil, // no node-state ⇒ never disabled ⇒ immediate winner
		},
	}
	r := newCoolingFallbackTestRouter(stub)
	got := r.chooseLeastCooledCandidate(candidates)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.CredentialID, "candidate without node-state is always preferred")
}

func TestChooseLeastCooledCandidate_PicksExpiredCooldown(t *testing.T) {
	now := time.Now().Unix()
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	stub := &stubFpSlots{
		states: []*credentialfpslot.NodeState{
			{Disabled: true, DisabledUntil: now + 600},
			{Disabled: true, DisabledUntil: now - 1}, // already expired
		},
	}
	r := newCoolingFallbackTestRouter(stub)
	got := r.chooseLeastCooledCandidate(candidates)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.CredentialID, "expired DisabledUntil must be picked (already eligible)")
}

func TestChooseLeastCooledCandidate_Empty(t *testing.T) {
	r := newCoolingFallbackTestRouter(&stubFpSlots{})
	assert.Nil(t, r.chooseLeastCooledCandidate(nil))
	assert.Nil(t, r.chooseLeastCooledCandidate([]provider.Candidate{}))
}

func TestChooseLeastCooledCandidate_FailOpen(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	stub := &stubFpSlots{err: assert.AnError}
	r := newCoolingFallbackTestRouter(stub)
	got := r.chooseLeastCooledCandidate(candidates)
	require.NotNil(t, got)
	assert.Equal(t, 1, got.CredentialID, "Redis read failure must fail-open to the first candidate")
}
