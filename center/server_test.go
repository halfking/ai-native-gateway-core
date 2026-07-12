//go:build !integration

package center

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDashboardStats_JSONTags verifies that DashboardStats marshals to snake_case
// JSON keys matching the frontend contract (see web/src/api/ops.ts CenterStats).
func TestDashboardStats_JSONTags(t *testing.T) {
	stats := DashboardStats{
		TotalInstances:    42,
		OnlineInstances:   30,
		OfflineInstances:  10,
		DegradedInstances: 2,
	}

	data, err := json.Marshal(stats)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))

	expectedKeys := map[string]float64{
		"total_instances":    42,
		"online_instances":   30,
		"offline_instances":  10,
		"degraded_instances": 2,
	}
	for key, want := range expectedKeys {
		v, ok := got[key]
		require.Truef(t, ok, "missing key %q in JSON output: %s", key, string(data))
		assert.EqualValues(t, want, v, "wrong value for key %q", key)
	}
}

// TestDashboardStats_JSONTagsOmitsPascalCase asserts we no longer leak Go field
// names ("TotalInstances") into the wire format.
func TestDashboardStats_JSONTagsOmitsPascalCase(t *testing.T) {
	stats := DashboardStats{}
	data, err := json.Marshal(stats)
	require.NoError(t, err)
	raw := string(data)
	for _, pascal := range []string{"TotalInstances", "OnlineInstances", "OfflineInstances", "DegradedInstances"} {
		assert.NotContainsf(t, raw, pascal, "JSON output should not contain Go field name %q", pascal)
	}
}
