package shadow

import (
	"testing"
)

// Note: the plan-spec test originally imported
// "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api" without using
// any of its symbols. Go rejects unused imports, so the import has been
// removed; the test logic is unchanged.

func TestAvailabilityMismatch(t *testing.T) {
	d := Compute("req1", "t1", "gpt", []string{"a", "b"}, []string{"b", "c"})
	if !d.HasAvailabilityMismatch() {
		t.Fatalf("availability sets must mismatch")
	}
}

func TestOrderMismatch(t *testing.T) {
	d := Compute("req2", "t1", "gpt", []string{"a", "b"}, []string{"b", "a"})
	if !d.HasOrderMismatch() {
		t.Fatalf("order must mismatch")
	}
	if d.HasAvailabilityMismatch() {
		t.Fatalf("availability must match")
	}
}

func TestOutcomeAndCapturedSlices(t *testing.T) {
	oldOrder := []string{"a", "b"}
	newOrder := []string{"a", "b"}
	d := Compute("req", "tenant", "model", oldOrder, newOrder)
	oldOrder[0] = "changed"
	newOrder[0] = "changed"

	if got := d.Outcome(); got != OutcomeIdentical {
		t.Fatalf("Outcome()=%q, want %q", got, OutcomeIdentical)
	}
	result := d.Result()
	if result.OldOrderedIDs[0] != "a" || result.NewOrderedIDs[0] != "a" {
		t.Fatalf("Compute must capture defensive copies: %+v", result)
	}
	result.OldOrderedIDs[0] = "mutated"
	if d.Result().OldOrderedIDs[0] != "a" {
		t.Fatal("Result must return defensive copies")
	}
}
