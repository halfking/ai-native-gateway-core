package orchestration

import (
	"errors"
	"testing"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestSchedulerEnforcesSingleActiveSuccessor(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{LeaseTTL: time.Minute})
	first := Action{ID: "a1", RunID: "run-1", Sequence: 1, BindingID: "b1"}
	if err := scheduler.Enqueue(first); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	if err := scheduler.Enqueue(Action{ID: "a2", RunID: "run-1", Sequence: 2, BindingID: "b1"}); !errors.Is(err, ErrRunHasActiveAction) {
		t.Fatalf("second Enqueue() error = %v, want ErrRunHasActiveAction", err)
	}
	claim, err := scheduler.Claim("run-1", "worker-1")
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := scheduler.Complete(claim.LeaseID, nil); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := scheduler.Enqueue(Action{ID: "a2", RunID: "run-1", Sequence: 2, BindingID: "b1"}); err != nil {
		t.Fatalf("successor Enqueue() error = %v", err)
	}
}

func TestSchedulerLeaseExpiryMakesActionClaimableAgain(t *testing.T) {
	now := time.Unix(100, 0)
	scheduler := NewScheduler(SchedulerConfig{LeaseTTL: time.Second, Now: func() time.Time { return now }})
	if err := scheduler.Enqueue(Action{ID: "a1", RunID: "run-1", Sequence: 1, BindingID: "b1"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	first, err := scheduler.Claim("run-1", "worker-1")
	if err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	now = now.Add(2 * time.Second)
	second, err := scheduler.Claim("run-1", "worker-2")
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second.LeaseID == first.LeaseID || second.Action.WorkerID != "worker-2" {
		t.Fatalf("reclaimed lease = %+v, first = %+v", second, first)
	}
}

func TestSchedulerFailurePolicyDeterminesTerminalState(t *testing.T) {
	tests := []struct {
		name   string
		policy pluginruntime.FailurePolicy
		want   ActionState
	}{
		{name: "closed", policy: pluginruntime.FailureClosed, want: ActionFailed},
		{name: "suspend", policy: pluginruntime.FailureSuspend, want: ActionSuspended},
		{name: "dlq", policy: pluginruntime.FailureRetryDLQ, want: ActionDeadLetter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduler := NewScheduler(SchedulerConfig{LeaseTTL: time.Minute})
			if err := scheduler.Enqueue(Action{ID: "a1", RunID: tt.name, Sequence: 1, BindingID: "b1", FailurePolicy: tt.policy}); err != nil {
				t.Fatalf("Enqueue() error = %v", err)
			}
			claim, err := scheduler.Claim(tt.name, "worker-1")
			if err != nil {
				t.Fatalf("Claim() error = %v", err)
			}
			if err := scheduler.Complete(claim.LeaseID, errors.New("plugin failed")); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			got, ok := scheduler.Get("a1")
			if !ok || got.State != tt.want {
				t.Fatalf("state = %v, want %v", got.State, tt.want)
			}
		})
	}
}

func TestSchedulerRejectsStaleLeaseCompletion(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{LeaseTTL: time.Second})
	if err := scheduler.Enqueue(Action{ID: "a1", RunID: "run-1", Sequence: 1, BindingID: "b1"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claim, err := scheduler.Claim("run-1", "worker-1")
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := scheduler.Complete(claim.LeaseID+"-stale", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Complete() error = %v, want ErrLeaseLost", err)
	}
	if err := scheduler.Complete(claim.LeaseID, nil); err != nil {
		t.Fatalf("valid Complete() error = %v", err)
	}
}

func TestSchedulerEnforcesBindingConcurrency(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{
		LeaseTTL:           time.Minute,
		BindingConcurrency: map[string]int{"b1": 1},
	})
	if err := scheduler.Enqueue(Action{ID: "a1", RunID: "run-1", Sequence: 1, BindingID: "b1"}); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	if err := scheduler.Enqueue(Action{ID: "a2", RunID: "run-2", Sequence: 1, BindingID: "b1"}); err != nil {
		t.Fatalf("second Enqueue() error = %v", err)
	}
	if _, err := scheduler.Claim("run-1", "worker-1"); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	if _, err := scheduler.Claim("run-2", "worker-2"); !errors.Is(err, ErrNoActionAvailable) {
		t.Fatalf("second Claim() error = %v, want ErrNoActionAvailable", err)
	}
}
