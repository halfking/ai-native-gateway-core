package settings

import "testing"

// TestProjectAttributionSpecRegisteredInPlatformSpecs locks down the spec
// being visible via PlatformSpecs(); without this, operators cannot find or
// toggle it from the admin UI and "default off" silently degrades to
// "not registered at all".
func TestProjectAttributionSpecRegisteredInPlatformSpecs(t *testing.T) {
	found := false
	for _, sp := range PlatformSpecs() {
		if sp.Key == "project_attribution.enabled" {
			found = true
			if sp.Type != TypeBool {
				t.Errorf("type = %v, want TypeBool", sp.Type)
			}
			if sp.Scope != ScopePlatform {
				t.Errorf("scope = %v, want ScopePlatform", sp.Scope)
			}
			if v, ok := sp.Default.(bool); !ok || v != false {
				t.Errorf("default = %v, want false", sp.Default)
			}
			if sp.HotReload {
				t.Error("HotReload must be false: enabling the flag without resolver / CloseHook would leave an inconsistent state")
			}
			break
		}
	}
	if !found {
		t.Fatal("project_attribution.enabled is not registered in PlatformSpecs")
	}
}

func TestProjectAttributionSpec_DefaultsOff(t *testing.T) {
	// 通过 GetPlatformBool 验证默认值是 false（fail-closed）。
	got := GetPlatformBool("project_attribution.enabled", false)
	if got {
		t.Error("default of project_attribution.enabled must be false")
	}
}
