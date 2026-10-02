package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// flashBlipObserver completes mockStateObserver with the no-candidates arm
// the Executor.StateObserver interface requires.
type flashBlipObserver struct {
	*mockStateObserver
}

func (flashBlipObserver) OnNoCandidates(ctx context.Context, sig credentialstate.NoCandidatesSignal) {}

// Wave 3 B2② flash-blip double confirmation: the FIRST consecutive
// network/timeout failure of a node defers its degrade-family state writes
// behind NodeProbeConfirm; the verdict then applies (both pings failed) or
// suppresses (a ping succeeded) them. Later consecutive failures and other
// error kinds degrade immediately as before.

func flashBlipDecision(consecutiveFailures int, kind nodehealth.ErrorKind) nodehealth.Decision {
	return nodehealth.Decision{
		Node:                nodehealth.NodeKey{TenantID: "default", ProviderID: 7, CredentialID: 22, Model: "vendor-model-a"},
		AttemptID:           "attempt-1",
		Phase:               nodehealth.PhaseRequest,
		Outcome:             requestjourney.OutcomeFailure,
		Accepted:            true,
		ConsecutiveFailures: consecutiveFailures,
		ErrorKind:           kind,
		RequestID:           "req-1",
		Effects: []nodehealth.Effect{
			{Kind: nodehealth.EffectPersistNodeStatus},
			{Kind: nodehealth.EffectUpdateURSM},
			{Kind: nodehealth.EffectInvalidateCandidateCache},
		},
	}
}

func TestFlashBlip_FirstNetworkFailureDefersDegradeUntilConfirmed(t *testing.T) {
	obs := &flashBlipObserver{mockStateObserver: &mockStateObserver{}}
	confirmDone := make(chan struct{}, 1)
	exec := &Executor{
		StateObserver: obs,
		NodeProbeConfirm: func(ctx context.Context, credentialID int, rawModel string) bool {
			confirmDone <- struct{}{}
			return true // both pings failed → node confirmed broken
		},
	}
	adapter := executorNodeHealthAdapter{executor: exec}

	if err := adapter.ApplyNodeHealthDecision(context.Background(), flashBlipDecision(1, nodehealth.ErrorKindNetwork)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	select {
	case <-confirmDone:
	case <-time.After(2 * time.Second):
		t.Fatal("confirm was not scheduled")
	}
	// The verdict goroutine applies the deferred degrade after the confirm
	// fake returns; poll instead of racing it.
	deadline := time.Now().Add(2 * time.Second)
	for len(obs.getFailures()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("UpdateOnFailure was never applied after a confirmed blip")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(obs.getFailures()); n != 1 {
		t.Fatalf("UpdateOnFailure after confirmed blip = %d, want 1", n)
	}
}

func TestFlashBlip_TransientBlipSuppressesDegrade(t *testing.T) {
	obs := &flashBlipObserver{mockStateObserver: &mockStateObserver{}}
	exec := &Executor{
		StateObserver: obs,
		NodeProbeConfirm: func(ctx context.Context, credentialID int, rawModel string) bool {
			return false // a ping succeeded → transient blip
		},
	}
	adapter := executorNodeHealthAdapter{executor: exec}

	if err := adapter.ApplyNodeHealthDecision(context.Background(), flashBlipDecision(1, nodehealth.ErrorKindTimeout)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Give the goroutine a beat; the degrade must never land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(obs.getFailures()) > 0 {
			t.Fatal("degrade was applied despite a transient verdict")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(obs.getFailures()); n != 0 {
		t.Fatalf("UpdateOnFailure after transient blip = %d, want 0", n)
	}
}

func TestFlashBlip_SecondFailureAndNonBlipKindsDegradeImmediately(t *testing.T) {
	obs := &flashBlipObserver{mockStateObserver: &mockStateObserver{}}
	exec := &Executor{
		StateObserver: obs,
		NodeProbeConfirm: func(ctx context.Context, credentialID int, rawModel string) bool {
			t.Fatal("confirm must not run for non-first or non-blip failures")
			return false
		},
	}
	adapter := executorNodeHealthAdapter{executor: exec}

	if err := adapter.ApplyNodeHealthDecision(context.Background(), flashBlipDecision(2, nodehealth.ErrorKindNetwork)); err != nil {
		t.Fatalf("apply streak=2: %v", err)
	}
	if err := adapter.ApplyNodeHealthDecision(context.Background(), flashBlipDecision(1, nodehealth.ErrorKindUpstream)); err != nil {
		t.Fatalf("apply upstream kind: %v", err)
	}
	if n := len(obs.getFailures()); n != 2 {
		t.Fatalf("immediate degrade calls = %d, want 2", n)
	}
}

func TestFlashBlip_NilConfirmKeepsImmediateDegrade(t *testing.T) {
	obs := &flashBlipObserver{mockStateObserver: &mockStateObserver{}}
	exec := &Executor{StateObserver: obs}
	adapter := executorNodeHealthAdapter{executor: exec}

	if err := adapter.ApplyNodeHealthDecision(context.Background(), flashBlipDecision(1, nodehealth.ErrorKindNetwork)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := len(obs.getFailures()); n != 1 {
		t.Fatalf("UpdateOnFailure without confirm seam = %d, want 1 (feature disabled)", n)
	}
}
