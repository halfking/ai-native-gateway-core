package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestResponseBodyMissingAlertUsesTelemetrySSOT(t *testing.T) {
	data, err := os.ReadFile("alerts.yml")
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

	var rule struct {
		Alert string
		Expr  string
		For   string
	}
	for _, group := range raw.Groups {
		if group.Name != "response_body_integrity_alerts" {
			continue
		}
		require.Len(t, group.Rules, 1)
		rule.Alert = group.Rules[0].Alert
		rule.Expr = group.Rules[0].Expr
		rule.For = group.Rules[0].For
	}
	require.Equal(t, "GatewaySuccessfulNonStreamResponseBodyMissing", rule.Alert)
	require.Contains(t, rule.Expr, "telemetry_success_response_body_missing_total")
	require.Contains(t, rule.Expr, `stream="non_stream"`)
	require.Equal(t, "5m", rule.For)
	require.NotContains(t, rule.Expr, "tenant")
	require.NotContains(t, rule.Expr, "request_id")
}

func TestHotTablePromoteAlertsUseRegisteredLowCardinalityMetrics(t *testing.T) {
	data, err := os.ReadFile("alerts.yml")
	require.NoError(t, err)

	var group struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert string `yaml:"alert"`
			Expr  string `yaml:"expr"`
			For   string `yaml:"for"`
		} `yaml:"rules"`
	}

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
	for _, candidate := range raw.Groups {
		if candidate.Name == "hot_table_promote_alerts" {
			group = candidate
			break
		}
	}
	require.Equal(t, "hot_table_promote_alerts", group.Name)
	require.GreaterOrEqual(t, len(group.Rules), 3)

	type alertContract struct {
		expr string
		wait string
	}
	byAlert := make(map[string]alertContract)
	for _, rule := range group.Rules {
		byAlert[rule.Alert] = alertContract{expr: rule.Expr, wait: rule.For}
		if strings.HasPrefix(rule.Alert, "HotTablePromote") {
			require.NotContains(t, rule.Expr, "model=")
			require.NotContains(t, rule.Expr, "tenant=")
			require.Contains(t, rule.Expr, "table")
		}
	}

	require.Contains(t, byAlert, "HotTablePromoteFailures")
	require.Contains(t, byAlert["HotTablePromoteFailures"].expr, "llm_gateway_hot_table_promote_failures_total")
	require.Equal(t, "5m", byAlert["HotTablePromoteFailures"].wait)
	require.Contains(t, byAlert, "HotTablePromoteLockContention")
	require.Contains(t, byAlert["HotTablePromoteLockContention"].expr, "llm_gateway_hot_table_promote_skipped_total")
	require.Contains(t, byAlert, "HotTablePromoteSlow")
	require.Contains(t, byAlert["HotTablePromoteSlow"].expr, "llm_gateway_hot_table_promote_duration_seconds_bucket")
	require.Contains(t, byAlert, "NodeProbeQueueSubmissionFailuresHigh")
	require.Contains(t, byAlert["NodeProbeQueueSubmissionFailuresHigh"].expr, "llmgw_node_probe_queue_submission_total")
	require.Contains(t, byAlert, "NodeProbeQueueSubmissionRetriesHigh")
	require.Contains(t, byAlert["NodeProbeQueueSubmissionRetriesHigh"].expr, "llmgw_node_probe_queue_submission_retries_total")

	text := string(data)
	require.False(t, strings.Contains(text, "tenant="))
	require.False(t, strings.Contains(text, "model="))
}
