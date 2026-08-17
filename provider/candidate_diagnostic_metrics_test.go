package provider

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestCandidateDiagnosticMetricInitializesAndUsesOnlyWhitelistedEvents(t *testing.T) {
	allowedEventSet := make(map[string]struct{}, len(candidateDiagnosticEvents))
	for _, event := range candidateDiagnosticEvents {
		allowedEventSet[event] = struct{}{}
	}

	candidateDiagnosticMetrics.Reset()
	initializeCandidateDiagnosticMetrics()

	observed := gatherCandidateDiagnosticMetrics(t, allowedEventSet)
	require.Len(t, observed, len(candidateDiagnosticEvents))
	for _, event := range candidateDiagnosticEvents {
		require.Contains(t, observed, event, "whitelisted event %q should be observable", event)
		require.Zero(t, observed[event], "whitelisted event %q should start at zero", event)
	}

	for _, event := range candidateDiagnosticEvents {
		recordCandidateDiagnostic(event)
	}
	recordCandidateDiagnostic("unbounded-event-value")

	observed = gatherCandidateDiagnosticMetrics(t, allowedEventSet)
	for _, event := range candidateDiagnosticEvents {
		expected := 1.0
		if event == "other" {
			expected = 2.0
		}
		require.Equal(t, expected, observed[event], "unexpected count for event %q", event)
	}
}

func gatherCandidateDiagnosticMetrics(t *testing.T, allowedEventSet map[string]struct{}) map[string]float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var found bool
	observed := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "llmgw_routing_candidate_diagnostics_total" {
			continue
		}
		found = true
		for _, metric := range mf.GetMetric() {
			require.Len(t, metric.GetLabel(), 1)
			require.Equal(t, "event", metric.GetLabel()[0].GetName())
			event := metric.GetLabel()[0].GetValue()
			_, allowed := allowedEventSet[event]
			require.True(t, allowed, "unexpected event label %q", event)
			observed[event] = metric.GetCounter().GetValue()
		}
	}

	require.True(t, found, "candidate diagnostic metric should be registered")
	return observed
}
