package dispatch

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestContextBudgetExceededTriggersModelChange verifies that when a request
// fails with context_budget_exceeded (gateway-side bucket exhaustion), the
// failover ladder escalates to model-change instead of terminal failure.
//
// This is the F7 fix (2026-11-19): auto mode selecting a small-window model
// (e.g., glm-5.2) for a large-context request should switch to a larger-window
// model (e.g., glm-5.3), not fail immediately.
func TestContextBudgetExceededTriggersModelChange(t *testing.T) {
	// Simulate a context_budget_exceeded error from executor
	// Use error message pattern to avoid import cycle
	budgetErr := errors.New("context_budget_exceeded: model glm-5.2: request tokens exceed bucket budget")

	// Classify the error
	kind := errorsx.ClassifyError(budgetErr, nil)
	if kind != errorsx.KindContextBudgetExceeded {
		t.Fatalf("ClassifyError(ContextBudgetError) = %v, want KindContextBudgetExceeded", kind)
	}

	// Verify it doesn't penalize the node
	penalty := errorsx.PenaltyOf(kind)
	if penalty.Target != errorsx.PenaltyNone {
		t.Errorf("PenaltyOf(KindContextBudgetExceeded) = %v, want PenaltyNone", penalty.Target)
	}

	// Verify PlanAfterFailure doesn't return NextActionFailed immediately
	qr := &QueuedRequest{
		ID:             "test-request",
		AttemptCount:   1,
		CredRetryCount: 0,
		SelectedCred: CredentialRef{
			CredentialID: 123,
			ProviderID:   32,
			Vendor:       "zhipu",
		},
	}

	out := ForwardOutcome{
		Err:       budgetErr,
		ErrorKind: string(kind),
	}

	decision := PlanAfterFailure(qr, out, Config{})

	// Should NOT be NextActionFailed (which would prevent model-change)
	if decision.Action == NextActionFailed {
		t.Errorf("PlanAfterFailure returned NextActionFailed for context_budget_exceeded, should allow continuation to model-change")
	}

	// The decision should allow fallthrough to credential switch / model change
	// (i.e., Action should be empty or NextActionSwitchCred, not Failed)
	if decision.Action != "" && decision.Action != NextActionSwitchCred {
		t.Logf("PlanAfterFailure decision: Action=%v, Reason=%s", decision.Action, decision.Reason)
	}
}

// TestContextLengthStillTerminalInPlanner verifies that upstream
// context_length_exceeded (400/422) remains terminal and does NOT trigger
// model-change. This ensures we didn't break the existing behavior.
func TestContextLengthStillTerminalInPlanner(t *testing.T) {
	// Simulate an upstream context_length error
	upstreamErr := errors.New("This model's maximum context length is 8192 tokens.")

	kind := errorsx.ClassifyError(upstreamErr, nil)
	if kind != errorsx.KindContextLength {
		t.Fatalf("ClassifyError(upstream context error) = %v, want KindContextLength", kind)
	}

	// Verify PlanAfterFailure returns NextActionFailed
	qr := &QueuedRequest{
		ID:             "test-request",
		AttemptCount:   1,
		CredRetryCount: 0,
		SelectedCred: CredentialRef{
			CredentialID: 456,
			ProviderID:   18,
			Vendor:       "openai",
		},
	}

	out := ForwardOutcome{
		Err:       upstreamErr,
		ErrorKind: string(kind),
	}

	decision := PlanAfterFailure(qr, out, Config{})

	// Should be NextActionFailed (terminal)
	if decision.Action != NextActionFailed {
		t.Errorf("PlanAfterFailure(KindContextLength) = %v, want NextActionFailed (terminal)", decision.Action)
	}
}

// TestIsCentralPolicyKindIncludesContextBudgetExceeded verifies that
// KindContextBudgetExceeded is registered in isCentralPolicyKind, so it
// won't be rewritten to KindTransient (which would cause node punishment).
func TestIsCentralPolicyKindIncludesContextBudgetExceeded(t *testing.T) {
	if !isCentralPolicyKind(errorsx.KindContextBudgetExceeded) {
		t.Errorf("KindContextBudgetExceeded not in isCentralPolicyKind, will be rewritten to KindTransient")
	}
}
