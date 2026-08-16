package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert string `yaml:"alert"`
			Expr  string `yaml:"expr"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func TestRoutingDBEmptyRuleUsesRegisteredLowCardinalityMetric(t *testing.T) {
	data, err := os.ReadFile("routing-db-empty.yml")
	require.NoError(t, err)

	var file ruleFile
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "routing_candidate_diagnostics", file.Groups[0].Name)
	require.Len(t, file.Groups[0].Rules, 1)
	require.Equal(t, "RoutingCandidateDBEmpty", file.Groups[0].Rules[0].Alert)

	expr := file.Groups[0].Rules[0].Expr
	require.Contains(t, expr, "llmgw_routing_candidate_diagnostics_total")
	require.Contains(t, expr, `event="db_empty"`)
	require.NotContains(t, expr, "model")
	require.NotContains(t, expr, "tenant")

	text := string(data)
	require.NotContains(t, text, "llmgw_routing_db_empty_total")
	require.False(t, strings.Contains(text, "model="))
	require.False(t, strings.Contains(text, "tenant"))
}
