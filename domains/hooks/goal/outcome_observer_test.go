package goal

import (
	"context"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

type recordingOutcomeObserver struct {
	mu       sync.Mutex
	outcomes []Outcome
}

func (o *recordingOutcomeObserver) ObserveGoalOutcome(_ context.Context, outcome Outcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, outcome)
	return nil
}

func (o *recordingOutcomeObserver) last() Outcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.outcomes) == 0 {
		return Outcome{}
	}
	return o.outcomes[len(o.outcomes)-1]
}

func TestModeHook_OutcomeObserverPreservesModelSwitchAction(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID: "observer-switch", TenantID: "tenant-a", State: StateActive,
		AutoContinueCount: 3, CurrentModel: "gpt-4o",
	})
	observer := &recordingOutcomeObserver{}
	hook := newTestHook(t, store, nil)
	hook.SetOutcomeObserver(observer)

	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "observer-switch", TenantID: "tenant-a", FinishReason: "stop",
		ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"still working"},"finish_reason":"stop"}]}`),
	})
	if err != nil || result == nil || result.Action != "goal_model_switch" {
		t.Fatalf("observer changed model-switch result: result=%+v err=%v", result, err)
	}
	outcome := observer.last()
	if outcome.Kind != OutcomeDegraded || outcome.Reason != "model_switched" || outcome.ModelSwitchCount != 1 {
		t.Fatalf("unexpected degraded outcome: %+v", outcome)
	}
}

func TestModeHook_OutcomeObserverReportsGiveUp(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID: "observer-failed", TenantID: "tenant-a", State: StateActive,
		AutoContinueCount: 3, ModelSwitchCount: 2, CurrentModel: "gpt-4o",
	})
	observer := &recordingOutcomeObserver{}
	hook := newTestHook(t, store, nil)
	hook.SetOutcomeObserver(observer)

	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "observer-failed", TenantID: "tenant-a", FinishReason: "stop",
		ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"stuck"},"finish_reason":"stop"}]}`),
	})
	if err != nil || result != nil {
		t.Fatalf("observer changed give-up result: result=%+v err=%v", result, err)
	}
	outcome := observer.last()
	if outcome.Kind != OutcomeFailed || outcome.Reason != "switch_budget_exhausted" || outcome.ModelSwitchCount != 2 {
		t.Fatalf("unexpected failed outcome: %+v", outcome)
	}
	stored, err := store.GetSession(context.Background(), "tenant-a", "observer-failed")
	if err != nil || stored == nil || stored.State != StateFailed {
		t.Fatalf("final Goal failure was not persisted: session=%+v err=%v", stored, err)
	}
}

func TestModeHook_AuditFollowUpDoesNotReenterGoal(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "observer-audit", TenantID: "tenant-a", State: StateCompleted})
	hook := newTestHook(t, store, nil)
	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "observer-audit", TenantID: "tenant-a", FollowUpAction: "audit",
		FinishReason: "stop", ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"audit completed successfully"}}]}`),
	})
	if err != nil || result != nil {
		t.Fatalf("audit follow-up reentered Goal: result=%+v err=%v", result, err)
	}
	stored, _ := store.GetSession(context.Background(), "tenant-a", "observer-audit")
	if stored == nil || stored.State != StateCompleted || stored.AutoContinueCount != 0 {
		t.Fatalf("audit follow-up mutated Goal session: %+v", stored)
	}
}

func TestModeHook_OutcomeObserverReportsCompletion(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "observer-complete", TenantID: "tenant-a", State: StateActive})
	observer := &recordingOutcomeObserver{}
	hook := newTestHook(t, store, nil)
	hook.SetOutcomeObserver(observer)

	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "observer-complete", TenantID: "tenant-a", FinishReason: "stop",
		ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"task completed successfully"},"finish_reason":"stop"}]}`),
	})
	if err != nil || result != nil {
		t.Fatalf("observer changed completion result: result=%+v err=%v", result, err)
	}
	if outcome := observer.last(); outcome.Kind != OutcomeCompleted {
		t.Fatalf("unexpected completion outcome: %+v", outcome)
	}
}
