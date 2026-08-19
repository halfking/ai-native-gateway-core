package settings

import "testing"

func TestStatsShadowSpecsRegistered(t *testing.T) {
	want := map[string]any{
		"stats.shadow_read.enabled":        false,
		"stats.shadow_read.sample_percent": 0,
		"stats.shadow_read.timeout_ms":     750,
	}
	got := map[string]*Spec{}
	for _, spec := range PlatformSpecs() {
		got[spec.Key] = spec
	}
	for key, defaultValue := range want {
		spec := got[key]
		if spec == nil {
			t.Errorf("PlatformSpecs() missing %s", key)
			continue
		}
		if spec.Default != defaultValue {
			t.Errorf("%s default = %v, want %v", key, spec.Default, defaultValue)
		}
		if !spec.HotReload {
			t.Errorf("%s must support hot reload", key)
		}
	}
}
