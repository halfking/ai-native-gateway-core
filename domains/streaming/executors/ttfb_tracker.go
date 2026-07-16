// ttfb_tracker.go — TTFB 历史追踪器实现
package executors

import (
	"sync"
	"time"
)

// TTFBTracker 追踪每个 credential 的历史 TTFB
type TTFBTracker struct {
	mu   sync.RWMutex
	data map[int]*TTFBStats
}

// NewTTFBTracker 创建新的 TTFB 追踪器
func NewTTFBTracker() *TTFBTracker {
	return &TTFBTracker{
		data: make(map[int]*TTFBStats),
	}
}

// Record 记录一次 TTFB
func (t *TTFBTracker) Record(credentialID int, ttfb time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats, ok := t.data[credentialID]
	if !ok {
		stats = &TTFBStats{}
		t.data[credentialID] = stats
	}

	stats.RecentTTFB = ttfb
	stats.UpdatedAt = time.Now()
	stats.SampleCount++

	if stats.AvgTTFB == 0 {
		stats.AvgTTFB = ttfb
	} else {
		stats.AvgTTFB = time.Duration(
			float64(stats.AvgTTFB)*0.8 + float64(ttfb)*0.2,
		)
	}
}

// Get 获取 credential 的 TTFB 统计
func (t *TTFBTracker) Get(credentialID int) *TTFBStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats, ok := t.data[credentialID]
	if !ok {
		return nil
	}

	if time.Since(stats.UpdatedAt) > 5*time.Minute {
		return nil
	}

	return stats
}
