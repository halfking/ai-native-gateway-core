package bg

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestZombieLockStreakGaugeLifecycle (2026-08-31, P2-8) pins the
// inc/reset contract on the zombie-lock streak gauge: inc moves the gauge
// up by exactly one per call, reset zeroes it, and the gauge is registered
// under the expected name with the table label only.
//
// Captures a baseline before each operation so the assertions are exact
// deltas; a silent no-op regression in inc/reset (e.g., accidentally
// replaced Inc with Add(0)) would be caught by the equality check rather
// than masked by a lower-bound assertion on a globally-shared gauge.
func TestZombieLockStreakGaugeLifecycle(t *testing.T) {
	const table = "test_partition_zombie"

	readGauge := func() float64 {
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather metrics: %v", err)
		}
		for _, family := range families {
			if family.GetName() != "llm_gateway_hot_table_promote_zombie_lock_streak" {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "table" && label.GetValue() == table {
						if g := metric.GetGauge(); g != nil {
							return g.GetValue()
						}
					}
				}
			}
		}
		return -1
	}

	// Reset to a known baseline first; the gauge is process-global and may
	// have been touched by other tests in the package.
	resetPromoteZombieLockStreak(table)
	baseline := readGauge()
	if baseline != 0 {
		t.Fatalf("baseline gauge = %v, want 0 after reset", baseline)
	}

	incPromoteZombieLockStreak(table)
	if got := readGauge(); got != baseline+1 {
		t.Fatalf("after 1 increment, gauge = %v, want %v", got, baseline+1)
	}
	incPromoteZombieLockStreak(table)
	if got := readGauge(); got != baseline+2 {
		t.Fatalf("after 2 increments, gauge = %v, want %v", got, baseline+2)
	}
	incPromoteZombieLockStreak(table)
	if got := readGauge(); got != baseline+3 {
		t.Fatalf("after 3 increments, gauge = %v, want %v", got, baseline+3)
	}

	resetPromoteZombieLockStreak(table)
	if got := readGauge(); got != 0 {
		t.Fatalf("after reset, gauge = %v, want 0", got)
	}
}

// TestZombieLockStreakGaugeLabelContract (2026-08-31, P2-8) confirms the
// gauge only carries a single "table" label so dashboards do not see
// cardinality explode. Uses a per-test label value that is reset on exit
// so subsequent tests in the package do not see a permanent label value
// on the global gauge.
func TestZombieLockStreakGaugeLabelContract(t *testing.T) {
	const table = "label_contract_test"
	defer resetPromoteZombieLockStreak(table)

	incPromoteZombieLockStreak(table)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "llm_gateway_hot_table_promote_zombie_lock_streak" {
			continue
		}
		for _, metric := range family.GetMetric() {
			hasTable := false
			otherLabelCount := 0
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "table":
					hasTable = true
				default:
					otherLabelCount++
				}
			}
			if !hasTable {
				t.Fatalf("gauge metric missing required table label: %+v", metric.GetLabel())
			}
			if otherLabelCount != 0 {
				t.Fatalf("gauge metric must only carry table label, got extras: %+v", metric.GetLabel())
			}
		}
	}
}