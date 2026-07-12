package settings

import "testing"

// TestGoalEnabledScope guards the scope of goal.enabled: it must be
// ScopePlatform so admin/modules can toggle it globally (the toggle handler
// has no tenant_id context).  Per-tenant tuning keys (detection_mode, etc.)
// stay ScopeTenant.
func TestGoalEnabledScope(t *testing.T) {
	var found bool
	for _, sp := range GoalSpecs() {
		if sp.Key == "goal.enabled" {
			found = true
			if sp.Scope != ScopePlatform {
				t.Errorf("goal.enabled scope = %q, want %q (admin/modules toggle requires platform scope)",
					sp.Scope, ScopePlatform)
			}
		}
	}
	if !found {
		t.Fatal("goal.enabled spec not registered in GoalSpecs()")
	}
}

// TestSessionAnalyticsEnabledScope guards the scope of session_analytics.enabled.
// Same rationale as TestGoalEnabledScope: the master toggle must be
// ScopePlatform so admin/modules can flip it without a tenant_id.
func TestSessionAnalyticsEnabledScope(t *testing.T) {
	var found bool
	for _, sp := range SessionAnalyticsSpecs() {
		if sp.Key == "session_analytics.enabled" {
			found = true
			if sp.Scope != ScopePlatform {
				t.Errorf("session_analytics.enabled scope = %q, want %q (admin/modules toggle requires platform scope)",
					sp.Scope, ScopePlatform)
			}
		}
	}
	if !found {
		t.Fatal("session_analytics.enabled spec not registered in SessionAnalyticsSpecs()")
	}
}

// TestSessionAnalyticsTenantSpecsDoNotIncludeEnabled guards that the master
// toggle does NOT leak into the per-tenant list (which would re-register the
// same key twice).
func TestSessionAnalyticsTenantSpecsDoNotIncludeEnabled(t *testing.T) {
	for _, sp := range SessionAnalyticsTenantSpecs() {
		if sp.Key == "session_analytics.enabled" {
			t.Errorf("session_analytics.enabled must live only in SessionAnalyticsSpecs() (platform); found it in SessionAnalyticsTenantSpecs()")
		}
	}
}

// TestPlatformSpecsIncludesSessionAnalyticsEnabled ensures the platform master
// toggle is wired through PlatformSpecs() so it is registered at boot via
// settings.Init().
func TestPlatformSpecsIncludesSessionAnalyticsEnabled(t *testing.T) {
	var found bool
	for _, sp := range PlatformSpecs() {
		if sp.Key == "session_analytics.enabled" {
			found = true
			break
		}
	}
	if !found {
		t.Error("session_analytics.enabled must be included in PlatformSpecs()")
	}
}

// TestTenantSpecsIncludesSessionAnalyticsTenantSpecs ensures the per-tenant
// tuning keys are registered via TenantSpecs() at boot.
func TestTenantSpecsIncludesSessionAnalyticsTenantSpecs(t *testing.T) {
	var found bool
	for _, sp := range TenantSpecs() {
		if sp.Key == "session_analytics.model.title" {
			found = true
			break
		}
	}
	if !found {
		t.Error("session_analytics.model.title must be included in TenantSpecs()")
	}
}
