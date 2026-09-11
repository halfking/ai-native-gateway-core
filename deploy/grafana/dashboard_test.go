package grafana_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Contract pins for the self-check necessity gate dashboard, mirroring the
// alert-side pins in deploy/prometheus/rules/alerts_test.go: renaming a
// necessity counter or deleting/reshaping the dashboard must fail CI instead
// of silently breaking the observation loop documented in
// docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md.

type necessityDashboardPanel struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	FieldConfig struct {
		Defaults struct {
			Thresholds struct {
				Steps []struct {
					Color string   `json:"color"`
					Value *float64 `json:"value"`
				} `json:"steps"`
			} `json:"thresholds"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
}

func loadSelfcheckNecessityDashboard(t *testing.T) ([]necessityDashboardPanel, []string) {
	t.Helper()
	data, err := os.ReadFile("selfcheck-necessity-dashboard.json")
	require.NoError(t, err)

	var dash struct {
		UID    string                    `json:"uid"`
		Panels []necessityDashboardPanel `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(data, &dash))
	require.Equal(t, "llm-gateway-selfcheck-necessity", dash.UID)
	require.Len(t, dash.Panels, 3)

	exprs := make([]string, 0, len(dash.Panels))
	for _, panel := range dash.Panels {
		require.Equal(t, "timeseries", panel.Type)
		require.Len(t, panel.Targets, 1)
		require.NotEmpty(t, panel.Targets[0].Expr)
		exprs = append(exprs, panel.Targets[0].Expr)
	}
	return dash.Panels, exprs
}

func TestSelfcheckNecessityDashboardPinsAllThreeCounters(t *testing.T) {
	_, exprs := loadSelfcheckNecessityDashboard(t)

	for _, metric := range []string{
		"llmgw_node_probe_necessity_skip_total",
		"llmgw_node_probe_necessity_mirror_delete_retry_total",
		"llmgw_node_probe_necessity_mirror_delete_failed_total",
	} {
		matched := 0
		for _, expr := range exprs {
			if strings.Contains(expr, metric) {
				matched++
			}
		}
		require.Equal(t, 1, matched, "expected exactly one panel expr on %s", metric)
	}

	// Same low-cardinality discipline as alerts.yml: dashboard exprs must not
	// filter or group by tenant/model.
	for _, expr := range exprs {
		require.NotContains(t, expr, "tenant=")
		require.NotContains(t, expr, "model=")
	}
}

func TestSelfcheckNecessityDashboardFailureThresholdMatchesAlert(t *testing.T) {
	panels, exprs := loadSelfcheckNecessityDashboard(t)

	var failedPanel *necessityDashboardPanel
	for i := range panels {
		if strings.Contains(exprs[i], "llmgw_node_probe_necessity_mirror_delete_failed_total") {
			failedPanel = &panels[i]
		}
	}
	require.NotNil(t, failedPanel, "failure panel missing")

	// Red threshold must stay at 5 to match NodeProbeNecessityMirrorDeleteFailedHigh
	// (increase(...[10m]) > 5).
	found := false
	for _, step := range failedPanel.FieldConfig.Defaults.Thresholds.Steps {
		if step.Color == "red" {
			require.NotNil(t, step.Value)
			require.Equal(t, float64(5), *step.Value)
			found = true
		}
	}
	require.True(t, found, "failure panel must define a red threshold step")
}

func TestGrafanaReadmeListsSelfcheckNecessityDashboard(t *testing.T) {
	data, err := os.ReadFile("README.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "selfcheck-necessity-dashboard.json")
}
