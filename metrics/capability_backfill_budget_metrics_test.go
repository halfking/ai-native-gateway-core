package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCapabilityBackfillBudget_CountersMove pins the recording helpers move
// exactly their own counter (charged vs blocked must not be conflated: they
// are the numerator pair an Owner reads to size the daily budget).
func TestCapabilityBackfillBudget_CountersMove(t *testing.T) {
	chargedBefore := testutil.ToFloat64(CapabilityBackfillProbeChargedTotal)
	blockedBefore := testutil.ToFloat64(CapabilityBackfillProbeBudgetBlockedTotal)

	RecordCapabilityBackfillProbeCharged()
	RecordCapabilityBackfillProbeCharged()

	if got := testutil.ToFloat64(CapabilityBackfillProbeChargedTotal); got != chargedBefore+2 {
		t.Fatalf("charged counter moved %v -> %v, want +2", chargedBefore, got)
	}
	if got := testutil.ToFloat64(CapabilityBackfillProbeBudgetBlockedTotal); got != blockedBefore {
		t.Fatalf("blocked counter must not move on charge, %v -> %v", blockedBefore, got)
	}

	RecordCapabilityBackfillProbeBudgetBlocked()
	if got := testutil.ToFloat64(CapabilityBackfillProbeBudgetBlockedTotal); got != blockedBefore+1 {
		t.Fatalf("blocked counter moved %v -> %v, want +1", blockedBefore, got)
	}
}

// TestCapabilityBackfillBudget_GaugeClampsNegative pins that an unbudgeted
// (<=0) process exposes 0, not a negative value — dashboards compare this
// gauge across processes to surface config drift, and -1 would read as
// "budget of minus one" instead of "no budget".
func TestCapabilityBackfillBudget_GaugeClampsNegative(t *testing.T) {
	SetCapabilityBackfillDailyBudget(-5)
	if got := testutil.ToFloat64(CapabilityBackfillDailyBudget); got != 0 {
		t.Fatalf("negative budget must clamp to 0, got %v", got)
	}
	SetCapabilityBackfillDailyBudget(2400)
	if got := testutil.ToFloat64(CapabilityBackfillDailyBudget); got != 2400 {
		t.Fatalf("budget gauge must reflect the configured value, got %v", got)
	}
}
