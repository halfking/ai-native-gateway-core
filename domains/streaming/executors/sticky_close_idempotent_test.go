package executors

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestStickyCacheCloseConcurrentIdempotent 钉 F-44（2026-11-12 53 增量）：
// 旧实现 select-default 判「已关」存在竞态——两个并发 Close 可同时进 default 臂
// double-close stopSweep panic。真 sync.Once 后并发 Close×N 必须无 panic 且
// 后台 sweep goroutine 确实退出。跑 -race 才有牙齿（无 race 检测时仍可抓 panic）。
func TestStickyCacheCloseConcurrentIdempotent(t *testing.T) {
	s := NewStickyCache()
	// 让 sweep goroutine 存活到 Close 时刻（带一个条目让 sweep 有活干）。
	s.RecordFailure("sticky-close-test-key", 2)

	const n = 16
	var wg sync.WaitGroup
	var panicked sync.Once
	panicCaught := make(chan struct{}, 1)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked.Do(func() { panicCaught <- struct{}{} })
				}
			}()
			s.Close()
		}()
	}
	wg.Wait()

	select {
	case <-panicCaught:
		t.Fatalf("并发 Close 触发 panic（double-close stopSweep）——F-44 修复失效")
	default:
	}

	// sweep goroutine 必须退出：重复 Close 后 WaitGroup 立即返回，不再有泄漏。
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("重复 Close 阻塞——sweepDone.Wait 未返回")
	}
}

// TestStickyCacheCloseNilReceiver Close 对 nil receiver 保持安全（既有语义保持）。
func TestStickyCacheCloseNilReceiver(t *testing.T) {
	var s *StickyCache
	assert.NotPanics(t, func() { s.Close() })
}
