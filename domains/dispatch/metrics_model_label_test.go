package dispatch

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestDispatchModelQueueMetricsNoModelLabel is the regression pin for
// Stage C.3. It scans every dispatch_model_queue_* series in the
// default Prometheus registry and asserts no series carries a `model`
// label (it used to be the dimension key; now the metric is
// aggregated across all models).
//
// Drop this test if the decision is reversed — it pins a coordinate
// break with dashboards/alerts, see
// docs/04-implementation/stage-C-model-label-removal.md.
func TestDispatchModelQueueMetricsNoModelLabel(t *testing.T) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	// Gather does not report Vec children that have never been observed.
	// Force a single observation on each metric so the series exists for
	// the scan below.
	metricModelQueueDepth.WithLabelValues().Inc()
	defer metricModelQueueDepth.WithLabelValues().Dec()
	metricModelQueueDepth.WithLabelValues().Inc() // restore to +1
	metricModelQueueWait.WithLabelValues().Observe(0)

	for _, mf := range mfs {
		name := mf.GetName()
		if !strings.HasPrefix(name, "dispatch_model_queue_") {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "model" {
					t.Errorf("%s still carries a 'model' label (value=%q) — Stage C.3 regression",
						name, lp.GetValue())
				}
			}
		}
	}
}

// TestDispatchModelQueueMetricsVariableLabelsEmpty complements the
// series-level check above with a Desc-level check: the metric must
// declare zero variableLabels (the `model` label used to be the only
// one). This is the harder-to-bypass invariant — adding a label back
// would also have to register a new vec, which the allowlist test
// would then catch.
func TestDispatchModelQueueMetricsVariableLabelsEmpty(t *testing.T) {
	ch := make(chan *prometheus.Desc, 32)
	go func() {
		reg := prometheus.DefaultRegisterer
		if r, ok := reg.(*prometheus.Registry); ok {
			r.Describe(ch)
		}
		close(ch)
	}()
	for d := range ch {
		s := d.String()
		if !strings.Contains(s, `fqName: "dispatch_model_queue_depth"`) &&
			!strings.Contains(s, `fqName: "dispatch_model_queue_wait_seconds"`) {
			continue
		}
		// Extract variableLabels. With zero labels, Desc.String()
		// emits `variableLabels: {}` (no space, no content).
		if !strings.Contains(s, "variableLabels: {}") {
			t.Errorf("dispatch_model_queue_* Desc must declare zero variableLabels; got: %s", s)
		}
		if strings.Contains(s, "variableLabels: {model}") {
			t.Errorf("dispatch_model_queue_* Desc must NOT declare 'model' label; got: %s", s)
		}
	}
}
