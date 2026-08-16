package handoff

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // observer contract test
)

func TestMemoryHandoffTrigger_DebouncesAndAllowsSeverityUpgrade(t *testing.T) {
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	trigger.now = func() time.Time { return now }
	pressure := TriggerSignal{Kind: SignalContextPressure, Severity: 2, Reason: "percentage_threshold"}

	reservationID, _, ok := trigger.Reserve("gw_old", pressure)
	if !ok {
		t.Fatal("first context signal must reserve")
	}
	trigger.Commit(reservationID)
	if _, _, ok := trigger.Reserve("gw_old", pressure); ok {
		t.Fatal("same-severity signal must be debounced after commit")
	}
	if err := trigger.ObserveGoalOutcome(context.Background(), goal.Outcome{
		Kind: goal.OutcomeFailed, SessionID: "gw_old", Reason: "switch_budget_exhausted",
	}); err != nil {
		t.Fatal(err)
	}
	_, upgraded, ok := trigger.Reserve("gw_old", TriggerSignal{})
	if !ok || upgraded.Kind != SignalGoalFailed {
		t.Fatalf("higher-severity signal must override lease: %+v ok=%v", upgraded, ok)
	}

	now = now.Add(6 * time.Minute)
	if _, _, ok := trigger.Reserve("gw_old", pressure); !ok {
		t.Fatal("expired reservation and lease must allow a new signal")
	}
}

func TestMemoryHandoffTrigger_AbortRestoresSignal(t *testing.T) {
	trigger := NewMemoryHandoffTrigger(time.Minute)
	trigger.Observe("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "failed"})
	reservationID, _, ok := trigger.Reserve("gw_old", TriggerSignal{})
	if !ok {
		t.Fatal("expected reservation")
	}
	if _, _, ok := trigger.Reserve("gw_old", TriggerSignal{}); ok {
		t.Fatal("concurrent reservation must be blocked")
	}
	trigger.Abort(reservationID)
	if _, signal, ok := trigger.Reserve("gw_old", TriggerSignal{}); !ok || signal.Reason != "failed" {
		t.Fatalf("aborted signal was lost: signal=%+v ok=%v", signal, ok)
	}
}

func TestMemoryHandoffTrigger_ModelSwitchDoesNotTrigger(t *testing.T) {
	trigger := NewMemoryHandoffTrigger(time.Minute)
	if err := trigger.ObserveGoalOutcome(context.Background(), goal.Outcome{
		Kind: goal.OutcomeDegraded, SessionID: "gw_old", Reason: "model_switched",
	}); err != nil {
		t.Fatal(err)
	}
	if _, signal, ok := trigger.Reserve("gw_old", TriggerSignal{}); ok {
		t.Fatalf("successful Goal model switch must not trigger handoff: %+v", signal)
	}
}

func TestMemoryHandoffTrigger_BindPeekAckDeepCopiesAndChecksTenant(t *testing.T) {
	trigger := NewMemoryHandoffTrigger(time.Minute)
	state := &GoalState{Version: 1, TenantID: "tenant-a", CompletedSteps: []string{"one"}}
	trigger.Bind("proposal", state)
	state.CompletedSteps[0] = "mutated"

	if got := trigger.Peek("proposal", "tenant-b"); got != nil {
		t.Fatalf("cross-tenant peek returned state: %+v", got)
	}
	got := trigger.Peek("proposal", "tenant-a")
	if got == nil || got.CompletedSteps[0] != "one" {
		t.Fatalf("bound state was not copied: %+v", got)
	}
	got.CompletedSteps[0] = "changed"
	if again := trigger.Peek("proposal", "tenant-a"); again == nil || again.CompletedSteps[0] != "one" {
		t.Fatalf("peek must return a deep copy: %+v", again)
	}
	trigger.Ack("proposal", "tenant-b")
	if trigger.Peek("proposal", "tenant-a") == nil {
		t.Fatal("cross-tenant ack consumed state")
	}
	trigger.Ack("proposal", "tenant-a")
	if again := trigger.Peek("proposal", "tenant-a"); again != nil {
		t.Fatalf("ack must consume state: %+v", again)
	}
	if !trigger.IsAcknowledged("proposal", "tenant-a") || trigger.IsAcknowledged("proposal", "tenant-b") {
		t.Fatal("acknowledgement tenant scope is incorrect")
	}
}

func TestMemoryHandoffTrigger_FiltersNonGoalSessions(t *testing.T) {
	store := newMemoryGoalStore()
	trigger := NewMemoryHandoffTrigger(time.Minute, store)
	if err := trigger.ObserveGoalOutcome(context.Background(), goal.Outcome{
		Kind: goal.OutcomeFailed, SessionID: "not-a-goal", Reason: "provider_retry_exhausted",
	}); err != nil {
		t.Fatal(err)
	}
	if _, signal, ok := trigger.Reserve("not-a-goal", TriggerSignal{}); ok {
		t.Fatalf("non-Goal session produced a handoff signal: %+v", signal)
	}
}

func TestMemoryHandoffTrigger_RejectsGoalOutcomeTenantMismatch(t *testing.T) {
	store := newMemoryGoalStore()
	store.sessions["gw_old"] = &goal.Session{SessionID: "gw_old", TenantID: "tenant-a"}
	trigger := NewMemoryHandoffTrigger(time.Minute, store)
	if err := trigger.ObserveGoalOutcome(context.Background(), goal.Outcome{
		Kind: goal.OutcomeFailed, SessionID: "gw_old", TenantID: "tenant-b",
	}); err == nil {
		t.Fatal("cross-tenant Goal outcome must be rejected")
	}
}

func TestMemoryHandoffTrigger_CleansExpiredAndBoundsEntries(t *testing.T) {
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	trigger := NewMemoryHandoffTrigger(time.Minute)
	trigger.now = func() time.Time { return now }
	trigger.limit = 3
	trigger.Observe("one", TriggerSignal{Kind: SignalGoalFailed, Severity: 4})
	trigger.Bind("proposal-one", &GoalState{Version: 1, TenantID: "tenant-a"})
	reservationID, _, _ := trigger.Reserve("two", TriggerSignal{Kind: SignalContextPressure, Severity: 2})
	trigger.Commit(reservationID)
	trigger.Bind("proposal-two", &GoalState{Version: 1, TenantID: "tenant-a"})
	if total := trigger.entryCountLocked(); total > trigger.limit {
		t.Fatalf("trigger entries exceed bound: %d > %d", total, trigger.limit)
	}

	now = now.Add(2 * time.Minute)
	trigger.Reserve("cleanup", TriggerSignal{})
	if total := trigger.entryCountLocked(); total != 0 {
		t.Fatalf("expired entries were not cleaned: total=%d", total)
	}
}
