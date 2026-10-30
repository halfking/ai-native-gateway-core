package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// 2026-10-30 magpie 对比轮（docs/research/2026-10-30-magpie四域对比刷新与
// 优化方案.md §四 P0-2）：RequestAttemptLimit 的读数契约。
//
// ★ 默认行为零漂移红线：settings_kv 无值时读数必须等于旧硬编码
//   DefaultUpstreamAttemptLimit=100（NewUpstreamAttemptBudget(100) 的
//   消费语义由既有 attempt_budget_test.go 覆盖，此处只钉「读数点」）。

func newAttemptBudgetTestRegistry(t *testing.T, raw string) {
	t.Helper()
	registry := settings.NewRegistry()
	store := map[string][]byte{}
	if raw != "" {
		store[settings.KeySurvivalAttemptBudget] = []byte(raw)
	}
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	for _, sp := range settings.SurvivalSpecs() {
		if sp.Key != settings.KeySurvivalAttemptBudget {
			continue
		}
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	settings.Global = registry
}

// fakeSettingsBackend mirrors settings.fakeBackend (registry_test.go, unexported)
// for the cross-package consumer test. jsonRawMessage = []byte (type alias),
// so []byte values satisfy the interface.
type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f *fakeSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *fakeSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func TestRequestAttemptLimit_DefaultMatchesLegacyHardcode(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	newAttemptBudgetTestRegistry(t, "")
	if got := RequestAttemptLimit(); got != DefaultUpstreamAttemptLimit {
		t.Fatalf("RequestAttemptLimit() unset = %d, want %d (legacy hardcode)", got, DefaultUpstreamAttemptLimit)
	}
}

func TestRequestAttemptLimit_OverrideTakesEffect(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	newAttemptBudgetTestRegistry(t, `30`)
	if got := RequestAttemptLimit(); got != 30 {
		t.Fatalf("RequestAttemptLimit() = %d, want 30", got)
	}

	// 热加载：改 DB 值后下一次读数立即生效（GetPlatformInt 无缓存层）。
	newAttemptBudgetTestRegistry(t, `250`)
	if got := RequestAttemptLimit(); got != 250 {
		t.Fatalf("RequestAttemptLimit() after hot change = %d, want 250", got)
	}
}

func TestRequestAttemptLimit_OverrideFeedsBudget(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	newAttemptBudgetTestRegistry(t, `10`)
	if got := RequestAttemptLimit(); got != 10 {
		t.Fatalf("RequestAttemptLimit() = %d, want 10 (raw 2 clamps to min 10)", got)
	}
	b := NewUpstreamAttemptBudget(RequestAttemptLimit())
	for i := 0; i < 10; i++ {
		if !b.TryConsume() {
			t.Fatalf("attempt %d of 10 must be admitted", i+1)
		}
	}
	if b.TryConsume() {
		t.Fatal("budget of 10 must refuse the 11th attempt")
	}
}
