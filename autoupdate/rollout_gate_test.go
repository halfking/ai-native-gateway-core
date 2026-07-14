package autoupdate

import "testing"

func TestEvaluateRolloutGate_NoSamples(t *testing.T) {
	result := EvaluateRolloutGate(RolloutStats{})
	if !result.Allowed {
		t.Fatalf("expected allow with no samples, got %+v", result)
	}
}

func TestEvaluateRolloutGate_SuccessThreshold(t *testing.T) {
	result := EvaluateRolloutGate(RolloutStats{
		Total: 100, SuccessCount: 97, FailedCount: 3,
		SuccessRatePct: 97, RollbackRatePct: 0,
	})
	if result.Allowed {
		t.Fatal("expected gate to block low success rate")
	}
}

func TestEvaluateRolloutGate_RollbackThreshold(t *testing.T) {
	result := EvaluateRolloutGate(RolloutStats{
		Total: 100, SuccessCount: 98, RolledBackCount: 2,
		SuccessRatePct: 98, RollbackRatePct: 2,
	})
	if result.Allowed {
		t.Fatal("expected gate to block high rollback rate")
	}
}

func TestEvaluateRolloutGate_Pass(t *testing.T) {
	result := EvaluateRolloutGate(RolloutStats{
		Total: 100, SuccessCount: 99, FailedCount: 1,
		SuccessRatePct: 99, RollbackRatePct: 0,
	})
	if !result.Allowed {
		t.Fatalf("expected gate pass, got %+v", result)
	}
}
