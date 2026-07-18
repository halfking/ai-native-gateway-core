package circuit

import (
	"sync"
	"time"
)

// WindowMetrics 是滑动窗口的统计指标
type WindowMetrics struct {
	Total     int64
	Successes int64
	Failures  int64
	ErrorRate float64
}

// SlidingWindow 是滑动时间窗口
type SlidingWindow struct {
	windowSize time.Duration
	buckets    []Bucket
	mu         sync.RWMutex
}

// Bucket 是时间桶
type Bucket struct {
	Timestamp time.Time
	Success   int64
	Failure   int64
}

// NewSlidingWindow 创建一个新的滑动窗口
func NewSlidingWindow(windowSize time.Duration) *SlidingWindow {
	return &SlidingWindow{
		windowSize: windowSize,
		buckets:    make([]Bucket, 0),
	}
}

// Record 记录一次调用结果
func (w *SlidingWindow) Record(success bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()

	// 清理过期 bucket
	w.removeExpired(now)

	// 获取或创建当前 bucket (按秒粒度)
	bucket := w.getCurrentBucket(now)
	if success {
		bucket.Success++
	} else {
		bucket.Failure++
	}
}

// Metrics 返回统计指标
func (w *SlidingWindow) Metrics() WindowMetrics {
	w.mu.RLock()
	defer w.mu.RUnlock()

	now := time.Now()
	var total, successes, failures int64

	for _, bucket := range w.buckets {
		if now.Sub(bucket.Timestamp) <= w.windowSize {
			total += bucket.Success + bucket.Failure
			successes += bucket.Success
			failures += bucket.Failure
		}
	}

	var errorRate float64
	if total > 0 {
		errorRate = float64(failures) / float64(total)
	}

	return WindowMetrics{
		Total:     total,
		Successes: successes,
		Failures:  failures,
		ErrorRate: errorRate,
	}
}

// Reset 重置窗口
func (w *SlidingWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buckets = make([]Bucket, 0)
}

// removeExpired 移除过期的 bucket (调用时需持有写锁)
func (w *SlidingWindow) removeExpired(now time.Time) {
	validBuckets := make([]Bucket, 0)
	for _, bucket := range w.buckets {
		if now.Sub(bucket.Timestamp) <= w.windowSize {
			validBuckets = append(validBuckets, bucket)
		}
	}
	w.buckets = validBuckets
}

// getCurrentBucket 获取或创建当前时间的 bucket (调用时需持有写锁)
func (w *SlidingWindow) getCurrentBucket(now time.Time) *Bucket {
	// 按秒粒度对齐
	bucketTime := now.Truncate(time.Second)

	// 查找是否已存在
	for i := range w.buckets {
		if w.buckets[i].Timestamp.Equal(bucketTime) {
			return &w.buckets[i]
		}
	}

	// 不存在，创建新 bucket
	newBucket := Bucket{
		Timestamp: bucketTime,
		Success:   0,
		Failure:   0,
	}
	w.buckets = append(w.buckets, newBucket)

	// 返回最后一个 bucket 的指针
	return &w.buckets[len(w.buckets)-1]
}
