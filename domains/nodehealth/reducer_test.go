package nodehealth_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func TestOutcomeReducerTemporaryFailureProgression(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	want := []requestjourney.NodeHealthStatus{
		requestjourney.NodeHealthSuspect,
		requestjourney.NodeHealthSuspect,
		requestjourney.NodeHealthDegraded,
	}
	for i, wantStatus := range want {
		decision, err := r.Reduce(nodehealth.Observation{
			Node:      nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
			AttemptID: fmt.Sprintf("attempt-%d", i+1),
			Phase:     nodehealth.PhaseRequest,
			Outcome:   requestjourney.OutcomeFailure,
			ErrorKind: nodehealth.ErrorKindNetwork,
		})
		if err != nil {
			t.Fatalf("failure %d: %v", i+1, err)
		}
		if decision.Status != wantStatus || decision.ConsecutiveFailures != i+1 {
			t.Fatalf("failure %d decision = %+v, want status=%s failures=%d", i+1, decision, wantStatus, i+1)
		}
		assertEffect(t, decision, nodehealth.EffectPersistNodeStatus)
		assertEffect(t, decision, nodehealth.EffectUpdateURSM)
		if i < 2 {
			for _, forbidden := range []nodehealth.EffectKind{
				nodehealth.EffectRecordCircuitFailure,
				nodehealth.EffectSetBindingUnavailable,
				nodehealth.EffectSetCredentialUnavailable,
				nodehealth.EffectInvalidateCandidateCache,
				nodehealth.EffectScheduleProbe,
			} {
				assertNoEffect(t, decision, forbidden)
			}
		} else {
			for _, required := range []nodehealth.EffectKind{
				nodehealth.EffectRecordCircuitFailure,
				nodehealth.EffectSetBindingUnavailable,
				nodehealth.EffectSetCredentialUnavailable,
				nodehealth.EffectInvalidateCandidateCache,
				nodehealth.EffectScheduleProbe,
			} {
				assertEffect(t, decision, required)
			}
		}
	}
}

func TestOutcomeReducerPermanentFailuresQuarantineImmediately(t *testing.T) {
	for _, kind := range []nodehealth.ErrorKind{
		nodehealth.ErrorKindAuth,
		nodehealth.ErrorKindQuota,
		nodehealth.ErrorKindModelBinding,
	} {
		t.Run(string(kind), func(t *testing.T) {
			decision, err := nodehealth.NewOutcomeReducer().Reduce(nodehealth.Observation{
				Node:      nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
				AttemptID: "attempt-1",
				Phase:     nodehealth.PhaseRequest,
				Outcome:   requestjourney.OutcomeFailure,
				ErrorKind: kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Status != requestjourney.NodeHealthQuarantined {
				t.Fatalf("decision = %+v, want quarantined", decision)
			}
			for _, required := range []nodehealth.EffectKind{
				nodehealth.EffectRecordCircuitFailure,
				nodehealth.EffectSetBindingUnavailable,
				nodehealth.EffectSetCredentialUnavailable,
				nodehealth.EffectInvalidateCandidateCache,
				nodehealth.EffectScheduleProbe,
				nodehealth.EffectQuarantine,
			} {
				assertEffect(t, decision, required)
			}
		})
	}
}

func TestOutcomeReducerSuccessResetsFailures(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	for i := 0; i < 3; i++ {
		_, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("failure-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	decision, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "success", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != requestjourney.NodeHealthHealthy || decision.ConsecutiveFailures != 0 {
		t.Fatalf("success decision = %+v", decision)
	}
	for _, effect := range []nodehealth.EffectKind{
		nodehealth.EffectRecoverCircuit,
		nodehealth.EffectRestoreBinding,
		nodehealth.EffectRestoreCredential,
		nodehealth.EffectUpdateURSM,
		nodehealth.EffectInvalidateCandidateCache,
		nodehealth.EffectCancelProbeBackoff,
	} {
		assertEffect(t, decision, effect)
	}
}

func TestOutcomeReducerProbeRecoveryRequiresBothPhases(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	_, _ = r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "failure", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	})

	direct, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "probe-1", Phase: nodehealth.PhaseDirectProbe,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Status != requestjourney.NodeHealthRecovering || direct.ConsecutiveFailures != 1 {
		t.Fatalf("direct decision = %+v, want recovering with existing streak", direct)
	}
	if hasEffect(direct, nodehealth.EffectRestoreBinding) || hasEffect(direct, nodehealth.EffectRecoverCircuit) {
		t.Fatalf("direct success restored node too early: %+v", direct.Effects)
	}

	gateway, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "probe-1", Phase: nodehealth.PhaseGatewayProbe,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gateway.Status != requestjourney.NodeHealthHealthy || gateway.ConsecutiveFailures != 0 {
		t.Fatalf("gateway decision = %+v, want healthy reset", gateway)
	}
	assertEffect(t, gateway, nodehealth.EffectRestoreBinding)
	assertEffect(t, gateway, nodehealth.EffectRecoverCircuit)
}

func TestOutcomeReducerClientFailureAndCancellationHaveNoHealthEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		outcome    requestjourney.Outcome
		errorKind  nodehealth.ErrorKind
		httpStatus int
	}{
		{name: "client failure", outcome: requestjourney.OutcomeFailure, errorKind: nodehealth.ErrorKindRequest, httpStatus: 400},
		{name: "canceled", outcome: requestjourney.OutcomeCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := nodehealth.NewOutcomeReducer().Reduce(nodehealth.Observation{
				Node:       nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
				AttemptID:  "attempt-1",
				Phase:      nodehealth.PhaseRequest,
				Outcome:    tc.outcome,
				ErrorKind:  tc.errorKind,
				HTTPStatus: tc.httpStatus,
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Status != requestjourney.NodeHealthHealthy || decision.ConsecutiveFailures != 0 {
				t.Fatalf("decision = %+v, want unchanged healthy state", decision)
			}
			if len(decision.Effects) != 0 {
				t.Fatalf("health effects = %+v, want none", decision.Effects)
			}
		})
	}
}

func TestOutcomeReducerConcurrentDuplicateAppliesAdapterOnce(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	adapter := &countingAdapter{}
	observation := nodehealth.Observation{
		Node: nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}, AttemptID: "attempt-1",
		Phase: nodehealth.PhaseRequest, Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.ReduceAndApply(context.Background(), observation, adapter); err != nil {
				t.Errorf("ReduceAndApply: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := adapter.calls.Load(); got != 1 {
		t.Fatalf("adapter calls = %d, want 1", got)
	}
}

type countingAdapter struct{ calls atomic.Int64 }

func (a *countingAdapter) ApplyNodeHealthDecision(context.Context, nodehealth.Decision) error {
	a.calls.Add(1)
	return nil
}

func assertEffect(t *testing.T, decision nodehealth.Decision, want nodehealth.EffectKind) {
	t.Helper()
	if !hasEffect(decision, want) {
		t.Fatalf("effects = %+v, want %s", decision.Effects, want)
	}
}

func assertNoEffect(t *testing.T, decision nodehealth.Decision, forbidden nodehealth.EffectKind) {
	t.Helper()
	if hasEffect(decision, forbidden) {
		t.Fatalf("effects = %+v, must not contain %s", decision.Effects, forbidden)
	}
}

func hasEffect(decision nodehealth.Decision, want nodehealth.EffectKind) bool {
	for _, effect := range decision.Effects {
		if effect.Kind == want {
			return true
		}
	}
	return false
}

func TestOutcomeReducerDeduplicatesAttemptPhase(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	observation := nodehealth.Observation{
		Node:      nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
		AttemptID: "attempt-1",
		Phase:     nodehealth.PhaseRequest,
		Outcome:   requestjourney.OutcomeFailure,
		ErrorKind: nodehealth.ErrorKindNetwork,
	}

	first, err := r.Reduce(observation)
	if err != nil {
		t.Fatalf("first Reduce: %v", err)
	}
	duplicate, err := r.Reduce(observation)
	if err != nil {
		t.Fatalf("duplicate Reduce: %v", err)
	}

	if !first.Accepted || first.Duplicate {
		t.Fatalf("first decision = %+v, want accepted non-duplicate", first)
	}
	if duplicate.Accepted || !duplicate.Duplicate {
		t.Fatalf("duplicate decision = %+v, want rejected duplicate", duplicate)
	}
	if duplicate.Status != requestjourney.NodeHealthSuspect || duplicate.ConsecutiveFailures != 1 {
		t.Fatalf("duplicate changed state: %+v", duplicate)
	}
}
