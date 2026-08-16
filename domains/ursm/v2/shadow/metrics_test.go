package shadow

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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
		string(OutcomeNotReady):             false,
		string(OutcomeError):                false,
		string(OutcomeSampledOut):           false,
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
