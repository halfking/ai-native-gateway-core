package settings

import (
	"testing"
)

// 2026-10-30 magpie 对比轮（docs/research/2026-10-30-magpie四域对比刷新与
// 优化方案.md §四）：routing.max_candidates / survival.attempt_budget 两键的
// 注册与读取契约。行为零漂移红线：未配置（DB 无值）时读数必须与改造前的
// 硬编码逐字节同值（12 / 100）。

func newPlanTestRegistry(t *testing.T, store map[string][]byte) *Registry {
	t.Helper()
	registry := NewRegistry()
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: store})
	for _, sp := range RoutingPlanSpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	for _, sp := range SurvivalSpecs() {
		if sp.Key != KeySurvivalAttemptBudget {
			continue
		}
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	return registry
}

func TestRoutingPlanSpecs_Registered(t *testing.T) {
	want := map[string]bool{
		KeyRoutingMaxCandidates: false,
	}
	for _, sp := range PlatformSpecs() {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
			if !sp.HotReload {
				t.Errorf("%s.HotReload = false, want true (read every plan)", sp.Key)
			}
			if sp.Scope != ScopePlatform {
				t.Errorf("%s.Scope = %s, want platform", sp.Key, sp.Scope)
			}
			if sp.EnvName != EnvNameAuto(sp.Key) {
				t.Errorf("%s.EnvName = %q, want %q", sp.Key, sp.EnvName, EnvNameAuto(sp.Key))
			}
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PlatformSpecs() missing %s", k)
		}
	}
	// attempt_budget 键也必须注册在 SurvivalSpecs 里。
	foundBudget := false
	for _, sp := range SurvivalSpecs() {
		if sp.Key == KeySurvivalAttemptBudget {
			foundBudget = true
			if !sp.HotReload {
				t.Errorf("%s.HotReload = false, want true (read per request)", sp.Key)
			}
		}
	}
	if !foundBudget {
		t.Error("SurvivalSpecs() missing survival.attempt_budget")
	}
}

func TestRoutingMaxCandidates_DefaultMatchesLegacyHardcode(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })
	Global = newPlanTestRegistry(t, map[string][]byte{})
	if got := RoutingMaxCandidates(); got != 0 {
		t.Fatalf("RoutingMaxCandidates() unset = %d, want 0 (consumer falls back to legacy 12)", got)
	}
}

func TestRoutingMaxCandidates_OverrideAndClamp(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"override", `24`, 24},
		{"below min clamps", `1`, 4},
		{"above max clamps", `100`, 48},
		{"zero means unset", `0`, 0},
		{"negative means unset", `-5`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Global = newPlanTestRegistry(t, map[string][]byte{
				KeyRoutingMaxCandidates: []byte(tc.raw),
			})
			if got := RoutingMaxCandidates(); got != tc.want {
				t.Fatalf("RoutingMaxCandidates(%s) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSurvivalAttemptBudget_DefaultMatchesLegacyHardcode(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })
	Global = newPlanTestRegistry(t, map[string][]byte{})
	if got := SurvivalAttemptBudget(); got != 0 {
		t.Fatalf("SurvivalAttemptBudget() unset = %d, want 0 (consumer falls back to legacy 100)", got)
	}
}

func TestSurvivalAttemptBudget_OverrideAndClamp(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"override", `200`, 200},
		{"below min clamps", `1`, 10},
		{"above max clamps", `1000`, 601},
		{"max equals executors ceiling", `601`, 601},
		{"zero means unset", `0`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Global = newPlanTestRegistry(t, map[string][]byte{
				KeySurvivalAttemptBudget: []byte(tc.raw),
			})
			if got := SurvivalAttemptBudget(); got != tc.want {
				t.Fatalf("SurvivalAttemptBudget(%s) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
