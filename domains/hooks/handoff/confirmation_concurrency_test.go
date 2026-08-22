package handoff

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
)

// countingGoalStateSerializer wraps a GoalStateSerializer and atomically counts
// Restore invocations. The audit's "safety net" concern for concurrent
// ConfirmRequest was that Restore is the only de-duplication primitive — this
// wrapper exposes the call count under -race without altering the
// idempotent-by-fingerprint semantics of the underlying serializer.
type countingGoalStateSerializer struct {
	delegate GoalStateSerializer
	calls    atomic.Int64
}

func (s *countingGoalStateSerializer) Serialize(ctx context.Context, input GoalStateInput) (*GoalState, error) {
	return s.delegate.Serialize(ctx, input)
}

func (s *countingGoalStateSerializer) Restore(ctx context.Context, newSessionID string, state *GoalState) error {
	s.calls.Add(1)
	return s.delegate.Restore(ctx, newSessionID, state)
}

// TestConfirmRequest_ConcurrentSameIdempotencyKey exercises the audit
// follow-up for 75c714b67 row F: under concurrent ConfirmRequest with the same
// idempotency key, MemoryConfirmationStore.Confirm's mutex + idempotency hash
// guarantee FirstConfirmation=true exactly once. MemoryGoalStateSerializer.Restore
// may be invoked multiple times (each successful Confirm — including replays —
// calls restoreGoalState), but the final target-session state must converge
// and the runtime budgets from the source session must not leak. This test
// pins the existing "serializer idempotency is the only safety net" guarantee
// so future changes cannot silently regress concurrent restore behavior.
func TestConfirmRequest_ConcurrentSameIdempotencyKey(t *testing.T) {
	t.Parallel()

	const goroutines = 32

	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID:        "gw_old",
		TenantID:         "tenant-a",
		State:            goal.StateActive,
		OriginalGoal:     "complete P0-D",
		RetryCount:       2,
		DecisionCount:    1,
		RepeatCount:      3,
		ModelSwitchCount: 1,
		CurrentModel:     "auto",
	}
	serializer := &countingGoalStateSerializer{delegate: NewMemoryGoalStateSerializer(goalStore)}
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	store := &goalConfirmationStore{
		memoryStore:             &memoryStore{},
		MemoryConfirmationStore: NewMemoryConfirmationStore(),
	}
	hook := NewTriggerHook(TriggerConfig{
		Enabled:             true,
		TriggerMode:         TriggerModeManual,
		SkillName:           "handoff",
		SummaryEngine:       SummaryRule,
		MaxPerSession:       5,
		GoalStateSerializer: serializer,
		GoalTrigger:         trigger,
		ContextMonitor:      NewMemoryContextMonitor(0, 0),
		MessageBuilder:      NewMemoryHandoffMessageBuilder(0),
	}, store)

	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", ClientModel: "auto",
		Body:         []byte(`{"messages":[{"role":"user","content":"/handoff continue P0-D"}]}`),
		MessageCount: 1,
	})
	if err != nil || requestResult == nil || !requestResult.Triggered {
		t.Fatalf("prepare failed: result=%+v err=%v", requestResult, err)
	}
	proposal, token, err := hook.PrepareConfirmation(context.Background(), requestResult, 42)
	if err != nil {
		t.Fatalf("prepare confirmation: %v", err)
	}
	input := ConfirmationInput{
		ProposalID:      proposal.ID,
		TenantID:        "tenant-a",
		APIKeyID:        42,
		Token:           token,
		NewSessionID:    "gw_new",
		TargetCreatedAt: time.Now().UTC(),
		IdempotencyKey:  "concurrent-restore-once-key",
	}

	type outcome struct {
		result *ConfirmationResult
		err    error
	}
	results := make([]outcome, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			r, err := hook.ConfirmRequest(context.Background(), input)
			results[idx] = outcome{result: r, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	var firstConfirm, replay int
	var retryable, conflict, other int
	for _, o := range results {
		if o.result == nil {
			other++
			continue
		}
		if o.result.FirstConfirmation {
			firstConfirm++
		} else {
			replay++
		}
		switch {
		case errors.Is(o.err, ErrGoalRestoreRetryable):
			retryable++
		case errors.Is(o.err, ErrGoalRestoreConflict):
			conflict++
		case o.err != nil:
			other++
		}
	}
	if firstConfirm != 1 {
		t.Fatalf("FirstConfirmation count = %d, want 1 (goroutines=%d)", firstConfirm, goroutines)
	}
	if replay != goroutines-1 {
		t.Fatalf("replay count = %d, want %d", replay, goroutines-1)
	}
	if retryable != 0 {
		t.Fatalf("retryable restore errors = %d, want 0", retryable)
	}
	if conflict != 0 {
		t.Fatalf("restore conflict errors = %d, want 0 (concurrent Restore must converge without ErrGoalRestoreConflict)", conflict)
	}
	if other != 0 {
		t.Fatalf("unexpected non-nil errors observed: %d", other)
	}
	if got := serializer.calls.Load(); got == 0 {
		t.Fatalf("Restore was never invoked under %d concurrent goroutines", goroutines)
	}
	for i, o := range results {
		if o.result.NewSessionID != "gw_new" {
			t.Fatalf("goroutine %d: NewSessionID=%q want gw_new", i, o.result.NewSessionID)
		}
		if o.result.ConfirmedAt.IsZero() {
			t.Fatalf("goroutine %d: ConfirmedAt is zero", i)
		}
	}

	restored, getErr := goalStore.GetSession(context.Background(), "tenant-a", "gw_new")
	if getErr != nil || restored == nil {
		t.Fatalf("target session missing after concurrent confirm: err=%v session=%+v", getErr, restored)
	}
	if restored.OriginalGoal != "complete P0-D" {
		t.Fatalf("OriginalGoal not restored: %q", restored.OriginalGoal)
	}
	if restored.RetryCount != 0 || restored.DecisionCount != 0 || restored.RepeatCount != 0 || restored.ModelSwitchCount != 0 {
		t.Fatalf("exhausted runtime budgets leaked into restored session: %+v", restored)
	}
}
