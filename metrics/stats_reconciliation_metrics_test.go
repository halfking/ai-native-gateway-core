package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func readCounterVec(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := counter.Write(m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestStatsReconciliationMetrics_Runs(t *testing.T) {
	runCompleted := statsReconciliationRuns.WithLabelValues("completed")
	runFailed := statsReconciliationRuns.WithLabelValues("failed")

	beforeCompleted := readCounterVec(t, runCompleted)
	beforeFailed := readCounterVec(t, runFailed)

	RecordStatsReconciliationRun("completed", 1)
	RecordStatsReconciliationRun("failed", 2)

	if got := readCounterVec(t, runCompleted); got != beforeCompleted+1 {
		t.Fatalf("completed count = %v, want %v", got, beforeCompleted+1)
	}
	if got := readCounterVec(t, runFailed); got != beforeFailed+2 {
		t.Fatalf("failed count = %v, want %v", got, beforeFailed+2)
	}
}

func TestStatsReconciliationMetrics_Diffs(t *testing.T) {
	open := statsReconciliationDiffs.WithLabelValues("open")
	repaired := statsReconciliationDiffs.WithLabelValues("auto_repaired")

	beforeOpen := readCounterVec(t, open)
	beforeRepaired := readCounterVec(t, repaired)

	ObserveStatsReconciliationDiffs("open", 5)
	ObserveStatsReconciliationDiffs("auto_repaired", 3)

	if got := readCounterVec(t, open); got != beforeOpen+5 {
		t.Fatalf("open count = %v, want %v", got, beforeOpen+5)
	}
	if got := readCounterVec(t, repaired); got != beforeRepaired+3 {
		t.Fatalf("auto_repaired count = %v, want %v", got, beforeRepaired+3)
	}
}

func TestStatsReconciliationMetrics_Adjustments(t *testing.T) {
	approveCommitted := statsAdjustments.WithLabelValues("approve", "committed")
	rejectFailed := statsAdjustments.WithLabelValues("reject", "failed")

	beforeApproveCommitted := readCounterVec(t, approveCommitted)
	beforeRejectFailed := readCounterVec(t, rejectFailed)

	RecordStatsAdjustment("approve", "committed", 4)
	RecordStatsAdjustment("reject", "failed", 1)

	if got := readCounterVec(t, approveCommitted); got != beforeApproveCommitted+4 {
		t.Fatalf("approve committed count = %v, want %v", got, beforeApproveCommitted+4)
	}
	if got := readCounterVec(t, rejectFailed); got != beforeRejectFailed+1 {
		t.Fatalf("reject failed count = %v, want %v", got, beforeRejectFailed+1)
	}
}

// TestStatsReconciliationMetrics_NoOpOnZero guards the zero-count helper
// contract: callers can freely pass 0 from reconciliation loops without
// polluting the registry with empty label combinations.
func TestStatsReconciliationMetrics_NoOpOnZero(t *testing.T) {
	open := statsReconciliationDiffs.WithLabelValues("open")
	approveCommitted := statsAdjustments.WithLabelValues("approve", "committed")
	completed := statsReconciliationRuns.WithLabelValues("completed")

	beforeOpen := readCounterVec(t, open)
	beforeApproveCommitted := readCounterVec(t, approveCommitted)
	beforeCompleted := readCounterVec(t, completed)

	ObserveStatsReconciliationDiffs("open", 0)
	RecordStatsAdjustment("approve", "committed", 0)
	RecordStatsReconciliationRun("completed", 0)

	if got := readCounterVec(t, open); got != beforeOpen {
		t.Fatalf("open count changed after zero-call: %v -> %v", beforeOpen, got)
	}
	if got := readCounterVec(t, approveCommitted); got != beforeApproveCommitted {
		t.Fatalf("approve committed count changed after zero-call: %v -> %v", beforeApproveCommitted, got)
	}
	if got := readCounterVec(t, completed); got != beforeCompleted {
		t.Fatalf("completed count changed after zero-call: %v -> %v", beforeCompleted, got)
	}
}