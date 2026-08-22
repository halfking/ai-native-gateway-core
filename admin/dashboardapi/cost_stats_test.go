package dashboardapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCostStatsJSONIncludesRequestLatency(t *testing.T) {
	raw, err := json.Marshal(CostStats{
		TotalCostUSD:  1.25,
		TotalRequests: 42,
		AvgLatencyMs:  320.5,
	})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.EqualValues(t, 42, m["total_requests"])
	assert.InDelta(t, 320.5, m["avg_latency_ms"], 0.01)
	assert.InDelta(t, 1.25, m["total_cost_usd"], 0.01)
}

func TestSessionPeriodWhereClauseAlwaysIncludesDays(t *testing.T) {
	days := 30
	frag := fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", days)
	assert.Contains(t, frag, "30 days")
	assert.Contains(t, frag, "first_request_at")
}
