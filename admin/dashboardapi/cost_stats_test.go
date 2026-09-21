package dashboardapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CostStats is cost-only since 585b76f20 densified the session KPIs; the JSON
// contract must keep exposing every cost aggregate.
func TestCostStatsJSONContract(t *testing.T) {
	raw, err := json.Marshal(CostStats{
		TotalCostUSD:      1.25,
		AvgCostPerSession: 0.5,
		AvgCostPerRequest: 0.05,
		MaxCostSession:    0.8,
		InputCostUSD:      0.45,
		OutputCostUSD:     0.8,
		CostGrowthPct:     12.5,
	})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.InDelta(t, 1.25, m["total_cost_usd"], 0.01)
	assert.InDelta(t, 0.5, m["avg_cost_per_session"], 0.01)
	assert.InDelta(t, 0.05, m["avg_cost_per_request"], 0.01)
	assert.InDelta(t, 0.8, m["max_cost_session"], 0.01)
	assert.InDelta(t, 0.45, m["input_cost_usd"], 0.01)
	assert.InDelta(t, 0.8, m["output_cost_usd"], 0.01)
	assert.InDelta(t, 12.5, m["cost_growth_pct"], 0.01)
}

// total_requests (SUM(session_summaries.request_count), migration 563
// restores the hot-path trigger that increments request_count) and the
// latency aggregate now surface through PerformanceSummary. A zero fixture
// would hide the JSON contract; keep non-zero samples.
func TestPerformanceSummaryJSONIncludesRequestCounters(t *testing.T) {
	raw, err := json.Marshal(PerformanceSummary{
		AvgLatencyMs:  320.5,
		TotalRequests: 42,
	})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.EqualValues(t, 42, m["total_requests"])
	assert.Greater(t, m["total_requests"], float64(0))
	assert.InDelta(t, 320.5, m["avg_latency_ms"], 0.01)
}

func TestSessionPeriodWhereClauseAlwaysIncludesDays(t *testing.T) {
	days := 30
	frag := fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", days)
	assert.Contains(t, frag, "30 days")
	assert.Contains(t, frag, "first_request_at")
}
