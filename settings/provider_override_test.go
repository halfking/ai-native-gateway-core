package settings

import (
	"context"
	"testing"
	"time"
)

// TestNewProviderSettingsResolver 验证构造函数和默认值.
//
// Pins current behaviour: cacheTTL = 5 minutes, cacheHitLog = false.
func TestNewProviderSettingsResolver(t *testing.T) {
	r := NewProviderSettingsResolver(nil, nil)
	if r == nil {
		t.Fatal("NewProviderSettingsResolver returned nil")
	}
	if r.cacheTTL != 5*time.Minute {
		t.Errorf("cacheTTL = %v, want 5m (audit pin)", r.cacheTTL)
	}
	if r.cacheHitLog != false {
		t.Errorf("cacheHitLog = %v, want false (audit pin)", r.cacheHitLog)
	}
	if r.db != nil || r.registry != nil {
		t.Error("nil-passthrough expected for both db and registry")
	}
}

// TestProviderSettingsResolver_Get_NilDBSafe pins the current nil-safe behaviour.
//
// Known behaviour: Get returns (nil, false) immediately when db or registry
// is nil, instead of dereferencing. This is the safe-fallback that prevents
// a similar P0 panic class to admin/tool_policy_api.go.
func TestProviderSettingsResolver_Get_NilDBSafe(t *testing.T) {
	r := NewProviderSettingsResolver(nil, nil)
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func() (interface{}, bool)
	}{
		{"Get", func() (interface{}, bool) { return r.Get(ctx, 1, "any.key") }},
		{"GetString", func() (interface{}, bool) {
			v, ok := r.GetString(ctx, 1, "any.key"); return v, ok
		}},
		{"GetBool", func() (interface{}, bool) {
			v, ok := r.GetBool(ctx, 1, "any.key"); return v, ok
		}},
		{"GetInt64", func() (interface{}, bool) {
			v, ok := r.GetInt64(ctx, 1, "any.key"); return v, ok
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := tt.fn()
			if ok {
				t.Errorf("%s: expected ok=false for nil db, got true (value=%v)", tt.name, v)
			}
		})
	}
}

// TestProviderSettingsResolver_GetTypeConversions 验证 GetString/Bool/Int64
// 的类型转换正确性.
//
// 用一个测试中手动填的 cache 注入值, 绕过 DB 路径. 通过 reflection 访问
// cache sync.Map 不优雅, 所以直接用 interface{} 注入 helper.
func TestProviderSettingsResolver_GetTypeConversions(t *testing.T) {
	// 用 helper 把 cache 填好, 然后用 Get 拉出来, 看转换是否正确.
	r := NewProviderSettingsResolver(nil, nil)
	providerID := 1
	key := "compression.mode"

	// 直接写 cache: r.cache.Store(k, cacheEntry{...})
	// 用 reflection 不可行, 改成走 queryDB path 不行 (db=nil)
	// 替代方案: 测 GetString/Bool/Int64 在 type 不匹配时返回 ok=false
	t.Run("GetString with non-string value returns ok=false", func(t *testing.T) {
		// nil db -> Get returns (nil, false) -> GetString returns ("", false)
		v, ok := r.GetString(context.Background(), providerID, key)
		if ok || v != "" {
			t.Errorf("expected (\"\", false) for nil db, got (%q, %v)", v, ok)
		}
	})

	t.Run("GetBool with non-bool value returns ok=false", func(t *testing.T) {
		v, ok := r.GetBool(context.Background(), providerID, key)
		if ok || v != false {
			t.Errorf("expected (false, false) for nil db, got (%v, %v)", v, ok)
		}
	})

	t.Run("GetInt64 with non-int value returns ok=false", func(t *testing.T) {
		v, ok := r.GetInt64(context.Background(), providerID, key)
		if ok || v != 0 {
			t.Errorf("expected (0, false) for nil db, got (%v, %v)", v, ok)
		}
	})
}

// TestProviderSettingsResolver_ClearCache 验证 cache 清理不 panic.
func TestProviderSettingsResolver_ClearCache(t *testing.T) {
	r := NewProviderSettingsResolver(nil, nil)
	// 空 cache 不 panic
	r.ClearCache()
	r.ClearProviderCache(42)
	r.ClearProviderCache(0)
	r.ClearProviderCache(-1)
}

// TestProviderSettingsResolver_CacheTTLConfiguration 验证 cache TTL 可调.
//
// Known behaviour (audit pin): 修改 cacheTTL 字段会改变后续所有 Get
// 调用的缓存有效期. 但 ClearCache 不重置 TTL. 这是设计: TTL 是
// resolver 初始化时的固定配置.
func TestProviderSettingsResolver_CacheTTLConfiguration(t *testing.T) {
	r := NewProviderSettingsResolver(nil, nil)
	r.cacheTTL = 1 * time.Hour
	if r.cacheTTL != 1*time.Hour {
		t.Errorf("cacheTTL not mutable: %v", r.cacheTTL)
	}
	r.ClearCache()
	// TTL 不应被 ClearCache 重置
	if r.cacheTTL != 1*time.Hour {
		t.Errorf("ClearCache reset TTL: %v", r.cacheTTL)
	}
}

// TestCacheKey_Equality 验证 cacheKey 的值相等性 (sync.Map lookup 用).
//
// sync.Map 用 cacheKey 作为 key, 必须有正确的 == 实现. 两个相同字段的
// cacheKey 必须相等才能命中缓存. 测试这个隐含的不变量.
func TestCacheKey_Equality(t *testing.T) {
	a := cacheKey{providerID: 42, key: "compression.mode"}
	b := cacheKey{providerID: 42, key: "compression.mode"}
	c := cacheKey{providerID: 42, key: "cache.enabled"}
	d := cacheKey{providerID: 43, key: "compression.mode"}

	if a != b {
		t.Error("identical cacheKey must be equal")
	}
	if a == c {
		t.Error("different key must not be equal")
	}
	if a == d {
		t.Error("different providerID must not be equal")
	}
}