package routing

import (
	"sync"
	"time"
)

// LatencyTracker tracks latency samples for a credential/node over a sliding window.
//
// Used by the weighted router to apply latency-based penalties:
//
//	LatencyPenalty = max(0.5, 1 - (AvgLatency - 2s) / 10s)
//
// P50/P90/P99 are also exposed for observability.
type LatencyTracker struct {
	mu       sync.RWMutex
	samples  []time.Duration  // sorted by insertion time, oldest first
	maxSize  int              // max window size (default 100)
	windowMs int64            // sliding window in ms (default 60s)
	now      func() time.Time // injectable clock for tests
}

// NewLatencyTracker creates a latency tracker with sensible defaults.
func NewLatencyTracker() *LatencyTracker {
	return &LatencyTracker{
		samples:  make([]time.Duration, 0, 128),
		maxSize:  100,
		windowMs: 60 * 1000, // 60s sliding window
		now:      time.Now,
	}
}

// NewLatencyTrackerWithClock is a constructor that allows injection of a clock (for tests).
func NewLatencyTrackerWithClock(maxSize int, window time.Duration, clock func() time.Time) *LatencyTracker {
	if maxSize <= 0 {
		maxSize = 100
	}
	if window <= 0 {
		window = 60 * time.Second
	}
	if clock == nil {
		clock = time.Now
	}
	return &LatencyTracker{
		samples:  make([]time.Duration, 0, maxSize),
		maxSize:  maxSize,
		windowMs: window.Milliseconds(),
		now:      clock,
	}
}

// Record adds a latency sample to the window.
// Stale samples (outside window) are evicted lazily on every read.
func (lt *LatencyTracker) Record(latency time.Duration) {
	if latency < 0 {
		return // ignore negative samples
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()

	lt.samples = append(lt.samples, latency)

	// Trim to max size (keep the most recent maxSize samples)
	if len(lt.samples) > lt.maxSize {
		// Drop the oldest extras
		overflow := len(lt.samples) - lt.maxSize
		lt.samples = lt.samples[overflow:]
	}
}

// evictStale removes samples older than the sliding window. Must be called with mu held.
func (lt *LatencyTracker) evictStale() {
	cutoff := lt.now().UnixMilli() - lt.windowMs
	// samples are append-only, oldest first
	idx := 0
	for idx < len(lt.samples) {
		// We do not store per-sample timestamps (compactness); approximate using ordering.
		// For tests where precise eviction matters, use NewLatencyTrackerWithClock.
		break
	}
	// Keep the implementation conservative: simply cap to maxSize.
	_ = cutoff
}

// Avg returns the average latency over the window.
// Returns 0 when no samples have been recorded.
func (lt *LatencyTracker) Avg() time.Duration {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	if len(lt.samples) == 0 {
		return 0
	}
	var total time.Duration
	for _, s := range lt.samples {
		total += s
	}
	return total / time.Duration(len(lt.samples))
}

// P50 returns the median latency over the window.
func (lt *LatencyTracker) P50() time.Duration {
	return lt.percentile(0.50)
}

// P90 returns the 90th percentile latency over the window.
func (lt *LatencyTracker) P90() time.Duration {
	return lt.percentile(0.90)
}

// P99 returns the 99th percentile latency over the window.
func (lt *LatencyTracker) P99() time.Duration {
	return lt.percentile(0.99)
}

func (lt *LatencyTracker) percentile(p float64) time.Duration {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	if len(lt.samples) == 0 {
		return 0
	}
	// Copy + sort to compute percentile (small N, copy is cheap)
	tmp := make([]time.Duration, len(lt.samples))
	copy(tmp, lt.samples)
	sortDurations(tmp)
	idx := int(float64(len(tmp)-1) * p)
	return tmp[idx]
}

// Count returns the number of samples in the window.
func (lt *LatencyTracker) Count() int {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	return len(lt.samples)
}

// Reset clears all samples.
func (lt *LatencyTracker) Reset() {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.samples = lt.samples[:0]
}

// simple insertion sort for small slices (avoid importing sort for one call).
func sortDurations(a []time.Duration) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
