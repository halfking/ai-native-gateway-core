// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log)
// Metrics tests. TDD: contract for metrics.go.
//
// Per design §9.2:
//
//	"Recommended metrics: active runs, terminal reason, retry, budget stop,
//	 lease conflict, resume safety block, action duplicate, handoff restore
//	 failure, audit degraded, fallback rejection, pending projection error."
//	"IDs do not enter high-cardinality metric labels."
//
// The Prometheus implementation MUST register against a caller-provided
// prometheus.Registerer (we test with prometheus.NewRegistry) so production
// can wire it into the gateway's existing registry while tests stay hermetic
// and never touch the default global registry.
//
// We deliberately avoid github.com/prometheus/client_golang/prometheus/testutil
// ( not vendored in this repo ) and read counter/gauge values via the standard
// prometheus.Metric.Write(*dto.Metric) hook.
package observ

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readCounter writes c into a dto.Metric and returns its current value.
func readCounter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var pb dto.Metric
	if err := c.Write(&pb); err != nil {
		t.Fatalf("counter write: %v", err)
	}
	return pb.GetCounter().GetValue()
}

// readGauge writes g into a dto.Metric and returns its current value.
func readGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var pb dto.Metric
	if err := g.Write(&pb); err != nil {
		t.Fatalf("gauge write: %v", err)
	}
	return pb.GetGauge().GetValue()
}

// readCounterLabel returns the value of a CounterVec at the given label tuple.
func readCounterLabel(t *testing.T, v *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := v.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get metric with labels %v: %v", labels, err)
	}
	return readCounter(t, c)
}

// TestNoopMetrics_NoPanic verifies the Noop implementation satisfies the
// Metrics contract without panicking on any call.
func TestNoopMetrics_NoPanic(t *testing.T) {
	n := NewNoopMetrics()
	n.SetActiveRuns(5)
	n.IncTerminalReason("manual_required")
	n.IncRetry()
	n.IncBudgetStop()
	n.IncLeaseConflict()
	n.IncResumeSafetyBlock()
	n.IncActionDuplicate()
	n.IncHandoffRestoreFailure("lease_lost")
	n.IncAuditDegraded()
	n.IncFallbackRejection("model_unavailable")
	n.IncPendingProjectionError("decrypt")
}

// TestPrometheusMetrics_CountersAndGauge verifies counter increments and
// gauge set on a private registry. IDs must NEVER appear as labels.
func TestPrometheusMetrics_CountersAndGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPrometheusMetrics(reg)

	m.SetActiveRuns(7)
	if got := readGauge(t, m.activeRuns); got != 7 {
		t.Errorf("activeRuns gauge=%v want 7", got)
	}

	m.IncRetry()
	m.IncRetry()
	m.IncBudgetStop()
	m.IncLeaseConflict()
	m.IncResumeSafetyBlock()
	m.IncActionDuplicate()
	m.IncAuditDegraded()

	if got := readCounter(t, m.retry); got != 2 {
		t.Errorf("retry=%v want 2", got)
	}
	if got := readCounter(t, m.budgetStop); got != 1 {
		t.Errorf("budgetStop=%v want 1", got)
	}
	if got := readCounter(t, m.leaseConflict); got != 1 {
		t.Errorf("leaseConflict=%v want 1", got)
	}
	if got := readCounter(t, m.resumeSafetyBlock); got != 1 {
		t.Errorf("resumeSafetyBlock=%v want 1", got)
	}
	if got := readCounter(t, m.actionDuplicate); got != 1 {
		t.Errorf("actionDuplicate=%v want 1", got)
	}
	if got := readCounter(t, m.auditDegraded); got != 1 {
		t.Errorf("auditDegraded=%v want 1", got)
	}
}

// TestPrometheusMetrics_LabeledCounters verifies label-bearing counters
// (terminal_reason, handoff_restore_failure, fallback_rejection,
// pending_projection_error). Only low-cardinality label values are allowed;
// IDs (goal_run / session / request) must never appear as labels.
func TestPrometheusMetrics_LabeledCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPrometheusMetrics(reg)

	m.IncTerminalReason("manual_required")
	m.IncTerminalReason("manual_required")
	m.IncTerminalReason("budget_exhausted")

	m.IncHandoffRestoreFailure("lease_lost")
	m.IncHandoffRestoreFailure("conflict")

	m.IncFallbackRejection("model_unavailable")

	m.IncPendingProjectionError("decrypt")
	m.IncPendingProjectionError("project_cas")

	if got := readCounterLabel(t, m.terminalReason, "manual_required"); got != 2 {
		t.Errorf("terminal_reason{manual_required}=%v want 2", got)
	}
	if got := readCounterLabel(t, m.terminalReason, "budget_exhausted"); got != 1 {
		t.Errorf("terminal_reason{budget_exhausted}=%v want 1", got)
	}
	if got := readCounterLabel(t, m.handoffRestoreFailure, "lease_lost"); got != 1 {
		t.Errorf("handoff_restore_failure{lease_lost}=%v want 1", got)
	}
	if got := readCounterLabel(t, m.fallbackRejection, "model_unavailable"); got != 1 {
		t.Errorf("fallback_rejection{model_unavailable}=%v want 1", got)
	}
	if got := readCounterLabel(t, m.pendingProjectionError, "decrypt"); got != 1 {
		t.Errorf("pending_projection_error{decrypt}=%v want 1", got)
	}
}

// TestPrometheusMetrics_NoIDLabels is a reflective guard against accidentally
// introducing high-cardinality labels. Per design §9.2 IDs MUST NOT enter
// metric labels — that would blow up Prometheus series count.
func TestPrometheusMetrics_NoIDLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPrometheusMetrics(reg)

	forbidden := []string{"goal_run", "session", "request_id", "task_id", "trace_id", "correlation_id", "causation_id"}
	collectors := []prometheus.Collector{
		m.activeRuns,
		m.terminalReason,
		m.retry,
		m.budgetStop,
		m.leaseConflict,
		m.resumeSafetyBlock,
		m.actionDuplicate,
		m.handoffRestoreFailure,
		m.auditDegraded,
		m.fallbackRejection,
		m.pendingProjectionError,
	}
	for _, c := range collectors {
		ch := make(chan prometheus.Metric, 32)
		c.Collect(ch)
		close(ch)
		for metric := range ch {
			var pb dto.Metric
			if err := metric.Write(&pb); err != nil {
				t.Fatalf("metric write: %v", err)
			}
			for _, lp := range pb.GetLabel() {
				name := strings.ToLower(lp.GetName())
				for _, bad := range forbidden {
					if strings.Contains(name, bad) {
						t.Errorf("high-cardinality label %q on metric (contains %q)", lp.GetName(), bad)
					}
				}
			}
		}
	}
}
