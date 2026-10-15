package settings

import (
	"testing"
)

// spec_survival_test.go — P0-2（2026-10-14）：survival.interactive_deadline_
// seconds 的注册与读取契约。
//
// r1027 教训：键不进 PlatformSpecs() 聚合点时，GetPlatformInt 会静默回退
// 默认值（Global.Spec(key)==nil → fallback），键永远不可翻转。本测试把
// 「已注册」钉成硬断言。

// TestSurvivalSpecs_RegisteredAndHotReloadable pins the aggregation-point
// registration (specs.go), the hot-reload flag and the [10, 18000] range.
func TestSurvivalSpecs_RegisteredAndHotReloadable(t *testing.T) {
	found := false
	for _, sp := range PlatformSpecs() {
		if sp.Key != KeySurvivalInteractiveDeadlineSeconds {
			continue
		}
		found = true
		if !sp.HotReload {
			t.Errorf("%s.HotReload = false, want true (the deadline must be tunable without a redeploy)", sp.Key)
		}
		if sp.Scope != ScopePlatform {
			t.Errorf("%s.Scope = %q, want platform", sp.Key, sp.Scope)
		}
		if sp.Type != TypeInt {
			t.Errorf("%s.Type = %q, want int", sp.Key, sp.Type)
		}
		if sp.Min == nil || *sp.Min != 10 {
			t.Errorf("%s.Min = %v, want 10", sp.Key, sp.Min)
		}
		if sp.Max == nil || *sp.Max != 18000 {
			t.Errorf("%s.Max = %v, want 18000 (the historical 5h ceiling stays legal)", sp.Key, sp.Max)
		}
		if sp.DangerLevel != Dangerous {
			t.Errorf("%s.DangerLevel = %v, want Dangerous (changes the max wait of every streaming request)", sp.Key, sp.DangerLevel)
		}
	}
	if !found {
		t.Fatalf("PlatformSpecs() missing %s — GetPlatformInt would silently fall back to the default and the override would never engage (r1027 lesson)", KeySurvivalInteractiveDeadlineSeconds)
	}
}

// TestSurvivalInteractiveDeadlineSeconds_Semantics pins the accessor
// contract: 0/unset → 0 (fall back to the config value, never clamp the
// sentinel up to Min), positive values clamp into [10, 18000], and a DB row
// outranks the env fallback.
func TestSurvivalInteractiveDeadlineSeconds_Semantics(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	store := map[string][]byte{}
	registry := NewRegistry()
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: store})
	for _, sp := range SurvivalSpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	Global = registry

	t.Run("unset sentinel stays 0", func(t *testing.T) {
		if got := SurvivalInteractiveDeadlineSeconds(); got != 0 {
			t.Fatalf("unset key = %d, want 0 (sentinel must not be clamped up to Min=10 — that would silently override every deployment config)", got)
		}
	})

	t.Run("in-range value passes through", func(t *testing.T) {
		store[KeySurvivalInteractiveDeadlineSeconds] = []byte(`240`)
		if got := SurvivalInteractiveDeadlineSeconds(); got != 240 {
			t.Fatalf("db 240 = %d, want 240", got)
		}
	})

	t.Run("out-of-range values clamp into [10, 18000]", func(t *testing.T) {
		store[KeySurvivalInteractiveDeadlineSeconds] = []byte(`5`)
		if got := SurvivalInteractiveDeadlineSeconds(); got != 10 {
			t.Fatalf("db 5 = %d, want clamped 10", got)
		}
		store[KeySurvivalInteractiveDeadlineSeconds] = []byte(`999999`)
		if got := SurvivalInteractiveDeadlineSeconds(); got != 18000 {
			t.Fatalf("db 999999 = %d, want clamped 18000", got)
		}
	})

	t.Run("explicit zero row keeps the fallback semantics", func(t *testing.T) {
		// An operator writing 0 back must be able to return to the config
		// value — the accessor must treat it exactly like unset.
		store[KeySurvivalInteractiveDeadlineSeconds] = []byte(`0`)
		if got := SurvivalInteractiveDeadlineSeconds(); got != 0 {
			t.Fatalf("db 0 = %d, want 0", got)
		}
	})
}
