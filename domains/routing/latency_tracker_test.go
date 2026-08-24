package routing

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLatencyTracker_RecordAndAvg tests basic recording and average calculation.
func TestLatencyTracker_RecordAndAvg(t *testing.T) {
	lt := NewLatencyTracker()

	lt.Record(100 * time.Millisecond)
	lt.Record(200 * time.Millisecond)
	lt.Record(300 * time.Millisecond)

	avg := lt.Avg()
	assert.Equal(t, 200*time.Millisecond, avg, "Average should be 200ms")
	assert.Equal(t, 3, lt.Count())

	t.Logf("✓ 延时记录和平均值计算正确: %v", avg)
}

// TestLatencyTracker_EmptyAvg tests average with no samples.
func TestLatencyTracker_EmptyAvg(t *testing.T) {
	lt := NewLatencyTracker()

	assert.Equal(t, time.Duration(0), lt.Avg())
	assert.Equal(t, 0, lt.Count())

	t.Logf("✓ 空tracker返回0延迟")
}

// TestLatencyTracker_P50 tests median calculation.
func TestLatencyTracker_P50(t *testing.T) {
	lt := NewLatencyTracker()

	// 5 samples: 100, 200, 300, 400, 500 → P50 = 300
	lt.Record(100 * time.Millisecond)
	lt.Record(200 * time.Millisecond)
	lt.Record(300 * time.Millisecond)
	lt.Record(400 * time.Millisecond)
	lt.Record(500 * time.Millisecond)

	p50 := lt.P50()
	assert.Equal(t, 300*time.Millisecond, p50, "P50 should be 300ms")

	t.Logf("✓ P50中位数计算正确: %v", p50)
}

// TestLatencyTracker_P90 tests 90th percentile.
func TestLatencyTracker_P90(t *testing.T) {
	lt := NewLatencyTracker()

	// 10 samples: 100ms to 1000ms (step 100ms)
	for i := 1; i <= 10; i++ {
		lt.Record(time.Duration(i*100) * time.Millisecond)
	}

	p90 := lt.P90()
	// P90 index = int(9 * 0.9) = 8 → sorted[8] = 900ms
	assert.Equal(t, 900*time.Millisecond, p90, "P90 should be 900ms")

	t.Logf("✓ P90百分位计算正确: %v", p90)
}

// TestLatencyTracker_P99 tests 99th percentile.
func TestLatencyTracker_P99(t *testing.T) {
	lt := NewLatencyTracker()

	// 100 samples: 100ms to 10000ms (step 100ms)
	for i := 1; i <= 100; i++ {
		lt.Record(time.Duration(i*100) * time.Millisecond)
	}

	p99 := lt.P99()
	// P99 index = int(99 * 0.99) = 98 → sorted[98] = 9900ms (1-based * 100)
	assert.Equal(t, 9900*time.Millisecond, p99, "P99 should be 9900ms")

	t.Logf("✓ P99百分位计算正确: %v", p99)
}

// TestLatencyTracker_MaxSizeLimit tests that samples are bounded.
func TestLatencyTracker_MaxSizeLimit(t *testing.T) {
	lt := NewLatencyTrackerWithClock(10, 60*time.Second, time.Now)

	// Record 150 samples (maxSize is 10)
	for i := 1; i <= 150; i++ {
		lt.Record(time.Duration(i) * time.Millisecond)
	}

	assert.Equal(t, 10, lt.Count(), "Should keep only 10 most recent samples")
	// The 10 most recent are 141ms through 150ms
	avg := lt.Avg()
	expected := (141 + 142 + 143 + 144 + 145 + 146 + 147 + 148 + 149 + 150) * time.Millisecond / 10
	assert.Equal(t, expected, avg, "Average should be from the last 10 samples")

	t.Logf("✓ 最大样本数限制正确: %d samples, avg=%v", lt.Count(), avg)
}

// TestLatencyTracker_NegativeLatency tests that negative samples are ignored.
func TestLatencyTracker_NegativeLatency(t *testing.T) {
	lt := NewLatencyTracker()

	lt.Record(100 * time.Millisecond)
	lt.Record(-50 * time.Millisecond) // Should be ignored
	lt.Record(200 * time.Millisecond)

	assert.Equal(t, 2, lt.Count(), "Negative sample should be ignored")
	assert.Equal(t, 150*time.Millisecond, lt.Avg())

	t.Logf("✓ 负延迟样本被正确忽略")
}

// TestLatencyTracker_Reset tests reset functionality.
func TestLatencyTracker_Reset(t *testing.T) {
	lt := NewLatencyTracker()

	lt.Record(100 * time.Millisecond)
	lt.Record(200 * time.Millisecond)
	assert.Equal(t, 2, lt.Count())

	lt.Reset()
	assert.Equal(t, 0, lt.Count())
	assert.Equal(t, time.Duration(0), lt.Avg())

	t.Logf("✓ Reset功能正确")
}

// TestLatencyTracker_ConcurrentAccess tests thread safety.
func TestLatencyTracker_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过并发测试")
	}

	lt := NewLatencyTracker()

	const goroutines = 50
	const recordsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < recordsPerGoroutine; i++ {
				lt.Record(time.Duration(i) * time.Millisecond)
			}
		}()
	}

	wg.Wait()

	expectedCount := goroutines * recordsPerGoroutine
	if expectedCount > 100 {
		expectedCount = 100 // capped at maxSize
	}

	count := lt.Count()
	assert.LessOrEqual(t, count, 100, "Should not exceed maxSize")
	assert.Greater(t, count, 0, "Should have samples")

	t.Logf("✓ 并发访问安全，记录了 %d 个样本", count)
}

// TestLatencyTracker_EmptyPercentile tests percentile on empty tracker.
func TestLatencyTracker_EmptyPercentile(t *testing.T) {
	lt := NewLatencyTracker()

	assert.Equal(t, time.Duration(0), lt.P50())
	assert.Equal(t, time.Duration(0), lt.P90())
	assert.Equal(t, time.Duration(0), lt.P99())

	t.Logf("✓ 空tracker百分位返回0")
}

// TestLatencyTracker_SingleSample tests with single sample.
func TestLatencyTracker_SingleSample(t *testing.T) {
	lt := NewLatencyTracker()

	lt.Record(500 * time.Millisecond)

	assert.Equal(t, 500*time.Millisecond, lt.Avg())
	assert.Equal(t, 500*time.Millisecond, lt.P50())
	assert.Equal(t, 500*time.Millisecond, lt.P90())
	assert.Equal(t, 500*time.Millisecond, lt.P99())

	t.Logf("✓ 单样本情况下所有统计量都正确")
}

// manualClock is a mutable clock advanced only by explicit test calls, so
// window eviction is deterministic without real sleeping.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// TestLatencyTracker_WindowEvictsStaleSamples pins the sliding-window
// contract: every read (Avg/P50/P90/P99/Count) must drop samples older than
// the window even when no new Record arrives. evictStale previously did
// nothing, so stale samples kept inflating the weighted-router latency
// penalty forever.
func TestLatencyTracker_WindowEvictsStaleSamples(t *testing.T) {
	clock := &manualClock{now: time.Unix(1700000000, 0)}
	lt := NewLatencyTrackerWithClock(100, 60*time.Second, clock.Now)

	lt.Record(100 * time.Millisecond)
	clock.advance(30 * time.Second)
	lt.Record(900 * time.Millisecond)

	// Both samples are inside the 60s window.
	assert.Equal(t, 2, lt.Count(), "both samples must stay in-window")
	assert.Equal(t, 500*time.Millisecond, lt.Avg(), "in-window average = 500ms")

	// Advance so the first sample is 61s old (outside), the second 31s (inside).
	clock.advance(31 * time.Second)
	assert.Equal(t, 1, lt.Count(), "stale sample must be evicted on read")
	assert.Equal(t, 900*time.Millisecond, lt.Avg(), "only the fresh sample remains")
	assert.Equal(t, 900*time.Millisecond, lt.P50())
	assert.Equal(t, 900*time.Millisecond, lt.P90())
	assert.Equal(t, 900*time.Millisecond, lt.P99())

	// Advance past everything: all reads must report empty.
	clock.advance(61 * time.Second)
	assert.Equal(t, 0, lt.Count(), "fully stale window must be empty")
	assert.Equal(t, time.Duration(0), lt.Avg())
	assert.Equal(t, time.Duration(0), lt.P50())
	assert.Equal(t, time.Duration(0), lt.P99())
}

// TestLatencyTracker_MaxSizeAndWindowCoexist pins that the size trim and the
// time eviction share one consistent sample set.
func TestLatencyTracker_MaxSizeAndWindowCoexist(t *testing.T) {
	clock := &manualClock{now: time.Unix(1700000000, 0)}
	lt := NewLatencyTrackerWithClock(3, 60*time.Second, clock.Now)

	// Fill to the size cap; the first (slow) sample gets trimmed by size.
	lt.Record(5 * time.Second)
	clock.advance(time.Second)
	lt.Record(100 * time.Millisecond)
	clock.advance(time.Second)
	lt.Record(200 * time.Millisecond)
	clock.advance(time.Second)
	lt.Record(300 * time.Millisecond)

	assert.Equal(t, 3, lt.Count())
	assert.Equal(t, 200*time.Millisecond, lt.Avg(), "size trim must drop the oldest (5s) sample")

	// Advance beyond the window: time eviction clears the rest.
	clock.advance(61 * time.Second)
	assert.Equal(t, 0, lt.Count())
	assert.Equal(t, time.Duration(0), lt.Avg())
}

// BenchmarkLatencyTracker_Record benchmarks recording performance.
func BenchmarkLatencyTracker_Record(b *testing.B) {
	lt := NewLatencyTracker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lt.Record(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkLatencyTracker_Avg benchmarks average calculation.
func BenchmarkLatencyTracker_Avg(b *testing.B) {
	lt := NewLatencyTracker()
	for i := 0; i < 100; i++ {
		lt.Record(time.Duration(i*10) * time.Millisecond)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lt.Avg()
	}
}
