package rules_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestClientTokenUnknownShareRuleUsesFiveMinuteCounterRatio(t *testing.T) {
	data, err := os.ReadFile("client-token-classification.yml")
	require.NoError(t, err)

	var file ruleFile
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "llmgw_client_token_classification", file.Groups[0].Name)
	require.Len(t, file.Groups[0].Rules, 1)
	require.Equal(t, "LLMGatewayClientTokenUnknownShareHigh", file.Groups[0].Rules[0].Alert)
	require.Contains(t, file.Groups[0].Rules[0].Expr, "gateway_client_token_requests_total")
	require.Contains(t, file.Groups[0].Rules[0].Expr, `[5m]`)
	require.Contains(t, file.Groups[0].Rules[0].Expr, `client_type="unknown"`)
	require.NotContains(t, file.Groups[0].Rules[0].Expr, "gateway_client_token_unknown_ratio")

	var raw struct {
		Groups []struct {
			Rules []struct {
				For string `yaml:"for"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &raw))
	require.Equal(t, "5m", raw.Groups[0].Rules[0].For)
}
