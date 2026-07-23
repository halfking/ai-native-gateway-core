package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mockSettingsAdapter provides deterministic settings for testing
type mockSettingsAdapter struct {
	bools   map[string]bool
	ints    map[string]int
	floats  map[string]float64
	strings map[string]string
}

func (m *mockSettingsAdapter) GetBool(tenantID, key string, def bool) bool {
	k := tenantID + ":" + key
	if v, ok := m.bools[k]; ok {
		return v
	}
	return def
}

func (m *mockSettingsAdapter) GetInt(tenantID, key string, def int) int {
	k := tenantID + ":" + key
	if v, ok := m.ints[k]; ok {
		return v
	}
	return def
}

func (m *mockSettingsAdapter) GetFloat(tenantID, key string, def float64) float64 {
	k := tenantID + ":" + key
	if v, ok := m.floats[k]; ok {
		return v
	}
	return def
}

func (m *mockSettingsAdapter) GetString(tenantID, key string, def string) string {
	k := tenantID + ":" + key
	if v, ok := m.strings[k]; ok {
		return v
	}
	return def
}

func TestGoalRetryPolicyResolver_Presets(t *testing.T) {
	adapter := &mockSettingsAdapter{
		bools:   make(map[string]bool),
		ints:    make(map[string]int),
		floats:  make(map[string]float64),
		strings: make(map[string]string),
	}

	resolver := newGoalRetryPolicyResolver(adapter)

	tests := []struct {
		name            string
		tenantID        string
		expectedMode    string
		expectedRetry   int
		expectedTimeout time.Duration
	}{
		{
			name:            "minimal mode default when no global registry",
			tenantID:        "tenant-a",
			expectedMode:    "minimal",
			expectedRetry:   2,
			expectedTimeout: 40 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := resolver.ResolveGoalRetryPolicy(tt.tenantID)

			require.Equal(t, tt.expectedMode, policy.CostMode)
			require.Equal(t, tt.expectedRetry, policy.MaxRetries)
			require.Equal(t, tt.expectedTimeout, policy.TotalTimeout)
			require.True(t, policy.Enabled) // preset default
		})
	}
}

func TestGoalRetryPolicyResolver_TenantIsolation(t *testing.T) {
	adapter := &mockSettingsAdapter{
		bools:   make(map[string]bool),
		ints:    make(map[string]int),
		floats:  make(map[string]float64),
		strings: make(map[string]string),
	}

	// Set different overrides for two tenants
	adapter.ints["tenant-x:goal.max_retry_count"] = 10
	adapter.ints["tenant-y:goal.max_retry_count"] = 1

	resolver := newGoalRetryPolicyResolver(adapter)

	policyX := resolver.ResolveGoalRetryPolicy("tenant-x")
	policyY := resolver.ResolveGoalRetryPolicy("tenant-y")

	require.Equal(t, 10, policyX.MaxRetries, "tenant-x should have override")
	require.Equal(t, 1, policyY.MaxRetries, "tenant-y should have override")
	require.NotEqual(t, policyX.MaxRetries, policyY.MaxRetries, "tenants must not share config")
}

func TestGoalRetryPolicyResolver_IndividualOverrides(t *testing.T) {
	adapter := &mockSettingsAdapter{
		bools:   make(map[string]bool),
		ints:    make(map[string]int),
		floats:  make(map[string]float64),
		strings: make(map[string]string),
	}

	// Override individual settings
	adapter.bools["tenant-z:goal.retry_on_error"] = false
	adapter.ints["tenant-z:goal.max_retry_count"] = 7
	adapter.ints["tenant-z:goal.retry_total_timeout_seconds"] = 90

	resolver := newGoalRetryPolicyResolver(adapter)

	policy := resolver.ResolveGoalRetryPolicy("tenant-z")

	require.False(t, policy.Enabled, "retry_on_error override")
	require.Equal(t, 7, policy.MaxRetries, "max_retry_count override")
	require.Equal(t, 90*time.Second, policy.TotalTimeout, "timeout override")
}
