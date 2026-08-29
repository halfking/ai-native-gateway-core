package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

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
	require.Len(t, group.Rules, 3)

	type alertContract struct {
		expr string
		wait string
	}
	byAlert := make(map[string]alertContract)
	for _, rule := range group.Rules {
		byAlert[rule.Alert] = alertContract{expr: rule.Expr, wait: rule.For}
		require.NotContains(t, rule.Expr, "model=")
		require.NotContains(t, rule.Expr, "tenant=")
		require.Contains(t, rule.Expr, "table")
	}

	require.Contains(t, byAlert, "HotTablePromoteFailures")
	require.Contains(t, byAlert["HotTablePromoteFailures"].expr, "llm_gateway_hot_table_promote_failures_total")
	require.Equal(t, "5m", byAlert["HotTablePromoteFailures"].wait)
	require.Contains(t, byAlert, "HotTablePromoteLockContention")
	require.Contains(t, byAlert["HotTablePromoteLockContention"].expr, "llm_gateway_hot_table_promote_skipped_total")
	require.Contains(t, byAlert, "HotTablePromoteSlow")
	require.Contains(t, byAlert["HotTablePromoteSlow"].expr, "llm_gateway_hot_table_promote_duration_seconds_bucket")

	text := string(data)
	require.False(t, strings.Contains(text, "tenant="))
	require.False(t, strings.Contains(text, "model="))
}
