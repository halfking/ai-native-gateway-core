package settings

import "testing"

// TestStorageSessionGraySwitches validates the storage-plan-v2 S1b gray-scale
// switches (migration 707/708): both must be platform-scoped booleans in the
// storage category, defaulting to OFF so upgrades are write-neutral until an
// operator opts in (回滚 = 关开关, plan §7).
func TestStorageSessionGraySwitches(t *testing.T) {
	specs := StorageSpecs()

	byKey := make(map[string]*Spec, len(specs))
	for _, sp := range specs {
		byKey[sp.Key] = sp
	}

	for _, key := range []string{
		"storage.session_turns_bodies_enabled",
		"storage.session_final_full_enabled",
	} {
		sp, ok := byKey[key]
		if !ok {
			t.Errorf("missing spec %s", key)
			continue
		}
		if sp.Type != TypeBool {
			t.Errorf("%s: type = %v, want bool", key, sp.Type)
		}
		if sp.Scope != ScopePlatform {
			t.Errorf("%s: scope = %v, want platform", key, sp.Scope)
		}
		if sp.Category != CategoryStorage {
			t.Errorf("%s: category = %v, want storage", key, sp.Category)
		}
		if sp.Default != false {
			t.Errorf("%s: default = %v, want false (S1b 灰度默认关)", key, sp.Default)
		}
		if !sp.HotReload {
			t.Errorf("%s: must be hot-reloadable", key)
		}
	}
}
