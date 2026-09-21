package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRoutingAnalyticsMVDriftMetricsRegistered verifies that all five
// consistency-check metrics are registered in the default registry with the
// correct names and label sets. Mirrors the pattern in
// incomplete_tool_call_metrics_test.go and empty_response_journal_metrics_test.go.
func TestRoutingAnalyticsMVDriftMetricsRegistered(t *testing.T) {
	descs := collectDeclaredDescs(t)
	require.NotEmpty(t, descs, "default registry should have descriptors")

	// Map fqName → descriptor string for lookup
	byName := make(map[string]string)
	for _, ds := range descs {
		byName[parseFQName(ds)] = ds
	}

	// Verify the five metrics exist with correct label dimensions
	requireMetricWithLabels(t, byName, "routing_analytics_mv_drift_pct", []string{"view"})
	requireMetricWithLabels(t, byName, "routing_analytics_mv_drift_abs", []string{"view"})
	requireMetricWithLabels(t, byName, "routing_analytics_mv_breach_count", []string{"view"})
	requireMetricWithLabels(t, byName, "routing_analytics_mv_consistency_last_unix", []string{"view"})
	requireMetricWithLabels(t, byName, "routing_analytics_mv_consistency_errors_total", []string{"view", "reason"})
}

// requireMetricWithLabels asserts that a metric exists in the registry with
// exactly the specified label set (order-independent).
func requireMetricWithLabels(t *testing.T, byName map[string]string, fqName string, wantLabels []string) {
	t.Helper()
	desc, found := byName[fqName]
	require.True(t, found, "metric %s not found in registry", fqName)

	gotLabels := parseVarLabels(desc)
	require.ElementsMatch(t, wantLabels, gotLabels,
		"metric %s has wrong label set: got %v, want %v", fqName, gotLabels, wantLabels)
}

// TestRoutingAnalyticsMVDriftNoHighCardinalityLabels verifies that the
// consistency metrics only use the closed-enum "view" label (and "reason" for
// errors), not model/tenant/request_id/etc. This is already covered by the
// global TestNoHighCardinalityLabels, but we test it explicitly here to guard
// against future label additions that would blow cardinality.
func TestRoutingAnalyticsMVDriftNoHighCardinalityLabels(t *testing.T) {
	descs := collectDeclaredDescs(t)
	forbiddenLabels := []string{
		"model", "tenant_id", "request_id", "session_id", "credential_id",
	}

	for _, ds := range descs {
		fqName := parseFQName(ds)
		if fqName != "routing_analytics_mv_drift_pct" &&
			fqName != "routing_analytics_mv_drift_abs" &&
			fqName != "routing_analytics_mv_breach_count" &&
			fqName != "routing_analytics_mv_consistency_last_unix" &&
			fqName != "routing_analytics_mv_consistency_errors_total" {
			continue
		}

		labels := parseVarLabels(ds)
		for _, forbidden := range forbiddenLabels {
			require.NotContains(t, labels, forbidden,
				"metric %s must not use high-cardinality label %s", fqName, forbidden)
		}
	}
}
