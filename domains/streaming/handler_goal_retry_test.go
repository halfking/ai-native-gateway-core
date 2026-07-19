package streaming

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/assert"
)

// TestGoalRetryConfigLoading tests Phase 1.6 configuration loading logic
func TestGoalRetryConfigLoading(t *testing.T) {
	tests := []struct {
		name             string
		costMode         string
		expectedRetries  int
		expectedTimeout  int
		setupSettings    func() *settings.Registry
		expectedCostMode string
	}{
		{
			name:             "minimal mode",
			costMode:         "minimal",
			expectedRetries:  2,
			expectedTimeout:  40,
			setupSettings:    nil,
			expectedCostMode: "minimal",
		},
		{
			name:             "balanced mode",
			costMode:         "balanced",
			expectedRetries:  3,
			expectedTimeout:  50,
			setupSettings:    nil,
			expectedCostMode: "balanced",
		},
		{
			name:             "aggressive mode",
			costMode:         "aggressive",
			expectedRetries:  5,
			expectedTimeout:  120,
			setupSettings:    nil,
			expectedCostMode: "aggressive",
		},
		{
			name:             "invalid mode falls back to minimal",
			costMode:         "invalid-mode",
			expectedRetries:  2,
			expectedTimeout:  40,
			setupSettings:    nil,
			expectedCostMode: "minimal",
		},
		{
			name:             "empty mode falls back to minimal",
			costMode:         "",
			expectedRetries:  2,
			expectedTimeout:  40,
			setupSettings:    nil,
			expectedCostMode: "minimal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get preset for the cost mode
			preset := goal.GetPreset(tt.costMode)

			// Verify retry configuration
			assert.True(t, preset.RetryEnabled, "RetryEnabled should be true for all modes")
			assert.Equal(t, tt.expectedRetries, preset.MaxRetryCount, "MaxRetryCount mismatch")
			assert.Equal(t, tt.expectedTimeout, preset.RetryTotalTimeout, "RetryTotalTimeout mismatch")
		})
	}
}

// TestGoalRetryConfigWithSettings tests configuration loading with settings system
func TestGoalRetryConfigWithSettings(t *testing.T) {
	tests := []struct {
		name             string
		settingsValue    interface{}
		settingsError    bool
		expectedCostMode string
		expectedRetries  int
		expectedTimeout  int
	}{
		{
			name:             "settings returns aggressive mode",
			settingsValue:    "aggressive",
			settingsError:    false,
			expectedCostMode: "aggressive",
			expectedRetries:  5,
			expectedTimeout:  120,
		},
		{
			name:             "settings returns balanced mode",
			settingsValue:    "balanced",
			settingsError:    false,
			expectedCostMode: "balanced",
			expectedRetries:  3,
			expectedTimeout:  50,
		},
		{
			name:             "settings returns minimal mode",
			settingsValue:    "minimal",
			settingsError:    false,
			expectedCostMode: "minimal",
			expectedRetries:  2,
			expectedTimeout:  40,
		},
		{
			name:             "settings returns empty string, falls back to balanced",
			settingsValue:    "",
			settingsError:    false,
			expectedCostMode: "balanced",
			expectedRetries:  3,
			expectedTimeout:  50,
		},
		{
			name:             "settings returns null, falls back to balanced",
			settingsValue:    nil,
			settingsError:    false,
			expectedCostMode: "balanced",
			expectedRetries:  3,
			expectedTimeout:  50,
		},
		{
			name:             "settings error, falls back to balanced",
			settingsValue:    nil,
			settingsError:    true,
			expectedCostMode: "balanced",
			expectedRetries:  3,
			expectedTimeout:  50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate settings system behavior
			costMode := "balanced" // default fallback

			if !tt.settingsError && tt.settingsValue != nil {
				// Simulate successful settings read
				if strValue, ok := tt.settingsValue.(string); ok && strValue != "" {
					costMode = strValue
				}
			}

			// Get preset
			preset := goal.GetPreset(costMode)

			// Verify
			assert.Equal(t, tt.expectedRetries, preset.MaxRetryCount, "MaxRetryCount mismatch")
			assert.Equal(t, tt.expectedTimeout, preset.RetryTotalTimeout, "RetryTotalTimeout mismatch")
		})
	}
}

// TestGoalRetryConfigJSONParsing tests JSON parsing edge cases
func TestGoalRetryConfigJSONParsing(t *testing.T) {
	tests := []struct {
		name             string
		jsonValue        []byte
		expectedCostMode string
		shouldParseFail  bool
	}{
		{
			name:             "valid JSON string",
			jsonValue:        []byte(`"aggressive"`),
			expectedCostMode: "aggressive",
			shouldParseFail:  false,
		},
		{
			name:             "JSON with whitespace",
			jsonValue:        []byte(`  "balanced"  `),
			expectedCostMode: "balanced",
			shouldParseFail:  false,
		},
		{
			name:             "empty JSON string",
			jsonValue:        []byte(`""`),
			expectedCostMode: "balanced", // fallback
			shouldParseFail:  false,
		},
		{
			name:             "JSON null",
			jsonValue:        []byte(`null`),
			expectedCostMode: "balanced", // fallback
			shouldParseFail:  false,
		},
		{
			name:             "invalid JSON - number",
			jsonValue:        []byte(`123`),
			expectedCostMode: "balanced", // fallback on parse error
			shouldParseFail:  true,
		},
		{
			name:             "invalid JSON - boolean",
			jsonValue:        []byte(`true`),
			expectedCostMode: "balanced", // fallback on parse error
			shouldParseFail:  true,
		},
		{
			name:             "invalid JSON - malformed",
			jsonValue:        []byte(`{broken`),
			expectedCostMode: "balanced", // fallback on parse error
			shouldParseFail:  true,
		},
		{
			name:             "empty byte array",
			jsonValue:        []byte{},
			expectedCostMode: "balanced", // fallback
			shouldParseFail:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			costMode := "balanced" // default fallback

			if len(tt.jsonValue) > 0 {
				var mode string
				err := json.Unmarshal(tt.jsonValue, &mode)

				if tt.shouldParseFail {
					assert.Error(t, err, "Expected JSON parsing to fail")
				}

				if err == nil && mode != "" {
					costMode = mode
				}
			}

			// Verify GetPreset handles the mode correctly
			preset := goal.GetPreset(costMode)
			assert.NotNil(t, preset, "GetPreset should always return a valid preset")
			assert.True(t, preset.RetryEnabled, "RetryEnabled should be true")
		})
	}
}

// TestGoalRetryConfigEdgeCases tests edge cases and boundary conditions
func TestGoalRetryConfigEdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		costMode        string
		expectedMinimal bool // Should fall back to minimal preset
	}{
		{
			name:            "whitespace only",
			costMode:        "   ",
			expectedMinimal: true,
		},
		{
			name:            "mixed case",
			costMode:        "Aggressive",
			expectedMinimal: true, // case-sensitive, should fallback
		},
		{
			name:            "with newline",
			costMode:        "balanced\n",
			expectedMinimal: true, // exact match fails
		},
		{
			name:            "unicode characters",
			costMode:        "激进模式",
			expectedMinimal: true,
		},
		{
			name:            "SQL injection attempt",
			costMode:        "'; DROP TABLE--",
			expectedMinimal: true,
		},
		{
			name:            "extremely long string",
			costMode:        string(make([]byte, 10000)),
			expectedMinimal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := goal.GetPreset(tt.costMode)

			if tt.expectedMinimal {
				// Should fall back to minimal preset
				minimalPreset := goal.GetPreset("minimal")
				assert.Equal(t, minimalPreset.MaxRetryCount, preset.MaxRetryCount,
					"Should fall back to minimal preset")
				assert.Equal(t, minimalPreset.RetryTotalTimeout, preset.RetryTotalTimeout,
					"Should fall back to minimal preset")
			}
		})
	}
}

// TestGoalRetryPresetConsistency verifies preset configuration consistency
func TestGoalRetryPresetConsistency(t *testing.T) {
	modes := []string{"minimal", "balanced", "aggressive"}

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			preset := goal.GetPreset(mode)

			// Basic sanity checks
			assert.True(t, preset.RetryEnabled, "RetryEnabled should be true")
			assert.Greater(t, preset.MaxRetryCount, 0, "MaxRetryCount should be positive")
			assert.Greater(t, preset.RetryTotalTimeout, 0, "RetryTotalTimeout should be positive")

			// Timeout should be reasonable (between 10s and 300s)
			assert.GreaterOrEqual(t, preset.RetryTotalTimeout, 10,
				"RetryTotalTimeout should be at least 10 seconds")
			assert.LessOrEqual(t, preset.RetryTotalTimeout, 300,
				"RetryTotalTimeout should be at most 300 seconds")

			// Retry count should be reasonable (between 1 and 10)
			assert.GreaterOrEqual(t, preset.MaxRetryCount, 1,
				"MaxRetryCount should be at least 1")
			assert.LessOrEqual(t, preset.MaxRetryCount, 10,
				"MaxRetryCount should be at most 10")
		})
	}
}

// TestGoalRetryPresetProgression verifies that presets progress logically
func TestGoalRetryPresetProgression(t *testing.T) {
	minimal := goal.GetPreset("minimal")
	balanced := goal.GetPreset("balanced")
	aggressive := goal.GetPreset("aggressive")

	// Retry count should increase: minimal < balanced < aggressive
	assert.Less(t, minimal.MaxRetryCount, balanced.MaxRetryCount,
		"balanced should have more retries than minimal")
	assert.Less(t, balanced.MaxRetryCount, aggressive.MaxRetryCount,
		"aggressive should have more retries than balanced")

	// Timeout should increase: minimal < balanced < aggressive
	assert.Less(t, minimal.RetryTotalTimeout, balanced.RetryTotalTimeout,
		"balanced should have longer timeout than minimal")
	assert.Less(t, balanced.RetryTotalTimeout, aggressive.RetryTotalTimeout,
		"aggressive should have longer timeout than balanced")
}
