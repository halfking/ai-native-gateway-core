package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// metricNamesAreRegistered pins the durable_* series names: dashboards and
// alert rules are authored against them before producers ship (doc 18 §15).
func metricNamesAreRegistered(t *testing.T, names ...string) {
	t.Helper()
	registered := map[string]bool{}
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		registered[mf.GetName()] = true
	}
	for _, name := range names {
		require.True(t, registered[name], "metric %s should be registered", name)
	}
}

func TestDurableMetricNamesRegistered(t *testing.T) {
	// Vec children only appear in Gather once observed — touch one label
	// combination per vec so the series names are visible.
	DurableTasksActive.WithLabelValues("__probe__").Set(0)
	DurableRecoveryRunsTotal.WithLabelValues("__probe__").Inc()
	DurablePendingProjectionsTotal.WithLabelValues("__probe__").Inc()
	DurablePendingProjectionErrorsTotal.WithLabelValues("__probe__").Inc()
	DurableLeaseLostTotal.Inc()
	metricNamesAreRegistered(t,
		"durable_tasks_active",
		"durable_recovery_runs_total",
		"durable_pending_projections_total",
		"durable_pending_projection_errors_total",
		"durable_lease_lost_total",
	)
}

func TestDurableCountersIncrement(t *testing.T) {
	DurableRecoveryRunsTotal.WithLabelValues("claimed").Inc()
	DurablePendingProjectionsTotal.WithLabelValues("completed").Add(2)
	DurablePendingProjectionErrorsTotal.WithLabelValues("project_cas").Inc()
	DurableLeaseLostTotal.Inc()

	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	values := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var v float64
			if m.GetCounter() != nil {
				v = m.GetCounter().GetValue()
			}
			if v == 0 {
				continue
			}
			key := mf.GetName()
			for _, l := range m.GetLabel() {
				key += "{" + l.GetValue() + "}"
			}
			values[key] = v
		}
	}
	require.GreaterOrEqual(t, values["durable_recovery_runs_total{claimed}"], 1.0)
	require.GreaterOrEqual(t, values["durable_pending_projections_total{completed}"], 2.0)
	require.GreaterOrEqual(t, values["durable_pending_projection_errors_total{project_cas}"], 1.0)
	require.GreaterOrEqual(t, values["durable_lease_lost_total"], 1.0)
}
