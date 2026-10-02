package settings

import (
	"testing"
)

// Wave 3 B5 adjudication tests: the four error_probe.* keys were registered
// in an earlier wave without consumers (UI-editable no-ops). They are now
// wired at construction time in cmd/gateway/main.go via the accessors below,
// mirroring the Wave 2 error_probe.workers precedent (startup-captured ⇒
// HotReload=false).

func TestErrorProbeSpecs_StartupCapturedAndRegistered(t *testing.T) {
	want := map[string]bool{
		KeyErrorProbeEnabled:              false,
		KeyErrorProbeConsecutiveThreshold: false,
		KeyErrorProbeMaxAttempts:          false,
		KeyErrorProbeTimeoutMs:            false,
	}
	for _, sp := range PlatformSpecs() {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
			if sp.HotReload {
				t.Errorf("%s.HotReload = true, want false (value is captured at worker construction)", sp.Key)
			}
			if sp.EnvName != EnvNameAuto(sp.Key) {
				t.Errorf("%s.EnvName = %q, want %q (env vars must keep working through the registry)", sp.Key, sp.EnvName, EnvNameAuto(sp.Key))
			}
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PlatformSpecs() missing %s", k)
		}
	}
}

// TestErrorProbeAccessors_DefaultsMatchRuntimeReality — 默认值必须等于接线
// 前的硬编码运行值（行为零漂移红线）。timeout 的历史登记默认 10000 从未生效，
// 已裁决对齐运行现实 30000（见 ErrorProbeTimeoutMs 注释），此处钉桩防回退。
func TestErrorProbeAccessors_DefaultsMatchRuntimeReality(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	registry := NewRegistry()
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: map[string][]byte{}})
	for _, sp := range ErrorProbeSpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	Global = registry

	if !ErrorProbeEnabled() {
		t.Error("ErrorProbeEnabled() = false, want true (legacy default)")
	}
	if got := ErrorProbeConsecutiveThreshold(); got != 2 {
		t.Errorf("ErrorProbeConsecutiveThreshold() = %d, want 2 (legacy default)", got)
	}
	if got := ErrorProbeMaxAttempts(); got != 5 {
		t.Errorf("ErrorProbeMaxAttempts() = %d, want 5 (legacy default)", got)
	}
	if got := ErrorProbeTimeoutMs(); got != 30000 {
		t.Errorf("ErrorProbeTimeoutMs() = %d, want 30000 (legacy runtime value)", got)
	}
}

// TestErrorProbeAccessors_ClampAndPriority — 手改 settings_kv 越界值须被
// clamp；DB 行优先于 env；env 的 "0" 关闭惯例（bool JSON 解析不认 "0"）。
func TestErrorProbeAccessors_ClampAndPriority(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	store := map[string][]byte{}
	registry := NewRegistry()
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: store})
	registry.RegisterBackend(EnvBackendScope, NewStoreEnv())
	for _, sp := range ErrorProbeSpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	Global = registry

	t.Run("out-of-range db values clamp", func(t *testing.T) {
		store[KeyErrorProbeConsecutiveThreshold] = []byte(`99`)
		store[KeyErrorProbeMaxAttempts] = []byte(`0`)
		store[KeyErrorProbeTimeoutMs] = []byte(`999999`)
		if got := ErrorProbeConsecutiveThreshold(); got != 10 {
			t.Errorf("threshold clamp: got %d, want 10", got)
		}
		if got := ErrorProbeMaxAttempts(); got != 1 {
			t.Errorf("max_attempts clamp: got %d, want 1", got)
		}
		if got := ErrorProbeTimeoutMs(); got != 60000 {
			t.Errorf("timeout clamp: got %d, want 60000", got)
		}
	})

	t.Run("env zero convention disables probe", func(t *testing.T) {
		delete(store, KeyErrorProbeEnabled)
		t.Setenv("LLM_GATEWAY_ERROR_PROBE_ENABLED", "0")
		if ErrorProbeEnabled() {
			t.Error(`ErrorProbeEnabled() = true with env "0", want false (legacy idiom)`)
		}
	})

	t.Run("db row wins over env", func(t *testing.T) {
		store[KeyErrorProbeEnabled] = []byte(`true`)
		t.Setenv("LLM_GATEWAY_ERROR_PROBE_ENABLED", "false")
		if !ErrorProbeEnabled() {
			t.Error("ErrorProbeEnabled() = false, want true (DB row must outrank env)")
		}
	})

	t.Run("db false wins over env zero", func(t *testing.T) {
		store[KeyErrorProbeEnabled] = []byte(`false`)
		t.Setenv("LLM_GATEWAY_ERROR_PROBE_ENABLED", "1")
		if ErrorProbeEnabled() {
			t.Error("ErrorProbeEnabled() = true, want false (DB row must outrank env)")
		}
	})
}
