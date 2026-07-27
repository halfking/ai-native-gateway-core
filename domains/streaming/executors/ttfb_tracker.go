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
//
// 并发修复 2026-07-27：此处必须返回结构体副本。原实现在释放 RLock 后把 map 中的
// *TTFBStats 直接交给调用方，而 Record 会在 Lock 下原地修改同一个结构体，
// 调用方（如 executor_chat.go 取 &stats.RecentTTFB）会与 Record 形成数据竞争。
// 拷贝一份返回，保证共享指针不逃逸出锁的保护范围。
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

	out := *stats
	return &out
}
