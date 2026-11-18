package bg

import (
	"testing"
	"time"
)

// TestSystemHealthWorker_LastDefaultsToSuspect ensures a freshly
// constructed worker (no pool, no tick yet) returns "suspect" — the
// homepage H badge should never be blank.
func TestSystemHealthWorker_LastDefaultsToSuspect(t *testing.T) {
	w := NewSystemHealthWorker(nil, nil)
	if got := w.Last().Status; got != "suspect" {
		t.Fatalf("expected default suspect, got %q", got)
	}
}

// TestSystemHealthWorker_StartNoopOnNilPool ensures a nil pool does
// not start the goroutine (so the worker cannot silently swallow
// panics in production with db disabled).
func TestSystemHealthWorker_StartNoopOnNilPool(t *testing.T) {
	w := NewSystemHealthWorker(nil, nil)
	w.Start(neverContext())
	if got := w.Last().Status; got != "suspect" {
		t.Fatalf("expected suspect to persist with no pool, got %q", got)
	}
}

// TestSystemHealthWorker_IntervalConstant matches the spec (30s).
func TestSystemHealthWorker_IntervalConstant(t *testing.T) {
	if SystemHealthInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %v", SystemHealthInterval)
	}
}

type closedCtx struct{}

func (closedCtx) Deadline() (time.Time, bool) { return time.Time{}, true }
func (closedCtx) Done() <-chan struct{}       { ch := make(chan struct{}); close(ch); return ch }
func (closedCtx) Err() error                  { return nil }
func (closedCtx) Value(any) any               { return nil }

func neverContext() closedCtx { return closedCtx{} }

// ──────────────────────────────────────────────────────────────────────────
// W1-G4 (2026-11-19): Circuit Breaker Aggregation Tests
// ──────────────────────────────────────────────────────────────────────────

// TestAggregateCircuitStats_EmptyStats tests that nil is returned for empty stats.
func TestAggregateCircuitStats_EmptyStats(t *testing.T) {
	result := aggregateCircuitStats(nil)
	if result != nil {
		t.Error("expected nil for empty stats")
	}

	result = aggregateCircuitStats([]map[string]any{})
	if result != nil {
		t.Error("expected nil for empty slice")
	}
}

// TestAggregateCircuitStats_AllClosed tests breakers in closed state.
func TestAggregateCircuitStats_AllClosed(t *testing.T) {
	stats := []map[string]any{
		{"state": "closed"},
		{"state": "closed"},
		{"state": "closed"},
	}

	result := aggregateCircuitStats(stats)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.TotalBreakers != 3 {
		t.Errorf("expected 3 total breakers, got %d", result.TotalBreakers)
	}
	if result.OpenCount != 0 {
		t.Errorf("expected 0 open breakers, got %d", result.OpenCount)
	}
	if result.HalfOpenCount != 0 {
		t.Errorf("expected 0 half_open breakers, got %d", result.HalfOpenCount)
	}
	if result.QuarantinedCount != 0 {
		t.Errorf("expected 0 quarantined breakers, got %d", result.QuarantinedCount)
	}
}

// TestAggregateCircuitStats_MixedStates tests breakers in various states.
func TestAggregateCircuitStats_MixedStates(t *testing.T) {
	now := time.Now()
	stats := []map[string]any{
		{"state": "closed"},
		{"state": "open", "last_transition": now.Add(-5 * time.Minute)},
		{"state": "open", "last_transition": now.Add(-3 * time.Minute)},
		{"state": "half_open", "last_transition": now.Add(-2 * time.Minute)},
		{"state": "quarantined", "last_transition": now.Add(-10 * time.Minute)},
	}

	result := aggregateCircuitStats(stats)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.TotalBreakers != 5 {
		t.Errorf("expected 5 total breakers, got %d", result.TotalBreakers)
	}
	if result.OpenCount != 2 {
		t.Errorf("expected 2 open breakers, got %d", result.OpenCount)
	}
	if result.HalfOpenCount != 1 {
		t.Errorf("expected 1 half_open breaker, got %d", result.HalfOpenCount)
	}
	if result.QuarantinedCount != 1 {
		t.Errorf("expected 1 quarantined breaker, got %d", result.QuarantinedCount)
	}

	// Should pick the most recent transition (-2 min)
	if result.LastTransition.IsZero() {
		t.Error("expected non-zero last transition")
	}
	expectedTime := now.Add(-2 * time.Minute)
	if !result.LastTransition.Equal(expectedTime) {
		t.Errorf("expected last transition at %v, got %v", expectedTime, result.LastTransition)
	}
}

// TestAggregateCircuitStats_NoTransitions tests stats without last_transition field.
func TestAggregateCircuitStats_NoTransitions(t *testing.T) {
	stats := []map[string]any{
		{"state": "closed"},
		{"state": "open"},
	}

	result := aggregateCircuitStats(stats)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if !result.LastTransition.IsZero() {
		t.Error("expected zero last transition when no transitions present")
	}
}

// TestAggregateCircuitStats_UnknownStates tests that unknown states are ignored.
func TestAggregateCircuitStats_UnknownStates(t *testing.T) {
	stats := []map[string]any{
		{"state": "closed"},
		{"state": "unknown_state"},
		{"state": "open"},
	}

	result := aggregateCircuitStats(stats)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.TotalBreakers != 3 {
		t.Errorf("expected 3 total breakers, got %d", result.TotalBreakers)
	}
	// Only the "open" state should be counted
	if result.OpenCount != 1 {
		t.Errorf("expected 1 open breaker, got %d", result.OpenCount)
	}
}
