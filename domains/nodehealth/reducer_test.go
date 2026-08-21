package nodehealth_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

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

func TestOutcomeReducerModelBindingFailureIsolatesCredentialScope(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}

	decision, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "attempt-1", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindModelBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != requestjourney.NodeHealthQuarantined {
		t.Fatalf("decision = %+v, want quarantined binding node", decision)
	}
	for _, required := range []nodehealth.EffectKind{
		nodehealth.EffectPersistNodeStatus,
		nodehealth.EffectUpdateURSM,
		nodehealth.EffectSetBindingUnavailable,
		nodehealth.EffectInvalidateCandidateCache,
		nodehealth.EffectScheduleProbe,
		nodehealth.EffectQuarantine,
	} {
		assertEffect(t, decision, required)
	}
	for _, forbidden := range []nodehealth.EffectKind{
		nodehealth.EffectRecordCircuitFailure,
		nodehealth.EffectSetCredentialUnavailable,
	} {
		assertNoEffect(t, decision, forbidden)
	}
}

func TestOutcomeReducerSiblingModelKeepsServingAfterBindingFailure(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	modelA := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	modelB := nodehealth.NodeKey{CredentialID: 42, Model: "model-b"}

	bindingFailure, err := r.Reduce(nodehealth.Observation{
		Node: modelA, AttemptID: "attempt-a", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindModelBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bindingFailure.Status != requestjourney.NodeHealthQuarantined {
		t.Fatalf("model-a binding failure = %+v, want quarantined", bindingFailure)
	}

	// model-b on the same credential starts from its own healthy state and
	// its failures still carry full credential-wide evidence.
	bFailure, err := r.Reduce(nodehealth.Observation{
		Node: modelB, AttemptID: "attempt-b", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bFailure.PreviousStatus != requestjourney.NodeHealthHealthy {
		t.Fatalf("model-b state polluted by model-a binding failure: %+v", bFailure)
	}
	assertEffect(t, bFailure, nodehealth.EffectUpdateURSM)
	assertNoEffect(t, bFailure, nodehealth.EffectRecordCircuitFailure)

	// A model-b success may recover its own binding, but must not restore
	// model-a's quarantined binding through a credential-wide side effect.
	bSuccess, err := r.Reduce(nodehealth.Observation{
		Node: modelB, AttemptID: "attempt-b2", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bSuccess.Status != requestjourney.NodeHealthHealthy {
		t.Fatalf("model-b success = %+v, want healthy", bSuccess)
	}
	for _, effect := range []nodehealth.EffectKind{
		nodehealth.EffectRecoverCircuit,
		nodehealth.EffectRestoreBinding,
	} {
		assertEffect(t, bSuccess, effect)
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
			// Credential-scoped permanent kinds escalate the credential-wide
			// circuit and availability. ModelBinding is deliberately excluded:
			// its isolation contract is pinned by
			// TestOutcomeReducerModelBindingFailureIsolatesCredentialScope.
			if kind == nodehealth.ErrorKindModelBinding {
				return
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

func TestOutcomeReducerSeenEntryExpiresAfterTTL(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		SeenTTL: 10 * time.Minute,
		Now:     func() time.Time { return now },
	})
	observation := nodehealth.Observation{
		Node:      nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
		AttemptID: "attempt-1",
		Phase:     nodehealth.PhaseRequest,
		Outcome:   requestjourney.OutcomeFailure,
		ErrorKind: nodehealth.ErrorKindNetwork,
	}

	first, err := r.Reduce(observation)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Accepted || first.Duplicate {
		t.Fatalf("first decision = %+v, want accepted", first)
	}

	within, err := r.Reduce(observation)
	if err != nil {
		t.Fatal(err)
	}
	if !within.Duplicate || within.Accepted {
		t.Fatalf("replay within TTL = %+v, want deduplicated", within)
	}

	now = now.Add(11 * time.Minute)
	expired, err := r.Reduce(observation)
	if err != nil {
		t.Fatal(err)
	}
	if !expired.Accepted || expired.Duplicate {
		t.Fatalf("replay after TTL = %+v, want re-accepted once the entry expired", expired)
	}
}

func TestOutcomeReducerTTLReclaimKeepsNodeState(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		SeenTTL: 5 * time.Minute,
		Now:     func() time.Time { return now },
	})
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	for i := 0; i < 3; i++ {
		if _, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("attempt-%d", i+1), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
		}); err != nil {
			t.Fatal(err)
		}
	}

	now = now.Add(6 * time.Minute)
	replayed, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "attempt-1", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Accepted || replayed.Duplicate {
		t.Fatalf("expired event should be accepted again: %+v", replayed)
	}
	if replayed.PreviousStatus != requestjourney.NodeHealthDegraded {
		t.Fatalf("node state was reset during TTL reclaim: %+v", replayed)
	}
	if replayed.Status != requestjourney.NodeHealthHealthy || replayed.ConsecutiveFailures != 0 {
		t.Fatalf("success after TTL reclaim did not recover node: %+v", replayed)
	}
}

// seenEvictedTotal reads the nodehealth_reducer_seen_evicted_total counter for
// one reason label. Counters are process-global; tests assert deltas.
func seenEvictedTotal(t *testing.T, reason string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather prometheus metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "nodehealth_reducer_seen_evicted_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "reason" && label.GetValue() == reason {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestOutcomeReducerCountsSeenEvictionsByReason(t *testing.T) {
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	failure := func(attemptID string) nodehealth.Observation {
		return nodehealth.Observation{
			Node: node, AttemptID: attemptID, Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
		}
	}

	ttlBefore := seenEvictedTotal(t, "ttl")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	ttlReducer := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		SeenTTL: 5 * time.Minute,
		Now:     func() time.Time { return now },
	})
	if _, err := ttlReducer.Reduce(failure("attempt-1")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if _, err := ttlReducer.Reduce(failure("attempt-2")); err != nil {
		t.Fatal(err)
	}
	if delta := seenEvictedTotal(t, "ttl") - ttlBefore; delta < 1 {
		t.Fatalf("ttl evictions delta = %v, want >= 1", delta)
	}

	capacityBefore := seenEvictedTotal(t, "capacity")
	capacityReducer := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		SeenCapacity: 2,
		Now:          func() time.Time { return now },
	})
	for _, attemptID := range []string{"attempt-1", "attempt-2", "attempt-3"} {
		if _, err := capacityReducer.Reduce(failure(attemptID)); err != nil {
			t.Fatal(err)
		}
	}
	if delta := seenEvictedTotal(t, "capacity") - capacityBefore; delta < 1 {
		t.Fatalf("capacity evictions delta = %v, want >= 1", delta)
	}
}

func TestOutcomeReducerZeroValueUsable(t *testing.T) {
	var r nodehealth.OutcomeReducer
	decision, err := r.Reduce(nodehealth.Observation{
		Node:      nodehealth.NodeKey{CredentialID: 42, Model: "model-a"},
		AttemptID: "attempt-1",
		Phase:     nodehealth.PhaseRequest,
		Outcome:   requestjourney.OutcomeFailure,
		ErrorKind: nodehealth.ErrorKindNetwork,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Accepted || decision.Status != requestjourney.NodeHealthSuspect {
		t.Fatalf("zero-value reducer decision = %+v, want accepted suspect", decision)
	}
}

func TestOutcomeReducerEvictsOldDedupEntriesWithoutResettingNodeState(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithSeenCapacity(2)
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	for _, attemptID := range []string{"attempt-1", "attempt-2", "attempt-3"} {
		if _, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: attemptID, Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
		}); err != nil {
			t.Fatal(err)
		}
	}

	replayed, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "attempt-1", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Accepted || replayed.Duplicate {
		t.Fatalf("evicted event should be accepted again: %+v", replayed)
	}
	if replayed.PreviousStatus != requestjourney.NodeHealthDegraded {
		t.Fatalf("node state was reset during event eviction: %+v", replayed)
	}
	if replayed.Status != requestjourney.NodeHealthHealthy || replayed.ConsecutiveFailures != 0 {
		t.Fatalf("success after eviction did not recover node: %+v", replayed)
	}
}
