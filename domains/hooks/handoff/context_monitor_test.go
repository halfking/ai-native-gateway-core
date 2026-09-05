package handoff

import (
	"testing"
	"time"
)

func TestMemoryContextMonitor_EvaluatePriorityAndThresholds(t *testing.T) {
	now := time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC)
	monitor := NewMemoryContextMonitor(100, 0.8)
	monitor.now = func() time.Time { return now }

	tests := []struct {
		name     string
		snapshot ContextSnapshot
		wantKind SignalKind
		wantWhy  string
	}{
		{name: "none", snapshot: ContextSnapshot{TokensUsed: 79, ContextWindow: 100}, wantKind: SignalNone},
		{name: "absolute", snapshot: ContextSnapshot{TokensUsed: 100}, wantKind: SignalContextPressure, wantWhy: "absolute_threshold"},
		{name: "percentage", snapshot: ContextSnapshot{TokensUsed: 80, ContextWindow: 100}, wantKind: SignalContextPressure, wantWhy: "percentage_threshold"},
		{name: "zero context", snapshot: ContextSnapshot{TokensUsed: 80, ContextWindow: 0}, wantKind: SignalNone},
		{name: "completed", snapshot: ContextSnapshot{Outcome: SignalGoalCompleted, Reason: "structured_status"}, wantKind: SignalGoalCompleted, wantWhy: "structured_status"},
		{name: "degraded beats pressure", snapshot: ContextSnapshot{TokensUsed: 100, Outcome: SignalGoalDegraded, Reason: "model_switched"}, wantKind: SignalGoalDegraded, wantWhy: "model_switched"},
		{name: "failed beats all", snapshot: ContextSnapshot{TokensUsed: 100, Outcome: SignalGoalFailed, Reason: "switch_budget_exhausted"}, wantKind: SignalGoalFailed, wantWhy: "switch_budget_exhausted"},
		{name: "retry exhausted", snapshot: ContextSnapshot{RetryExhausted: true}, wantKind: SignalGoalFailed, wantWhy: "provider_retry_exhausted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := monitor.Evaluate(test.snapshot)
			if got.Kind != test.wantKind || got.Reason != test.wantWhy {
				t.Fatalf("Evaluate() = kind %q reason %q, want %q %q", got.Kind, got.Reason, test.wantKind, test.wantWhy)
			}
			if got.ObservedAt != now {
				t.Fatalf("ObservedAt = %v, want %v", got.ObservedAt, now)
			}
		})
	}
}
