package settings

import (
	"testing"
)

// TestTieredExemptionGetters 验证便捷读取口存在且默认值为 true（存量行为）。
func TestTieredExemptionGetters(t *testing.T) {
	// 默认值（无任何设置时）应为 true（存量豁免行为）
	if !TieredExemptDefaultTenant() {
		t.Error("TieredExemptDefaultTenant() default should be true")
	}
	if !TieredExemptInternalKeys() {
		t.Error("TieredExemptInternalKeys() default should be true")
	}
}

// TestTieredExemptionSpecs 验证 TieredQuotaExemptionSpecs 返回 2 个正确的 spec。
func TestTieredExemptionSpecs(t *testing.T) {
	specs := TieredQuotaExemptionSpecs()
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
	
	keys := make(map[string]*Spec)
	for _, s := range specs {
		keys[s.Key] = s
	}
	
	// 验证两个 key 存在
	defaultTenant := keys["quota.tiered_exempt_default_tenant"]
	if defaultTenant == nil {
		t.Fatal("quota.tiered_exempt_default_tenant spec not found")
	}
	if defaultTenant.Type != TypeBool {
		t.Errorf("default_tenant: expected TypeBool, got %v", defaultTenant.Type)
	}
	if defaultTenant.Default != true {
		t.Errorf("default_tenant: expected Default=true, got %v", defaultTenant.Default)
	}
	if defaultTenant.Scope != ScopePlatform {
		t.Errorf("default_tenant: expected ScopePlatform, got %v", defaultTenant.Scope)
	}
	if !defaultTenant.HotReload {
		t.Error("default_tenant: expected HotReload=true")
	}
	
	internalKeys := keys["quota.tiered_exempt_internal_keys"]
	if internalKeys == nil {
		t.Fatal("quota.tiered_exempt_internal_keys spec not found")
	}
	if internalKeys.Type != TypeBool {
		t.Errorf("internal_keys: expected TypeBool, got %v", internalKeys.Type)
	}
	if internalKeys.Default != true {
		t.Errorf("internal_keys: expected Default=true, got %v", internalKeys.Default)
	}
	if internalKeys.Scope != ScopePlatform {
		t.Errorf("internal_keys: expected ScopePlatform, got %v", internalKeys.Scope)
	}
	if !internalKeys.HotReload {
		t.Error("internal_keys: expected HotReload=true")
	}
}
