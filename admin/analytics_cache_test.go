package admin

import (
	"testing"
	"time"
)

func TestAnalyticsCache_SetGet(t *testing.T) {
	cache, err := NewAnalyticsCache(10, 1*time.Minute)
	if err != nil {
		t.Fatalf("NewAnalyticsCache failed: %v", err)
	}

	// 设置
	cache.Set("key1", "value1")

	// 获取（命中）
	val, found := cache.Get("key1")
	if !found {
		t.Fatal("expected cache hit")
	}
	if val != "value1" {
		t.Errorf("expected 'value1', got %v", val)
	}

	// 获取（未命中）
	_, found = cache.Get("key2")
	if found {
		t.Fatal("expected cache miss")
	}
}

func TestAnalyticsCache_TTL(t *testing.T) {
	cache, err := NewAnalyticsCache(10, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewAnalyticsCache failed: %v", err)
	}

	cache.Set("key1", "value1")

	// 立即获取（命中）
	_, found := cache.Get("key1")
	if !found {
		t.Fatal("expected cache hit")
	}

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 再次获取（已过期）
	_, found = cache.Get("key1")
	if found {
		t.Fatal("expected cache miss after TTL")
	}
}

func TestAnalyticsCache_Stats(t *testing.T) {
	cache, err := NewAnalyticsCache(10, 1*time.Minute)
	if err != nil {
		t.Fatalf("NewAnalyticsCache failed: %v", err)
	}

	cache.Set("key1", "value1")
	cache.Get("key1") // hit
	cache.Get("key2") // miss
	cache.Get("key1") // hit

	stats := cache.Stats()
	if stats.Hits != 2 {
		t.Errorf("expected 2 hits, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}
	expectedHitRate := 2.0 / 3.0
	if stats.HitRate < expectedHitRate-0.01 || stats.HitRate > expectedHitRate+0.01 {
		t.Errorf("expected hit rate ~%.2f, got %.2f", expectedHitRate, stats.HitRate)
	}
}

func TestBuildCacheKey(t *testing.T) {
	filters := AnalyticsCacheFilters{
		DateFrom: "2026-06-01",
		DateTo:   "2026-07-06",
		Model:    []string{"gpt-4o"},
	}

	key1 := BuildCacheKey("activity", "tenant_abc", filters)
	key2 := BuildCacheKey("activity", "tenant_abc", filters)

	// 相同输入应生成相同键
	if key1 != key2 {
		t.Errorf("expected same key, got %s vs %s", key1, key2)
	}

	// 不同租户应生成不同键
	key3 := BuildCacheKey("activity", "tenant_xyz", filters)
	if key1 == key3 {
		t.Error("expected different keys for different tenants")
	}

	// 不同端点应生成不同键
	key4 := BuildCacheKey("cost-trend", "tenant_abc", filters)
	if key1 == key4 {
		t.Error("expected different keys for different endpoints")
	}
}

func TestAnalyticsCache_Clear(t *testing.T) {
	cache, err := NewAnalyticsCache(10, 1*time.Minute)
	if err != nil {
		t.Fatalf("NewAnalyticsCache failed: %v", err)
	}

	cache.Set("key1", "value1")
	cache.Set("key2", "value2")

	stats := cache.Stats()
	if stats.Size != 2 {
		t.Errorf("expected size 2, got %d", stats.Size)
	}

	cache.Clear()

	stats = cache.Stats()
	if stats.Size != 0 {
		t.Errorf("expected size 0 after clear, got %d", stats.Size)
	}

	// 清空后应无法获取
	_, found := cache.Get("key1")
	if found {
		t.Fatal("expected cache miss after clear")
	}
}

func TestAnalyticsCache_ComplexValue(t *testing.T) {
	cache, err := NewAnalyticsCache(10, 1*time.Minute)
	if err != nil {
		t.Fatalf("NewAnalyticsCache failed: %v", err)
	}

	// 缓存复杂结构
	type Result struct {
		Sessions int
		Cost     float64
		Data     []string
	}
	expected := Result{
		Sessions: 100,
		Cost:     12.34,
		Data:     []string{"a", "b", "c"},
	}

	cache.Set("key1", expected)

	val, found := cache.Get("key1")
	if !found {
		t.Fatal("expected cache hit")
	}

	result, ok := val.(Result)
	if !ok {
		t.Fatalf("expected Result type, got %T", val)
	}

	if result.Sessions != expected.Sessions {
		t.Errorf("expected Sessions %d, got %d", expected.Sessions, result.Sessions)
	}
	if result.Cost != expected.Cost {
		t.Errorf("expected Cost %.2f, got %.2f", expected.Cost, result.Cost)
	}
	if len(result.Data) != len(expected.Data) {
		t.Errorf("expected Data length %d, got %d", len(expected.Data), len(result.Data))
	}
}
