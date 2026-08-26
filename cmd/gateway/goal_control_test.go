package main

import (
	"testing"

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
