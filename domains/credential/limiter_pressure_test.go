package credential

import (
	"testing"
)

// TestLimiter_GetPressure_NoPressure 测试无压力情况
func TestLimiter_GetPressure_NoPressure(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	pressure := l.GetPressure(123, 1, "")

	if pressure != 0 {
		t.Errorf("expected no pressure (0), got %f", pressure)
	}
}

// TestLimiter_GetPressure_GlobalPressure 测试 Global 层压力
func TestLimiter_GetPressure_GlobalPressure(t *testing.T) {
	l := NewWithLimits(100, 100, 50, 10)

	// 模拟 80 个并发占用 global 层
	for i := 0; i < 80; i++ {
		if !l.global.TryAcquire() {
			t.Fatalf("failed to acquire global token at %d", i)
		}
	}

	pressure := l.GetPressure(123, 1, "")

	// 预期压力 0.8 (80/100)
	if pressure < 0.75 || pressure > 0.85 {
		t.Errorf("expected pressure ~0.8, got %f (global: %d/%d)",
			pressure, l.global.Used(), l.global.Capacity())
	}
}

// TestLimiter_GetPressure_PoolPressure 测试 Pool 层压力
func TestLimiter_GetPressure_PoolPressure(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	// 获取 pool semaphore
	pool := l.Pool(1)

	// 模拟 60 个并发占用 pool 层
	for i := 0; i < 60; i++ {
		if !pool.TryAcquire() {
			t.Fatalf("failed to acquire pool token at %d", i)
		}
	}

	pressure := l.GetPressure(123, 1, "")

	// 预期压力 0.6 (60/100)
	if pressure < 0.55 || pressure > 0.65 {
		t.Errorf("expected pressure ~0.6, got %f (pool: %d/%d)",
			pressure, pool.Used(), pool.Capacity())
	}
}

// TestLimiter_GetPressure_CredentialPressure 测试 Credential 层压力
func TestLimiter_GetPressure_CredentialPressure(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	// 获取 credential semaphore
	cred := l.Credential(1, 123)

	// 模拟 45 个并发占用 credential 层
	for i := 0; i < 45; i++ {
		if !cred.TryAcquire() {
			t.Fatalf("failed to acquire credential token at %d", i)
		}
	}

	pressure := l.GetPressure(123, 1, "")

	// 预期压力 0.9 (45/50)
	if pressure < 0.85 || pressure > 0.95 {
		t.Errorf("expected pressure ~0.9, got %f (cred: %d/%d)",
			pressure, cred.Used(), cred.Capacity())
	}
}

// TestLimiter_GetPressure_MaxOfMultipleLayers 测试取多层中的最大压力
func TestLimiter_GetPressure_MaxOfMultipleLayers(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	// Global: 100/1000 = 10%
	for i := 0; i < 100; i++ {
		l.global.TryAcquire()
	}

	// Pool: 20/100 = 20%
	pool := l.Pool(1)
	for i := 0; i < 20; i++ {
		pool.TryAcquire()
	}

	// Credential: 40/50 = 80%（最高压力）
	cred := l.Credential(1, 123)
	for i := 0; i < 40; i++ {
		cred.TryAcquire()
	}

	pressure := l.GetPressure(123, 1, "")

	// 应该返回最高压力 0.8
	if pressure < 0.75 || pressure > 0.85 {
		t.Errorf("expected max pressure ~0.8, got %f", pressure)
	}
}

// TestLimiter_GetPressure_IdentityPressure 测试 Identity 层压力
func TestLimiter_GetPressure_IdentityPressure(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	// 获取 identity semaphore（通过 Identity() 方法）
	identityKey := "1/123/test-hash"
	identity := l.Identity(1, 123, "test-hash")

	// 模拟 9 个并发占用 identity 层
	for i := 0; i < 9; i++ {
		if !identity.TryAcquire() {
			t.Fatalf("failed to acquire identity token at %d", i)
		}
	}

	pressure := l.GetPressure(123, 1, identityKey)

	// 预期压力 0.9 (9/10)
	if pressure < 0.85 || pressure > 0.95 {
		t.Errorf("expected pressure ~0.9, got %f (identity: %d/%d)",
			pressure, identity.Used(), identity.Capacity())
	}
}

// TestLimiter_GetPressure_NoIdentityKey 测试不传 identityKey
func TestLimiter_GetPressure_NoIdentityKey(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	// 占用 identity 层
	identity := l.Identity(1, 123, "test-hash")
	for i := 0; i < 9; i++ {
		identity.TryAcquire()
	}

	// 占用 credential 层（较低压力）
	cred := l.Credential(1, 123)
	for i := 0; i < 10; i++ {
		cred.TryAcquire()
	}

	// 不传 identityKey，应该忽略 identity 层
	pressure := l.GetPressure(123, 1, "")

	// 压力来自 credential 层 (10/50 = 20%)
	if pressure < 0.15 || pressure > 0.25 {
		t.Errorf("expected pressure ~0.2 (credential layer), got %f", pressure)
	}
}

// TestLimiter_GetPressure_UnlimitedLayers 测试无限制层
func TestLimiter_GetPressure_UnlimitedLayers(t *testing.T) {
	// 创建无限制的 limiter（所有层 limit = 0）
	l := NewWithLimits(0, 0, 0, 0)

	pressure := l.GetPressure(123, 1, "")

	// 无限制层应该返回 0 压力
	if pressure != 0 {
		t.Errorf("expected pressure = 0 for unlimited layers, got %f", pressure)
	}
}

// TestLimiter_GetPressure_AfterRelease 测试释放后压力降低
func TestLimiter_GetPressure_AfterRelease(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)

	cred := l.Credential(1, 123)

	// 占用 30 个 token
	for i := 0; i < 30; i++ {
		cred.TryAcquire()
	}

	pressureBefore := l.GetPressure(123, 1, "")

	// 释放 20 个 token
	for i := 0; i < 20; i++ {
		cred.Release()
	}

	pressureAfter := l.GetPressure(123, 1, "")

	// 释放后压力应该降低
	if pressureAfter >= pressureBefore {
		t.Errorf("expected pressure to decrease after release, before=%f, after=%f",
			pressureBefore, pressureAfter)
	}

	// 验证压力值合理（10/50 = 20%）
	if pressureAfter < 0.15 || pressureAfter > 0.25 {
		t.Errorf("expected pressure ~0.2 after release, got %f", pressureAfter)
	}
}

// TestLimiter_GetPressure_MaxCap 测试压力上限为 1.0
func TestLimiter_GetPressure_MaxCap(t *testing.T) {
	l := NewWithLimits(100, 100, 50, 10)

	// 占满 global（100 个 token）
	for i := 0; i < 100; i++ {
		l.global.TryAcquire()
	}

	pressure := l.GetPressure(123, 1, "")

	// 压力应该是 1.0（100%）
	if pressure < 0.95 || pressure > 1.0 {
		t.Errorf("expected pressure = 1.0, got %f", pressure)
	}
}
