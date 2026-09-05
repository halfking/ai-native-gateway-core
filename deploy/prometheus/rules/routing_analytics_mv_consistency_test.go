package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestRoutingAnalyticsMVConsistencyAlertsUseRegisteredMetrics verifies that
// the routing-analytics-mv-consistency.yml alert rules reference only the
// metrics actually registered in metrics/routing_analytics_mv_drift.go and
// bg/metrics.go, with the correct label sets (closed-enum "view" only).
func TestRoutingAnalyticsMVConsistencyAlertsUseRegisteredMetrics(t *testing.T) {
	data, err := os.ReadFile("routing-analytics-mv-consistency.yml")
	require.NoError(t, err)

	var raw struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
				For   string `yaml:"for"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &raw))

	// Verify the single group exists
	groupIdx := -1
	for i, candidate := range raw.Groups {
		if candidate.Name == "routing_analytics_mv_consistency" {
			groupIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, groupIdx, 0, "routing_analytics_mv_consistency group not found")
	group := raw.Groups[groupIdx]
	require.Len(t, group.Rules, 5, "expected 5 alert rules: drift, check stale, check erroring, refresh failing, refresh stale")

	// Map alert name → expr/for contract
	type alertContract struct {
		expr string
		wait string
	}
	byAlert := make(map[string]alertContract)
	for _, rule := range group.Rules {
		byAlert[rule.Alert] = alertContract{expr: rule.Expr, wait: rule.For}

		// Cardinality guard: no high-cardinality labels in any expr
		require.NotContains(t, rule.Expr, "model=", "alert %s must not filter by model", rule.Alert)
		require.NotContains(t, rule.Expr, "tenant=", "alert %s must not filter by tenant_id", rule.Alert)
		require.NotContains(t, rule.Expr, "request_id=", "alert %s must not filter by request_id", rule.Alert)
		require.NotContains(t, rule.Expr, "session_id=", "alert %s must not filter by session_id", rule.Alert)

		// All rules should reference the fixed view label (closed enum)
		if !strings.Contains(rule.Expr, "time()") { // time() expressions don't have view label
			require.Contains(t, rule.Expr, `view="routing_analytics_7d"`,
				"alert %s should filter by the fixed view label", rule.Alert)
		}
	}

	// Alert 1: RoutingAnalyticsMVDriftHigh
	require.Contains(t, byAlert, "RoutingAnalyticsMVDriftHigh")
	require.Contains(t, byAlert["RoutingAnalyticsMVDriftHigh"].expr, "routing_analytics_mv_breach_count")
	require.Contains(t, byAlert["RoutingAnalyticsMVDriftHigh"].expr, "> 0")
	require.Equal(t, "20m", byAlert["RoutingAnalyticsMVDriftHigh"].wait,
		"drift alert should wait 20m (2 refresh cycles) to avoid transient spikes")

	// Alert 2: RoutingAnalyticsMVConsistencyCheckStale
	require.Contains(t, byAlert, "RoutingAnalyticsMVConsistencyCheckStale")
	require.Contains(t, byAlert["RoutingAnalyticsMVConsistencyCheckStale"].expr, "time() - routing_analytics_mv_consistency_last_unix")
	require.Contains(t, byAlert["RoutingAnalyticsMVConsistencyCheckStale"].expr, "> 3600")
	require.Equal(t, "15m", byAlert["RoutingAnalyticsMVConsistencyCheckStale"].wait)

	// Alert 3: RoutingAnalyticsMVConsistencyCheckErroring
	require.Contains(t, byAlert, "RoutingAnalyticsMVConsistencyCheckErroring")
	require.Contains(t, byAlert["RoutingAnalyticsMVConsistencyCheckErroring"].expr, "routing_analytics_mv_consistency_errors_total")
	require.Contains(t, byAlert["RoutingAnalyticsMVConsistencyCheckErroring"].expr, "increase")
	require.Equal(t, "5m", byAlert["RoutingAnalyticsMVConsistencyCheckErroring"].wait)

	// Alert 4: RoutingAnalyticsMVRefreshFailing
	require.Contains(t, byAlert, "RoutingAnalyticsMVRefreshFailing")
	require.Contains(t, byAlert["RoutingAnalyticsMVRefreshFailing"].expr, "gateway_mv_refresh_total")
	require.Contains(t, byAlert["RoutingAnalyticsMVRefreshFailing"].expr, `outcome="failed"`)
	require.Equal(t, "20m", byAlert["RoutingAnalyticsMVRefreshFailing"].wait)

	// Alert 5: RoutingAnalyticsMVRefreshStale
	require.Contains(t, byAlert, "RoutingAnalyticsMVRefreshStale")
	require.Contains(t, byAlert["RoutingAnalyticsMVRefreshStale"].expr, "time() - gateway_mv_refresh_last_success_unix")
	require.Contains(t, byAlert["RoutingAnalyticsMVRefreshStale"].expr, "> 1200")
	require.Equal(t, "5m", byAlert["RoutingAnalyticsMVRefreshStale"].wait)

	// Verify no stray sensitive strings in the entire file (belt-and-suspenders)
	text := string(data)
	require.False(t, strings.Contains(text, "tenant="))
	require.False(t, strings.Contains(text, "model="))
	require.False(t, strings.Contains(text, "request_id="))
}
