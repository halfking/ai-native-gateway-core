// storage_metrics_test.go 校验 StorageMetrics 的计数正确性、派生值
// （命中率 / 平均延迟）、写错误语义与并发安全（-race）。
package monitoring

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// approx 比较浮点数（1e-9 容差，足够命中率/均值断言）。
func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func layerOf(t *testing.T, snap map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	raw, ok := snap[key]
	if !ok {
		t.Fatalf("snapshot 缺少 %q", key)
	}
	layer, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("snapshot[%q] 类型 = %T, want map[string]interface{}", key, raw)
	}
	return layer
}

func uintOf(t *testing.T, layer map[string]interface{}, key string) uint64 {
	t.Helper()
	v, ok := layer[key].(uint64)
	if !ok {
		t.Fatalf("layer[%q] 类型 = %T, want uint64", key, layer[key])
	}
	return v
}

func floatOf(t *testing.T, layer map[string]interface{}, key string) float64 {
	t.Helper()
	v, ok := layer[key].(float64)
	if !ok {
		t.Fatalf("layer[%q] 类型 = %T, want float64", key, layer[key])
	}
	return v
}

func TestStorageMetricsRecordAndSnapshot(t *testing.T) {
	m := NewStorageMetrics()
	// L1: 3 命中 1 未命中 → 命中率 0.75
	m.RecordL1Hit()
	m.RecordL1Hit()
	m.RecordL1Hit()
	m.RecordL1Miss()
	// L1.5: 1 命中 3 未命中 → 0.25
	m.RecordL15Hit()
	m.RecordL15Miss()
	m.RecordL15Miss()
	m.RecordL15Miss()
	// L2: 2 命中 2 未命中 → 0.5
	m.RecordL2Hit()
	m.RecordL2Hit()
	m.RecordL2Miss()
	m.RecordL2Miss()
	// L3: 1ms + 3ms → 均值 2ms
	m.RecordL3Query(1 * time.Millisecond)
	m.RecordL3Query(3 * time.Millisecond)
	// 写入：2 成功（123 字节）+ 1 失败
	m.RecordWrite(100, nil)
	m.RecordWrite(23, nil)
	m.RecordWrite(999, errors.New("disk full"))

	snap := m.Snapshot()

	l1 := layerOf(t, snap, "l1")
	if got := uintOf(t, l1, "hits"); got != 3 {
		t.Errorf("l1.hits = %d, want 3", got)
	}
	if got := uintOf(t, l1, "misses"); got != 1 {
		t.Errorf("l1.misses = %d, want 1", got)
	}
	if got := floatOf(t, l1, "hit_rate"); !approx(got, 0.75) {
		t.Errorf("l1.hit_rate = %v, want 0.75", got)
	}

	l15 := layerOf(t, snap, "l1_5")
	if got := floatOf(t, l15, "hit_rate"); !approx(got, 0.25) {
		t.Errorf("l1_5.hit_rate = %v, want 0.25", got)
	}

	l2 := layerOf(t, snap, "l2")
	if got := floatOf(t, l2, "hit_rate"); !approx(got, 0.5) {
		t.Errorf("l2.hit_rate = %v, want 0.5", got)
	}

	l3 := layerOf(t, snap, "l3")
	if got := uintOf(t, l3, "queries"); got != 2 {
		t.Errorf("l3.queries = %d, want 2", got)
	}
	if got := floatOf(t, l3, "avg_latency_ms"); !approx(got, 2.0) {
		t.Errorf("l3.avg_latency_ms = %v, want 2", got)
	}

	writes := layerOf(t, snap, "writes")
	if got := uintOf(t, writes, "total"); got != 2 {
		t.Errorf("writes.total = %d, want 2", got)
	}
	if got := uintOf(t, writes, "total_bytes"); got != 123 {
		t.Errorf("writes.total_bytes = %d, want 123", got)
	}
	if got := uintOf(t, writes, "errors"); got != 1 {
		t.Errorf("writes.errors = %d, want 1", got)
	}

	if got := floatOf(t, snap, "uptime_seconds"); got < 0 {
		t.Errorf("uptime_seconds = %v, want >= 0", got)
	}
}

func TestStorageMetricsHitRateZeroDenominator(t *testing.T) {
	m := NewStorageMetrics() // 全新实例：各层分母为 0
	snap := m.Snapshot()
	for _, key := range []string{"l1", "l1_5", "l2"} {
		layer := layerOf(t, snap, key)
		if got := floatOf(t, layer, "hit_rate"); got != 0 {
			t.Errorf("%s.hit_rate = %v, want 0（分母为 0）", key, got)
		}
		if got := uintOf(t, layer, "hits"); got != 0 {
			t.Errorf("%s.hits = %d, want 0", key, got)
		}
	}
	if got := floatOf(t, layerOf(t, snap, "l3"), "avg_latency_ms"); got != 0 {
		t.Errorf("l3.avg_latency_ms = %v, want 0（queries 为 0）", got)
	}
}

func TestStorageMetricsRecordWriteErrorOnlyCountsErrors(t *testing.T) {
	m := NewStorageMetrics()
	m.RecordWrite(0, errors.New("boom"))
	m.RecordWrite(0, errors.New("boom again"))
	snap := m.Snapshot()
	writes := layerOf(t, snap, "writes")
	if got := uintOf(t, writes, "errors"); got != 2 {
		t.Errorf("writes.errors = %d, want 2", got)
	}
	if got := uintOf(t, writes, "total"); got != 0 {
		t.Errorf("writes.total = %d, want 0（失败不计入成功总数）", got)
	}
	if got := uintOf(t, writes, "total_bytes"); got != 0 {
		t.Errorf("writes.total_bytes = %d, want 0（失败不计入字节数）", got)
	}
}

func TestStorageMetricsDefaultIsSingleton(t *testing.T) {
	if Default() != Default() {
		t.Fatal("Default() 两次调用返回不同实例，应为 sync.Once 单例")
	}
}

// TestStorageMetricsConcurrentMixed 100 goroutine 混合打点：每个 goroutine
// 50 轮固定操作，结束后所有计数应精确等于 100×50。需在 -race 下运行。
func TestStorageMetricsConcurrentMixed(t *testing.T) {
	const (
		goroutines = 100
		rounds     = 50
	)
	m := NewStorageMetrics()
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				m.RecordL1Hit()
				m.RecordL1Miss()
				m.RecordL15Hit()
				m.RecordL15Miss()
				m.RecordL2Hit()
				m.RecordL2Miss()
				m.RecordL3Query(2 * time.Microsecond) // 每次固定 2μs
				m.RecordWrite(10, nil)
			}
		}()
	}
	wg.Wait()

	want := uint64(goroutines * rounds)
	snap := m.Snapshot()
	for _, key := range []string{"l1", "l1_5", "l2"} {
		layer := layerOf(t, snap, key)
		if got := uintOf(t, layer, "hits"); got != want {
			t.Errorf("%s.hits = %d, want %d", key, got, want)
		}
		if got := uintOf(t, layer, "misses"); got != want {
			t.Errorf("%s.misses = %d, want %d", key, got, want)
		}
	}
	l3 := layerOf(t, snap, "l3")
	if got := uintOf(t, l3, "queries"); got != want {
		t.Errorf("l3.queries = %d, want %d", got, want)
	}
	// 均值 = 2μs = 0.002ms（并发下累计延迟仍精确可加）
	if got := floatOf(t, l3, "avg_latency_ms"); !approx(got, 0.002) {
		t.Errorf("l3.avg_latency_ms = %v, want 0.002", got)
	}
	writes := layerOf(t, snap, "writes")
	if got := uintOf(t, writes, "total"); got != want {
		t.Errorf("writes.total = %d, want %d", got, want)
	}
	if got := uintOf(t, writes, "total_bytes"); got != want*10 {
		t.Errorf("writes.total_bytes = %d, want %d", got, want*10)
	}
}
