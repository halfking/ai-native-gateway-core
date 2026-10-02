package settings

import "testing"

// oneMillionContext mirrors domains/hooks/handoff/handoff_1m_contract_test.go.
// The behavioural half of the 1M handoff contract lives there; this file pins
// the settings-side bound, so the two together fail loudly if either the
// shipped default or the accepted range moves out from under 1M.
const oneMillionContext = 1_000_000

func handoffAbsoluteThresholdSpec(t *testing.T) *Spec {
	t.Helper()
	for _, s := range HandoffSpecs() {
		if s.Key == "handoff.absolute_threshold" {
			spec := s
			return &spec
		}
	}
	t.Fatal("handoff.absolute_threshold missing from HandoffSpecs()")
	return nil
}

// TestHandoffAbsoluteThreshold_DefaultCoversOneMillion pins the shipped
// default strictly below 1M. If a future raise pushes it to or above 1M, a
// 1M-token session would stop handing off and the "1M handoff contract"
// would silently disappear — this gate exists to make that loud.
func TestHandoffAbsoluteThreshold_DefaultCoversOneMillion(t *testing.T) {
	spec := handoffAbsoluteThresholdSpec(t)

	def, ok := spec.Default.(int)
	if !ok {
		t.Fatalf("handoff.absolute_threshold Default must be an int, got %T (%v)", spec.Default, spec.Default)
	}
	if def >= oneMillionContext {
		t.Fatalf("handoff.absolute_threshold Default=%d must stay below %d, otherwise a 1M-token "+
			"context no longer triggers the absolute backstop and the 1M handoff contract is void",
			def, oneMillionContext)
	}
	if spec.Min == nil {
		t.Fatal("handoff.absolute_threshold must declare Min")
	}
	if int(*spec.Min) > def {
		t.Fatalf("Default=%d violates the spec Min=%v", def, *spec.Min)
	}
	if spec.Max != nil && def > int(*spec.Max) {
		t.Fatalf("Default=%d exceeds spec Max=%v", def, *spec.Max)
	}
}

// TestHandoffAbsoluteThreshold_RangeAdmitsOneMillion pins that 1M stays
// expressible as an operator override. Without this, someone could narrow Max
// below 1M and the absolute backstop would become unreachable for exactly the
// oversized contexts it exists to protect.
func TestHandoffAbsoluteThreshold_RangeAdmitsOneMillion(t *testing.T) {
	spec := handoffAbsoluteThresholdSpec(t)

	if spec.Max == nil {
		t.Fatal("handoff.absolute_threshold must declare Max so the 1M override stays bounded")
	}
	if int(*spec.Max) < oneMillionContext {
		t.Fatalf("handoff.absolute_threshold Max=%v is below %d; a 1M-token context could no longer "+
			"be covered by a single tenant threshold", *spec.Max, oneMillionContext)
	}
	if spec.Min == nil {
		t.Fatal("handoff.absolute_threshold must declare Min")
	}
	if int(*spec.Min) > oneMillionContext {
		t.Fatalf("handoff.absolute_threshold Min=%v exceeds %d, the range is inverted", *spec.Min, oneMillionContext)
	}
}
