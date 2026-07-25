package credential

import (
	"testing"
	"time"
)

// TestLimiterPressureCache 测试 GetPressure 缓存机制
func TestLimiterPressureCache(t *testing.T) {
	l := NewWithLimits(100, 50, 20, 10)
	defer l.Stop()

	credentialID := 123
	poolID := 1

	// 第一次调用 - 缓存未命中
	pressure1 := l.GetPressure(credentialID, poolID, "")
	if pressure1 < 0 || pressure1 > 1 {
		t.Errorf("pressure out of range: %f", pressure1)
	}

	// 第二次调用（立即） - 应该从缓存读取
	pressure2 := l.GetPressure(credentialID, poolID, "")
	if pressure2 != pressure1 {
		t.Errorf("cache should return same value: got %f, want %f", pressure2, pressure1)
	}

	// 等待缓存过期（6秒 > 5秒 TTL）
	time.Sleep(6 * time.Second)

	// 第三次调用 - 缓存已过期，应该重新计算
	pressure3 := l.GetPressure(credentialID, poolID, "")
	// 值可能相同（如果压力没变），但应该重新计算了
	_ = pressure3
}

// TestLimiterPressureCacheTTL 测试缓存 TTL
func TestLimiterPressureCacheTTL(t *testing.T) {
	l := NewWithLimits(100, 50, 20, 10)
	defer l.Stop()

	// 自定义短 TTL 用于快速测试
	l.pressureCacheTTL = 100 * time.Millisecond

	credentialID := 456
	poolID := 2

	// 获取初始压力
	pressure1 := l.GetPressure(credentialID, poolID, "")

	// 等待 TTL 过期
	time.Sleep(150 * time.Millisecond)

	// 此时缓存应该已过期
	pressure2 := l.GetPressure(credentialID, poolID, "")
	// 两次值应该相同（因为没有改变负载）
	if pressure1 != pressure2 {
		t.Logf("pressure changed after TTL: %f -> %f (expected, no load change)", pressure1, pressure2)
	}
}

// TestLimiterPressureCacheMultipleCredentials 测试多个凭据的缓存隔离
func TestLimiterPressureCacheMultipleCredentials(t *testing.T) {
	l := NewWithLimits(100, 50, 20, 10)
	defer l.Stop()

	cred1, cred2 := 100, 200
	pool1, pool2 := 1, 2

	// 获取两个不同凭据的压力
	pressure1 := l.GetPressure(cred1, pool1, "")
	pressure2 := l.GetPressure(cred2, pool2, "")

	// 验证缓存隔离
	cachedPressure1 := l.GetPressure(cred1, pool1, "")
	cachedPressure2 := l.GetPressure(cred2, pool2, "")

	if pressure1 != cachedPressure1 {
		t.Errorf("cred1 cache mismatch: got %f, want %f", cachedPressure1, pressure1)
	}
	if pressure2 != cachedPressure2 {
		t.Errorf("cred2 cache mismatch: got %f, want %f", cachedPressure2, pressure2)
	}
}

// TestLimiterPressureCacheConcurrency 测试并发访问缓存
func TestLimiterPressureCacheConcurrency(t *testing.T) {
	l := NewWithLimits(1000, 500, 200, 100)
	defer l.Stop()

	credentialID := 999
	poolID := 5

	// 并发读取压力（测试缓存的并发安全性）
	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			pressure := l.GetPressure(credentialID, poolID, "")
			if pressure < 0 || pressure > 1 {
				t.Errorf("invalid pressure: %f", pressure)
			}
			done <- true
		}()
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 100; i++ {
		<-done
	}
}
