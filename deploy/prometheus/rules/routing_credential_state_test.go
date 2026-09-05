package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRoutingCredentialStateRulesUseRegisteredMetrics(t *testing.T) {
	data, err := os.ReadFile("routing-credential-state.yml")
	require.NoError(t, err)

	var file ruleFile
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "routing_credential_state", file.Groups[0].Name)

	alerts := make([]string, 0, len(file.Groups[0].Rules))
	exprs := ""
	for _, r := range file.Groups[0].Rules {
		alerts = append(alerts, r.Alert)
		exprs += r.Expr + "\n"
	}

	// Every hzx-2 round-3 counter/gauge must be referenced by at least one
	// alert except llmgw_routing_priority_candidates_selected_total, which
	// is a distribution signal (dashboard-only by design).
	wantAlerts := []string{
		"RoutingCredentialResetErrors",
		"RoutingCredentialResetNotFoundSpike",
		"RoutingCredentialRecoveryErrors",
		"RoutingRecoveryTickSlow",
		"RoutingHealthAutoRecoverErrors",
		"RoutingAutoHealSubmitErrors",
		"RoutingCoolingFallbackTriggered",
	}
	require.ElementsMatch(t, wantAlerts, alerts)

	wantMetrics := []string{
		"llmgw_routing_credential_reset_total",
		"llmgw_routing_credential_recovery_total",
		"llmgw_routing_credential_recovery_tick_duration_seconds",
		"llmgw_routing_health_auto_recover_total",
		"llmgw_routing_auto_heal_submit_total",
		"llmgw_routing_cooling_fallback_total",
	}
	for _, m := range wantMetrics {
		require.Contains(t, exprs, m, "alert rules must cover metric %s", m)
	}

	// The 404/500 distinction (hzx-2 round-3 follow-up): not_found must be
	// its own alert condition, not folded into result="error".
	require.Contains(t, exprs, `surface="lookup",result="not_found"`)

	// GW-00 low-cardinality guard: no per-model or per-tenant selectors.
	require.NotContains(t, exprs, `model=`)
	require.NotContains(t, exprs, `tenant=`)

	text := string(data)
	require.False(t, strings.Contains(text, "model=\""))
	require.False(t, strings.Contains(text, "tenant=\""))
}
