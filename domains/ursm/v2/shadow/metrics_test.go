package shadow

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestShadowDiffMetricHasStableLabels(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	want := map[string]bool{
		string(OutcomeIdentical):            false,
		string(OutcomeAvailabilityMismatch): false,
		string(OutcomeOrderMismatch):        false,
		string(OutcomeTop1Mismatch):         false,
		string(OutcomeNotReady):             false,
		string(OutcomeError):                false,
		string(OutcomeSampledOut):           false,
		string(OutcomeDropped):              false,
	}
	for _, family := range families {
		if family.GetName() != "ursm_shadow_diff_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "type" {
					if _, ok := want[label.GetValue()]; ok {
						want[label.GetValue()] = true
					}
				}
			}
		}
	}
	for label, found := range want {
		if !found {
			t.Errorf("ursm_shadow_diff_total missing type=%q", label)
		}
	}
}

// TestShadowEnqueueTotal_ResultLabels (2026-08-31, P2-3 observability) pins
// that the queue-capacity drop counter is registered with a stable set of
// label values and that RecordEnqueueResult ignores unknown results to
// bound Prometheus cardinality.
func TestShadowEnqueueTotal_ResultLabels(t *testing.T) {
	readCounter := func() float64 {
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather metrics: %v", err)
		}
		var total float64
		for _, family := range families {
			if family.GetName() != "ursm_shadow_enqueued_total" {
				continue
			}
			for _, metric := range family.Metric {
				m := &dto.Metric{}
				if err := metric.Write(m); err == nil {
					total += m.GetCounter().GetValue()
				}
			}
		}
		return total
	}

	// Snapshot then increment, expecting the counter to move.
	before := readCounter()
	RecordEnqueueResult("queue_full")
	RecordEnqueueResult("enqueued")
	RecordEnqueueResult("sampled_out")
	RecordEnqueueResult("worker_stopped")
	// Unknown results must be ignored, not silently created as new series.
	RecordEnqueueResult("garbage_unknown_label")
	after := readCounter()
	if after-before < 4 {
		t.Fatalf("expected at least 4 increments, got %v -> %v", before, after)
	}

	// Confirm all four expected label values are present.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	want := map[string]bool{
		"enqueued":      false,
		"sampled_out":   false,
		"queue_full":    false,
		"worker_stopped": false,
	}
	for _, family := range families {
		if family.GetName() != "ursm_shadow_enqueued_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "result" {
					if _, ok := want[label.GetValue()]; ok {
						want[label.GetValue()] = true
					}
				}
			}
		}
	}
	for label, found := range want {
		if !found {
			t.Errorf("ursm_shadow_enqueued_total missing result=%q", label)
		}
	}
}
