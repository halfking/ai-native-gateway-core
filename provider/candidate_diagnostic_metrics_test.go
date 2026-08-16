package provider

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestCandidateDiagnosticMetricUsesOnlyWhitelistedEvents(t *testing.T) {
	allowedEvents := []string{
		"db_empty",
		"db_empty_fallback",
		"cache_empty",
		"db_unavailable",
		"stale_cache_empty",
		"enrich_empty",
		"db_query_retry",
		"other",
	}
	allowedEventSet := make(map[string]struct{}, len(allowedEvents))
	for _, event := range allowedEvents {
		allowedEventSet[event] = struct{}{}
	}

	candidateDiagnosticMetrics.Reset()
	for _, event := range allowedEvents {
		recordCandidateDiagnostic(event)
	}
	recordCandidateDiagnostic("unbounded-event-value")

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
	for _, event := range allowedEvents {
		require.Contains(t, observed, event, "whitelisted event %q should be observable", event)
	}
	require.Equal(t, 2.0, observed["other"], "unknown events should be collapsed into other")
}
