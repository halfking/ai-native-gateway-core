package errorsx

import (
	"errors"
	"testing"
)

// TestContextBudgetExceededClassification verifies that ContextBudgetError
// is classified as KindContextBudgetExceeded, not KindContextLength.
func TestContextBudgetExceededClassification(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind ErrorKind
	}{
		{
			name:     "context_budget_exceeded prefix",
			err:      errors.New("context_budget_exceeded: model glm-5.2: request exceeds bucket budget"),
			wantKind: KindContextBudgetExceeded,
		},
		{
			name:     "context_length_exceeded (upstream)",
			err:      errors.New("This model's maximum context length is 8192 tokens."),
			wantKind: KindContextLength,
		},
		{
			name:     "wrapped context_budget_exceeded",
			err:      errors.New("execution failed: context_budget_exceeded: model glm-5.2: tokens exceed limit"),
			wantKind: KindContextBudgetExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, nil)
			if got != tt.wantKind {
				t.Errorf("ClassifyError(%v) = %v, want %v", tt.err, got, tt.wantKind)
			}
		})
	}
}

// TestContextBudgetExceededPenalty verifies that KindContextBudgetExceeded
// does not penalize the node (PenaltyNone).
func TestContextBudgetExceededPenalty(t *testing.T) {
	got := PenaltyOf(KindContextBudgetExceeded)
	if got.Target != PenaltyNone {
		t.Errorf("PenaltyOf(KindContextBudgetExceeded) = %v, want PenaltyNone", got.Target)
	}
}

// TestContextBudgetExceededNotTerminal verifies that KindContextBudgetExceeded
// is NOT in terminalActionKind (should allow model-change).
func TestContextBudgetExceededNotTerminal(t *testing.T) {
	if terminalActionKind(KindContextBudgetExceeded) {
		t.Errorf("terminalActionKind(KindContextBudgetExceeded) = true, want false (should allow model-change)")
	}
}

// TestContextLengthStillTerminal verifies that KindContextLength remains
// terminal (upstream 400/422 should not trigger model-change).
func TestContextLengthStillTerminal(t *testing.T) {
	if !terminalActionKind(KindContextLength) {
		t.Errorf("terminalActionKind(KindContextLength) = false, want true (upstream error is terminal)")
	}
}

// TestContextBudgetErrorDetection verifies the isContextBudgetError helper.
func TestContextBudgetErrorDetection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "context_budget_exceeded error",
			err:  errors.New("context_budget_exceeded: model glm-5.2: request exceeds bucket"),
			want: true,
		},
		{
			name: "context_length_exceeded error (upstream)",
			err:  errors.New("context length exceeded"),
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("network timeout"),
			want: false,
		},
		{
			name: "wrapped context_budget_exceeded",
			err:  errors.New("failed to execute: context_budget_exceeded: tokens exceed limit"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextBudgetError(tt.err)
			if got != tt.want {
				t.Errorf("isContextBudgetError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
