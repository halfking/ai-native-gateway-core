package shadow

import (
	"testing"
)

// Note: the plan-spec test originally imported
// "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api" without using
// any of its symbols. Go rejects unused imports, so the import has been
// removed; the test logic is unchanged.

func TestAvailabilityMismatch(t *testing.T) {
	d := Diff("req1", "t1", "gpt", []string{"a", "b"}, []string{"b", "c"})
	if !d.HasAvailabilityMismatch() {
		t.Fatalf("availability sets must mismatch")
	}
}

func TestOrderMismatch(t *testing.T) {
	d := Diff("req2", "t1", "gpt", []string{"a", "b"}, []string{"b", "a"})
	if !d.HasOrderMismatch() {
		t.Fatalf("order must mismatch")
	}
	if d.HasAvailabilityMismatch() {
		t.Fatalf("availability must match")
	}
}
