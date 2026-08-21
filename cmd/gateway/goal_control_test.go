package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestSettingsAdapterResolvesCorrectScope ensures settingsAdapter.settingScope
// returns ScopePlatform for goal.enabled / session_analytics.enabled (so
// EffectiveValue reads the platform-scoped value without requiring tenant_id),
// and ScopeTenant for per-tenant tuning keys (goal.detection_mode, etc.).
func TestSettingsAdapterResolvesCorrectScope(t *testing.T) {
	// Wire up a minimal registry with the two master toggles + one tenant key.
	registry := settings.NewRegistry()
	registry.MustRegisterSpec(&settings.Spec{
		Key:   "goal.enabled",
		Scope: settings.ScopePlatform,
		Type:  settings.TypeBool,
	})
	registry.MustRegisterSpec(&settings.Spec{
		Key:   "session_analytics.enabled",
		Scope: settings.ScopePlatform,
		Type:  settings.TypeBool,
	})
	registry.MustRegisterSpec(&settings.Spec{
		Key:   "goal.detection_mode",
		Scope: settings.ScopeTenant,
		Type:  settings.TypeEnum,
	})
	settings.Global = registry

	adapter := settingsAdapter{}

	cases := []struct {
		key       string
		wantScope settings.Scope
	}{
		{"goal.enabled", settings.ScopePlatform},
		{"session_analytics.enabled", settings.ScopePlatform},
		{"goal.detection_mode", settings.ScopeTenant},
		{"unknown.key", settings.ScopeTenant}, // default fallback
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := adapter.settingScope(tc.key)
			if got != tc.wantScope {
				t.Errorf("settingScope(%q) = %v, want %v", tc.key, got, tc.wantScope)
			}
		})
	}
}

// TestEnforceFollowUpDepthBudgetInvariant covers the budget-exhaustion
// invariant helper that lifts MaxFollowUpDepth when preset-driven defaults
// (e.g. balanced MaxAutoContinueCount=5, aggressive MaxAutoContinueCount=10)
// violate the documented minimum required for budgetExhausted to fire.
//
// See 903dc8b4d follow-up audit: spec defaults were aligned, but the
// preset-driven boot path at cmd/gateway/goal_control.go still allows
// MaxAutoContinueCount > MaxFollowUpDepth/(MaxModelSwitchCount+1). Without
// the invariant helper, the depth guardrail would silently truncate the
// loop before budgetExhausted is reached — defeating the budget-exhaustion
// model-switch branch.
func TestEnforceFollowUpDepthBudgetInvariant(t *testing.T) {
	cases := []struct {
		name              string
		continueCount     int
		switchCount       int
		startDepth        int
		expectedUnchanged bool
		expectedNewDepth  int
	}{
		{
			name:              "balanced_preset_violates_default_depth",
			continueCount:     5,
			switchCount:       3,
			startDepth:        15, // 903dc8b4d spec default; 5*(3+1)=20 > 15
			expectedUnchanged: false,
			expectedNewDepth:  20 + minBudgetExhaustionMargin,
		},
		{
			name:              "aggressive_preset_violates_default_depth",
			continueCount:     10,
			switchCount:       5,
			startDepth:        15,
			expectedUnchanged: false,
			expectedNewDepth:  10*6 + minBudgetExhaustionMargin,
		},
		{
			name:              "invariant_already_satisfied",
			continueCount:     3,
			switchCount:       3,
			startDepth:        50, // 3*4=12; 50 ≥ 12
			expectedUnchanged: true,
			expectedNewDepth:  50,
		},
		{
			name:              "boundary_equal_required",
			continueCount:     3,
			switchCount:       4,
			startDepth:        15, // 3*5=15; depth exactly == required
			expectedUnchanged: true,
			expectedNewDepth:  15,
		},
		{
			name:              "auto_continue_disabled_no_op",
			continueCount:     0,
			switchCount:       3,
			startDepth:        15,
			expectedUnchanged: true,
			expectedNewDepth:  15,
		},
		{
			name:              "model_switch_disabled_no_op",
			continueCount:     5,
			switchCount:       0,
			startDepth:        1, // 5*1=5 > 1; but switch disabled, invariant does not apply
			// Wait: with switchCount=0, required = 5*(0+1)=5, startDepth=1 < 5 → violates.
			// Actually we treat switchCount=0 as the "still bounded path": a single
			// model goes through 5 continues, depth=5 is required. Helper should lift.
			expectedUnchanged: false,
			expectedNewDepth:  5 + minBudgetExhaustionMargin,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &goal.ModeConfig{
				MaxAutoContinueCount: tc.continueCount,
				MaxModelSwitchCount:  tc.switchCount,
				MaxFollowUpDepth:     tc.startDepth,
			}
			enforceFollowUpDepthBudgetInvariant(cfg)
			if cfg.MaxFollowUpDepth != tc.expectedNewDepth {
				t.Errorf("depth = %d, want %d", cfg.MaxFollowUpDepth, tc.expectedNewDepth)
			}
		})
	}
}

// TestEnforceFollowUpDepthBudgetInvariant_NilSafe ensures the helper is
// safe to call before the config is fully populated (e.g. when an early
// validation path hands in a nil pointer).
func TestEnforceFollowUpDepthBudgetInvariant_NilSafe(t *testing.T) {
	enforceFollowUpDepthBudgetInvariant(nil) // must not panic
}
