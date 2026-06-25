package armor

import (
	"context"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// TDD: Logger tests (B1-4)
// ──────────────────────────────────────────────────────────────────────────────

// TestLogger_NilPool verifies Logger no-ops when pool is nil.
func TestLogger_NilPool(t *testing.T) {
	l := NewLogger(nil)
	l.Log(context.Background(), Judgment{
		RequestID: "req-1",
		TenantID:  "acme",
		CheckType: "prompt_inject",
		Decision:  DecisionWarn,
		Score:     0.8,
		Threshold: 0.7,
		CreatedAt: time.Now(),
	})
	// Should not panic, no error logged (no-op)
}

// TestLogger_EmptyTenant verifies Logger skips insert when tenant is empty.
// We can't easily fake pgxpool without real DB, so this test only verifies
// the early-return logic doesn't panic.
func TestLogger_EmptyTenant(t *testing.T) {
	l := NewLogger(nil) // nil pool = no-op

	l.Log(context.Background(), Judgment{
		RequestID: "req-2",
		TenantID:  "", // empty
		CheckType: "prompt_inject",
		Decision:  DecisionSafe,
		CreatedAt: time.Now(),
	})
	// Should not panic (logs a warning, then returns early)
}

// TestLogger_DecisionMarshal verifies Decision.String() output for audit.
func TestLogger_DecisionMarshal(t *testing.T) {
	tests := []struct {
		decision Decision
		want     string
	}{
		{DecisionSafe, "safe"},
		{DecisionWarn, "warn"},
		{DecisionBlock, "block"},
		{Decision(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.decision.String()
		if got != tt.want {
			t.Errorf("Decision(%d).String() = %q, want %q", tt.decision, got, tt.want)
		}
	}
}

// TestJudgment_Fields verifies Judgment struct can hold all expected fields.
func TestJudgment_Fields(t *testing.T) {
	now := time.Now()
	patternID := "base64_spam"

	j := Judgment{
		RequestID:    "req-3",
		TenantID:     "acme",
		CheckType:    "prompt_inject",
		Decision:     DecisionWarn,
		Source:       "judge",
		PatternIDs:   []string{patternID},
		JudgeModel:   "gpt-4o-mini",
		Score:        0.85,
		Threshold:    0.7,
		Mode:         ModeObserve,
		LatencyMS:    120,
		PromptSHA256: "abc123def456",
		Snippet:      "ignore previous instructions",
		Reason:       "prompt injection detected",
		CreatedAt:    now,
	}

	if j.RequestID != "req-3" {
		t.Errorf("RequestID: want req-3, got %s", j.RequestID)
	}
	if j.TenantID != "acme" {
		t.Errorf("TenantID: want acme, got %s", j.TenantID)
	}
	if j.Decision != DecisionWarn {
		t.Errorf("Decision: want warn, got %s", j.Decision.String())
	}
	if j.Source != "judge" {
		t.Errorf("Source: want judge, got %s", j.Source)
	}
	if j.Score != 0.85 {
		t.Errorf("Score: want 0.85, got %.2f", j.Score)
	}
	if len(j.PatternIDs) != 1 || j.PatternIDs[0] != "base64_spam" {
		t.Errorf("PatternIDs: want [base64_spam], got %v", j.PatternIDs)
	}
	if j.Reason != "prompt injection detected" {
		t.Errorf("Reason: want 'prompt injection detected', got %s", j.Reason)
	}
	if !j.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt: want %v, got %v", now, j.CreatedAt)
	}
}
