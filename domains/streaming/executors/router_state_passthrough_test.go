package executors

import (
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// vapeur 遗留 #3 (2026-10-02) — ROUTER side of the state passthrough.
//
// 注入接缝纪律：本轮给 Router 开了「把已读到的 state 挂到 candidate 上」这条
// 接缝。接缝另一侧（executor 消费它）由 capability_state_passthrough_test.go
// 覆盖，但**生产侧的挂载点本身**如果没人测，它可以完全不挂而全绿——
// executor 那侧会因为拿到 nil 而安静地回落自己读，测试照样绿。
// 所以挂载点必须单独钉住。
//
// 这正是上一轮 capability_backfill 踩过的坑的同款：判据只写在接缝的
// 消费侧，生产侧改坏了没有任何用例会红。

// errStubBatch is the batch-read failure used by the fail-open cases.
var errStubBatch = errors.New("stub: node state batch read failed")

// nodeStateWithVerdict builds a state carrying a known negative Responses
// verdict, so tests can tell "attached the right state" from "attached some
// state".
func nodeStateWithVerdict(credID int, model string, supported bool) *credentialfpslot.NodeState {
	verdict := supported
	return &credentialfpslot.NodeState{
		CredentialID:        credID,
		Model:               model,
		SlideWindow:         []credentialfpslot.NodeRecord{},
		Capabilities:        &credentialfpslot.NodeCapabilities{SupportsResponses: &verdict},
		CapabilityExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
}

// A healthy node must carry the state the router already read, so the
// executor can skip the second GET.
func TestFilterHealthyNodes_AttachesStateToHealthyCandidate(t *testing.T) {
	state := nodeStateWithVerdict(1, "minimax-m3", false)
	stub := &stubFpSlots{states: []*credentialfpslot.NodeState{state}}
	r := newCoolingFallbackTestRouter(stub)

	in := []provider.Candidate{{CredentialID: 1, RawModel: "minimax-m3"}}
	out := r.filterHealthyNodes(in)
	if len(out) != 1 {
		t.Fatalf("healthy candidates = %d, want 1", len(out))
	}
	if out[0].RoutedNodeState == nil {
		t.Fatal("RoutedNodeState is nil: the executor would fall back to re-reading the key")
	}
	if out[0].RoutedNodeState.Capabilities.SupportsResponses == nil {
		t.Fatal("attached state carries no verdict; it is the wrong state")
	}
	if out[0].RoutedNodeState.CapabilityExpiresAt != state.CapabilityExpiresAt {
		t.Fatalf("attached a different state than the one read (expires_at=%d, want %d)",
			out[0].RoutedNodeState.CapabilityExpiresAt, state.CapabilityExpiresAt)
	}
}

// Index alignment: with several candidates, each must get ITS OWN state.
// Pairing candidate i with states[j] would hand a node another node's verdict
// — a silent protocol-selection bug, not a crash.
func TestFilterHealthyNodes_AttachesPerCandidateState(t *testing.T) {
	s1 := nodeStateWithVerdict(1, "minimax-m3", false)
	s2 := nodeStateWithVerdict(2, "minimax-m3", true)
	s3 := nodeStateWithVerdict(3, "minimax-m3", false)
	stub := &stubFpSlots{states: []*credentialfpslot.NodeState{s1, s2, s3}}
	r := newCoolingFallbackTestRouter(stub)

	in := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
		{CredentialID: 3, RawModel: "minimax-m3"},
	}
	out := r.filterHealthyNodes(in)
	if len(out) != 3 {
		t.Fatalf("healthy candidates = %d, want 3", len(out))
	}
	for i, want := range []*credentialfpslot.NodeState{s1, s2, s3} {
		got := out[i].RoutedNodeState
		if got == nil {
			t.Fatalf("candidate %d: no state attached", i)
		}
		if got.CapabilityExpiresAt != want.CapabilityExpiresAt {
			t.Fatalf("candidate %d: attached the wrong state (expires_at=%d, want %d)",
				i, got.CapabilityExpiresAt, want.CapabilityExpiresAt)
		}
	}
}

// A nil slot in the batch result must stay nil on the candidate: nil means
// "no snapshot, re-read", and a zero-valued state would instead read as
// "no verdict" — which is a different answer (see the fallback contract).
func TestFilterHealthyNodes_NilStateStaysNil(t *testing.T) {
	stub := &stubFpSlots{states: []*credentialfpslot.NodeState{nil}}
	r := newCoolingFallbackTestRouter(stub)

	in := []provider.Candidate{{CredentialID: 1, RawModel: "minimax-m3"}}
	out := r.filterHealthyNodes(in)
	if len(out) != 1 {
		t.Fatalf("healthy candidates = %d, want 1", len(out))
	}
	if out[0].RoutedNodeState != nil {
		t.Fatal("a nil batch slot must not become a non-nil snapshot")
	}
}

// Batch read failure must fail open with NO state attached: the router did not
// learn anything, and claiming a snapshot would suppress the executor's read.
func TestFilterHealthyNodes_BatchErrorAttachesNoState(t *testing.T) {
	stub := &stubFpSlots{err: errStubBatch}
	r := newCoolingFallbackTestRouter(stub)

	in := []provider.Candidate{{CredentialID: 1, RawModel: "minimax-m3"}}
	out := r.filterHealthyNodes(in)
	if len(out) != 1 {
		t.Fatalf("fail-open must return all candidates, got %d", len(out))
	}
	if out[0].RoutedNodeState != nil {
		t.Fatal("batch read failed: a state was attached anyway")
	}
}

// A node filtered OUT for health is not in the returned slice, so there is
// nothing to assert about its state; but the surviving candidates must still be
// aligned after the filtering pass.
func TestFilterHealthyNodes_FilteredNodeDoesNotShiftAlignment(t *testing.T) {
	cooling := time.Now().Unix() + 3600
	bad := &credentialfpslot.NodeState{
		CredentialID: 1, Model: "minimax-m3", SlideWindow: []credentialfpslot.NodeRecord{},
		Disabled: true, DisabledUntil: cooling,
	}
	good := nodeStateWithVerdict(2, "minimax-m3", false)
	stub := &stubFpSlots{states: []*credentialfpslot.NodeState{bad, good}}
	r := newCoolingFallbackTestRouter(stub)

	in := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	out := r.filterHealthyNodes(in)
	if len(out) != 1 {
		t.Fatalf("healthy candidates = %d, want 1 (the cooling node must be filtered)", len(out))
	}
	if out[0].CredentialID != 2 {
		t.Fatalf("surviving candidate = %d, want 2", out[0].CredentialID)
	}
	if out[0].RoutedNodeState == nil {
		t.Fatal("surviving candidate lost its state after filtering")
	}
	if out[0].RoutedNodeState.CapabilityExpiresAt != good.CapabilityExpiresAt {
		t.Fatal("after filtering, the survivor got a misaligned state")
	}
}

// chooseLeastCooledCandidate paid for its own MGET, so it attaches too — and
// it must attach the state of the index it actually PICKED, not states[0] or
// states[bestIdx] when the pick came from an early-exit branch.
func TestChooseLeastCooledCandidate_AttachesPickedState(t *testing.T) {
	now := time.Now().Unix()
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
		{CredentialID: 3, RawModel: "minimax-m3"},
	}
	// Index 1 is the earliest-expiring cooldown, so it wins via the bestUntil
	// branch (no early exit).
	states := []*credentialfpslot.NodeState{
		{Disabled: true, DisabledUntil: now + 7200, SlideWindow: []credentialfpslot.NodeRecord{}},
		{Disabled: true, DisabledUntil: now + 600, SlideWindow: []credentialfpslot.NodeRecord{}},
		{Disabled: true, DisabledUntil: now + 3600, SlideWindow: []credentialfpslot.NodeRecord{}},
	}
	r := newCoolingFallbackTestRouter(&stubFpSlots{states: states})

	got := r.chooseLeastCooledCandidate(candidates)
	if got == nil {
		t.Fatal("nil candidate")
	}
	if got.CredentialID != 2 {
		t.Fatalf("picked credential %d, want 2", got.CredentialID)
	}
	if got.RoutedNodeState == nil {
		t.Fatal("no state attached on the cooling-fallback path")
	}
	if got.RoutedNodeState.DisabledUntil != now+600 {
		t.Fatalf("attached the wrong index's state (DisabledUntil=%d, want %d)",
			got.RoutedNodeState.DisabledUntil, now+600)
	}
}

// The early-exit branch (no state at all ⇒ never disabled ⇒ first in line)
// picks index 0, and the attached state must be states[0] — this is the branch
// that a stale states[bestIdx] attachment would get wrong.
func TestChooseLeastCooledCandidate_EarlyExitAttachesMatchingIndex(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	first := nodeStateWithVerdict(1, "minimax-m3", false)
	// Index 0 has NO state → early exit picks index 0.
	states := []*credentialfpslot.NodeState{nil, nodeStateWithVerdict(2, "minimax-m3", true)}
	r := newCoolingFallbackTestRouter(&stubFpSlots{states: states})

	got := r.chooseLeastCooledCandidate(candidates)
	if got == nil {
		t.Fatal("nil candidate")
	}
	if got.CredentialID != 1 {
		t.Fatalf("picked credential %d, want 1 (index 0 has no state)", got.CredentialID)
	}
	// states[0] is nil, so the correct attachment is nil — NOT states[1].
	if got.RoutedNodeState != nil {
		t.Fatal("attached states[1] to a candidate picked from a nil slot: index mismatch")
	}
	if first == nil { // keep the helper referenced and the intent explicit
		t.Fatal("unreachable")
	}
}

// The cooling-fallback path must not mutate the caller's candidate slice.
func TestChooseLeastCooledCandidate_DoesNotMutateInput(t *testing.T) {
	now := time.Now().Unix()
	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "minimax-m3"},
		{CredentialID: 2, RawModel: "minimax-m3"},
	}
	states := []*credentialfpslot.NodeState{
		{Disabled: true, DisabledUntil: now + 7200, SlideWindow: []credentialfpslot.NodeRecord{}},
		{Disabled: true, DisabledUntil: now + 600, SlideWindow: []credentialfpslot.NodeRecord{}},
	}
	r := newCoolingFallbackTestRouter(&stubFpSlots{states: states})

	got := r.chooseLeastCooledCandidate(candidates)
	if got == nil {
		t.Fatal("nil candidate")
	}
	for i, c := range candidates {
		if c.RoutedNodeState != nil {
			t.Fatalf("caller's candidate %d was mutated in place", i)
		}
	}
}
