package settings

import (
	"testing"
)

// Wave 2 任务一（2026-09-22 设计差距审计 C5/C8/C9）钉桩测试：
// 阈值常量表注册、默认值=集中前各包 const、clamp 边界、热更生效。

// TestThresholdSpecs_RegisteredInPlatformSpecs — PlatformSpecs() 必须包含
// 五个阈值键，否则注册链断裂、GetPlatformInt 永远走 fallback、热更全死
// （同 TestAutoSummarySpecs_RegisteredInPlatformSpecs 的守卫逻辑）。
func TestThresholdSpecs_RegisteredInPlatformSpecs(t *testing.T) {
	want := map[string]bool{
		KeyNodeFailStreakLimit:         false,
		KeyNodeDisabledCooldownSeconds: false,
		KeyStickyFailureThreshold:      false,
		KeyFpSlotTTLSeconds:            false,
		KeyErrorProbeWorkers:           false,
	}
	for _, sp := range PlatformSpecs() {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PlatformSpecs() missing %s — specs.go registration is stale", k)
		}
	}
}

// TestThresholdSpecs_DefaultsMatchLegacyConstants — 默认值必须等于集中前
// 的各包 const（行为零漂移红线），且 default 落在 [Min,Max] 内。
func TestThresholdSpecs_DefaultsMatchLegacyConstants(t *testing.T) {
	cases := []struct {
		key      string
		def      int
		hot      bool
		min, max float64
	}{
		{KeyNodeFailStreakLimit, DefaultNodeFailStreakLimit, true, 1, 10},
		{KeyNodeDisabledCooldownSeconds, DefaultNodeDisabledCooldownSeconds, true, 30, 7200},
		{KeyStickyFailureThreshold, DefaultStickyFailureThreshold, true, 1, 10},
		{KeyFpSlotTTLSeconds, DefaultFpSlotTTLSeconds, true, 300, 86400},
		{KeyErrorProbeWorkers, DefaultErrorProbeWorkers, false, 1, float64(MaxErrorProbeWorkers)},
	}
	byKey := map[string]*Spec{}
	for _, sp := range ThresholdSpecs() {
		byKey[sp.Key] = sp
	}
	for _, tc := range cases {
		sp, ok := byKey[tc.key]
		if !ok {
			t.Fatalf("ThresholdSpecs() missing %s", tc.key)
		}
		d, ok := sp.Default.(int)
		if !ok {
			t.Fatalf("%s.Default = %v (%T), want int", tc.key, sp.Default, sp.Default)
		}
		if d != tc.def {
			t.Errorf("%s.Default = %d, want %d (legacy const)", tc.key, d, tc.def)
		}
		if d < int(tc.min) || d > int(tc.max) {
			t.Errorf("%s.Default = %d outside [%v, %v]", tc.key, d, tc.min, tc.max)
		}
		if sp.HotReload != tc.hot {
			t.Errorf("%s.HotReload = %v, want %v", tc.key, sp.HotReload, tc.hot)
		}
		if sp.EnvName != EnvNameAuto(tc.key) {
			t.Errorf("%s.EnvName = %q, want %q", tc.key, sp.EnvName, EnvNameAuto(tc.key))
		}
	}
}

// TestThresholdAccessors_HotReloadAndClamp — 交换 Global 后：
// 空 store → 默认值；写入新值（+显式失效缓存）→ 立即可见；
// 写入越界值 → 访问器 clamp 回安全区间（env/手改 settings_kv 防线）。
func TestThresholdAccessors_HotReloadAndClamp(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })

	store := map[string][]byte{}
	registry := NewRegistry()
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: store})
	for _, sp := range ThresholdSpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	Global = registry
	t.Cleanup(func() {
		for _, sp := range ThresholdSpecs() {
			InvalidatePlatformValue(sp.Key)
		}
	})

	// 1) 默认值。
	if got := NodeFailStreakLimit(); got != DefaultNodeFailStreakLimit {
		t.Errorf("NodeFailStreakLimit() = %d, want %d", got, DefaultNodeFailStreakLimit)
	}
	if got := NodeDisabledCooldownSeconds(); got != DefaultNodeDisabledCooldownSeconds {
		t.Errorf("NodeDisabledCooldownSeconds() = %d, want %d", got, DefaultNodeDisabledCooldownSeconds)
	}
	if got := StickyFailureThreshold(); got != DefaultStickyFailureThreshold {
		t.Errorf("StickyFailureThreshold() = %d, want %d", got, DefaultStickyFailureThreshold)
	}
	if got := FpSlotTTLSeconds(); got != DefaultFpSlotTTLSeconds {
		t.Errorf("FpSlotTTLSeconds() = %d, want %d", got, DefaultFpSlotTTLSeconds)
	}
	if got := ErrorProbeWorkers(); got != DefaultErrorProbeWorkers {
		t.Errorf("ErrorProbeWorkers() = %d, want %d", got, DefaultErrorProbeWorkers)
	}

	setKey := func(key string, raw string) {
		store[key] = []byte(raw)
		InvalidatePlatformValue(key)
	}

	// 2) 热更生效（热路径缓存读者写入后经 InvalidatePlatformValue 立即可见）。
	setKey(KeyNodeFailStreakLimit, `5`)
	if got := NodeFailStreakLimit(); got != 5 {
		t.Errorf("NodeFailStreakLimit() = %d, want 5 after hot update", got)
	}
	setKey(KeyFpSlotTTLSeconds, `900`)
	if got := FpSlotTTLSeconds(); got != 900 {
		t.Errorf("FpSlotTTLSeconds() = %d, want 900 after hot update", got)
	}
	setKey(KeyStickyFailureThreshold, `4`)
	if got := StickyFailureThreshold(); got != 4 {
		t.Errorf("StickyFailureThreshold() = %d, want 4 after hot update", got)
	}
	setKey(KeyNodeDisabledCooldownSeconds, `600`)
	if got := NodeDisabledCooldownSeconds(); got != 600 {
		t.Errorf("NodeDisabledCooldownSeconds() = %d, want 600 after hot update", got)
	}
	setKey(KeyErrorProbeWorkers, `3`)
	if got := ErrorProbeWorkers(); got != 3 {
		t.Errorf("ErrorProbeWorkers() = %d, want 3 after hot update", got)
	}

	// 3) 越界值被访问器 clamp（spec.Min/Max 只拦管理端 PUT，env 与
	// 手改 settings_kv 不经校验——防线必须在读取侧）。
	setKey(KeyNodeFailStreakLimit, `999`)
	if got := NodeFailStreakLimit(); got != 10 {
		t.Errorf("NodeFailStreakLimit() = %d, want clamped 10", got)
	}
	setKey(KeyNodeDisabledCooldownSeconds, `1`)
	if got := NodeDisabledCooldownSeconds(); got != 30 {
		t.Errorf("NodeDisabledCooldownSeconds() = %d, want clamped 30", got)
	}
	setKey(KeyFpSlotTTLSeconds, `1`)
	if got := FpSlotTTLSeconds(); got != 300 {
		t.Errorf("FpSlotTTLSeconds() = %d, want clamped 300", got)
	}
	setKey(KeyErrorProbeWorkers, `50`)
	if got := ErrorProbeWorkers(); got != MaxErrorProbeWorkers {
		t.Errorf("ErrorProbeWorkers() = %d, want clamped %d", got, MaxErrorProbeWorkers)
	}
}

// TestThresholdAccessors_NilGlobal — Global 未初始化（如部分单测环境）时
// 访问器必须返回默认值而不是 panic。
func TestThresholdAccessors_NilGlobal(t *testing.T) {
	prevGlobal := Global
	t.Cleanup(func() { Global = prevGlobal })
	Global = nil

	if got := NodeFailStreakLimit(); got != DefaultNodeFailStreakLimit {
		t.Errorf("NodeFailStreakLimit() = %d, want %d", got, DefaultNodeFailStreakLimit)
	}
	if got := ErrorProbeWorkers(); got != DefaultErrorProbeWorkers {
		t.Errorf("ErrorProbeWorkers() = %d, want %d", got, DefaultErrorProbeWorkers)
	}
}
