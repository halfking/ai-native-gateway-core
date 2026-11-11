package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

// Config holds rate limit bucket configuration
type Config struct {
	Window        time.Duration // 时间窗口，默认1分钟
	MaxQueueSize  int           // 最大队列长度，默认等于limit
	MaxWaitTime   time.Duration // 最大等待时间，默认2分钟
	EnableMetrics bool          // 是否启用指标收集
}

// DefaultConfig returns default rate limit configuration
func DefaultConfig() Config {
	return Config{
		Window:        time.Minute,
		MaxQueueSize:  0, // 0表示等于limit
		MaxWaitTime:   2 * time.Minute,
		EnableMetrics: true,
	}
}

// minuteBucket represents a rate limit bucket for a time window
type minuteBucket struct {
	count int       // 当前窗口已消耗数量
	start time.Time // 窗口开始时间
}

// queuedRequest represents a queued request waiting for admission
type queuedRequest struct {
	ready  chan struct{} // 通知channel，当可以通过时关闭
	queued bool          // 是否还在队列中
}

// RateLimitProvider implements BucketProvider for rate limiting
type RateLimitProvider struct {
	mu      sync.Mutex
	config  Config
	clock   func() time.Time
	buckets map[string]*minuteBucket    // key -> bucket
	queues  map[string][]*queuedRequest // key -> queue
	metrics *providerMetrics
}

// providerMetrics tracks rate limit metrics
type providerMetrics struct {
	mu                sync.RWMutex
	totalRequests     int64
	admittedRequests  int64
	rejectedRequests  int64
	queuedRequests    int64
	totalWaitTime     time.Duration
	maxQueueLength    int
	currentQueueTotal int
}

// NewRateLimitProvider creates a new rate limit bucket provider
func NewRateLimitProvider(config Config) *RateLimitProvider {
	return &RateLimitProvider{
		config:  config,
		clock:   time.Now,
		buckets: make(map[string]*minuteBucket),
		queues:  make(map[string][]*queuedRequest),
		metrics: &providerMetrics{},
	}
}

// Name returns the provider name
func (p *RateLimitProvider) Name() string {
	return "rate_limit_minute"
}

// Type returns the bucket type
func (p *RateLimitProvider) Type() core.BucketType {
	return core.BucketTypeRateLimit
}

// GetState retrieves the current state of a rate limit bucket
func (p *RateLimitProvider) GetState(ctx context.Context, key core.BucketKey) (*core.BucketState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	bucketKey := key.String()
	bucket := p.buckets[bucketKey]
	queue := p.queues[bucketKey]

	now := p.clock()
	state := &core.BucketState{
		Capacity:  0, // Will be set based on limit passed in Query
		Used:      0,
		Reserved:  0,
		Available: 0,
		UpdatedAt: now,
		Metadata:  make(map[string]interface{}),
	}

	if bucket != nil && p.sameWindow(bucket.start, now) {
		state.Used = int64(bucket.count)
		state.Metadata["window_start"] = bucket.start
		state.Metadata["window_end"] = bucket.start.Add(p.config.Window)
	} else {
		state.Used = 0
		state.Metadata["window_start"] = p.bucketStart(now)
		state.Metadata["window_end"] = p.bucketStart(now).Add(p.config.Window)
	}

	state.Metadata["queue_length"] = len(queue)
	state.Metadata["time_until_next_window"] = p.timeUntilNextBucketLocked()

	return state, nil
}

// UpdateState applies a delta to update bucket state
func (p *RateLimitProvider) UpdateState(ctx context.Context, key core.BucketKey, delta core.BucketDelta) (*core.BucketState, error) {
	// Rate limit bucket不支持直接UpdateState
	// 使用Query方法进行准入控制
	return nil, fmt.Errorf("rate limit bucket does not support direct state update, use Query for admission control")
}

// Query performs admission control for rate limiting
func (p *RateLimitProvider) Query(ctx context.Context, query core.BucketQuery) (*core.BucketResult, error) {
	if query.Type != core.BucketTypeRateLimit {
		return nil, fmt.Errorf("invalid bucket type: expected %s, got %s", core.BucketTypeRateLimit, query.Type)
	}

	// 从Params中提取limit
	limit := 0
	if limitVal, ok := query.Params["limit"]; ok {
		switch v := limitVal.(type) {
		case int:
			limit = v
		case int64:
			limit = int(v)
		case float64:
			limit = int(v)
		default:
			return nil, fmt.Errorf("invalid limit type: %T", limitVal)
		}
	}

	if limit <= 0 {
		// 无限制，直接通过
		return &core.BucketResult{
			RowsAffected: 1,
			Data:         []core.BucketState{},
			Metadata: map[string]interface{}{
				"admitted":  true,
				"limit":     0,
				"remaining": -1,
			},
		}, nil
	}

	// 构造BucketKey
	bucketKey := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   query.TenantID,
		ResourceID: query.ResourceID,
		Window:     query.Window,
	}

	// 提取maxWait参数
	var maxWait time.Duration
	if mw, ok := query.Params["max_wait"]; ok {
		if mwDuration, ok := mw.(time.Duration); ok {
			maxWait = mwDuration
		}
	}

	// 提取notify回调
	var notify func(core.AdmissionResult)
	if nf, ok := query.Params["notify"]; ok {
		if notifyFunc, ok := nf.(func(core.AdmissionResult)); ok {
			notify = notifyFunc
		}
	}

	result, err := p.admit(ctx, bucketKey, limit, maxWait, notify)
	return result, err
}

// admit performs the actual admission control logic
func (p *RateLimitProvider) admit(ctx context.Context, key core.BucketKey, limit int, maxWait time.Duration, notify func(core.AdmissionResult)) (*core.BucketResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	bucketKey := key.String()

	p.mu.Lock()
	now := p.clock()
	bucket := p.buckets[bucketKey]

	// 检查是否需要创建新窗口
	if bucket == nil || !p.sameWindow(bucket.start, now) {
		bucket = &minuteBucket{start: p.bucketStart(now)}
		p.buckets[bucketKey] = bucket
		p.releaseWaitersLocked(bucketKey, limit)
	}

	queue := p.queues[bucketKey]

	// 快速路径：无队列且未满
	if len(queue) == 0 && bucket.count < limit {
		bucket.count++
		remaining := limit - bucket.count
		p.mu.Unlock()

		// 更新指标
		if p.config.EnableMetrics {
			p.metrics.mu.Lock()
			p.metrics.totalRequests++
			p.metrics.admittedRequests++
			p.metrics.mu.Unlock()
		}

		state := core.BucketState{
			Capacity:  int64(limit),
			Used:      int64(bucket.count),
			Available: int64(remaining),
			UpdatedAt: now,
		}

		return &core.BucketResult{
			RowsAffected: 1,
			Data:         []core.BucketState{state},
			Metadata: map[string]interface{}{
				"admitted":  true,
				"limit":     limit,
				"remaining": remaining,
			},
		}, nil
	}

	// 队列已满
	maxQueueSize := p.config.MaxQueueSize
	if maxQueueSize == 0 {
		maxQueueSize = limit
	}
	if len(queue) >= maxQueueSize {
		p.mu.Unlock()

		// 更新指标
		if p.config.EnableMetrics {
			p.metrics.mu.Lock()
			p.metrics.totalRequests++
			p.metrics.rejectedRequests++
			p.metrics.mu.Unlock()
		}

		return &core.BucketResult{
			RowsAffected: 0,
			Data:         []core.BucketState{},
			Metadata: map[string]interface{}{
				"admitted": false,
				"limit":    limit,
				"position": len(queue) + 1,
				"reason":   "queue_full",
			},
		}, core.ErrBucketExceeded
	}

	// 估算等待时间
	position := len(queue) + 1
	estimatedWait := p.estimateWaitLocked(position, limit)

	// 检查是否超过maxWait预算
	if maxWait > 0 && estimatedWait > maxWait {
		p.mu.Unlock()

		// 更新指标
		if p.config.EnableMetrics {
			p.metrics.mu.Lock()
			p.metrics.totalRequests++
			p.metrics.rejectedRequests++
			p.metrics.mu.Unlock()
		}

		return &core.BucketResult{
			RowsAffected: 0,
			Data:         []core.BucketState{},
			Metadata: map[string]interface{}{
				"admitted":           false,
				"limit":              limit,
				"position":           position,
				"queue_remaining":    maxQueueSize - len(queue) - 1,
				"estimated_wait_sec": int(estimatedWait.Round(time.Second) / time.Second),
				"reason":             "budget_exceeded",
			},
		}, core.ErrBucketWaitTimeout
	}

	// 加入队列
	waiter := &queuedRequest{ready: make(chan struct{}), queued: true}
	p.queues[bucketKey] = append(queue, waiter)
	queueRemaining := maxQueueSize - len(queue) - 1
	estimatedWaitSec := int(estimatedWait.Round(time.Second) / time.Second)
	p.mu.Unlock()

	// 更新指标
	if p.config.EnableMetrics {
		p.metrics.mu.Lock()
		p.metrics.totalRequests++
		p.metrics.queuedRequests++
		p.metrics.currentQueueTotal++
		if p.metrics.currentQueueTotal > p.metrics.maxQueueLength {
			p.metrics.maxQueueLength = p.metrics.currentQueueTotal
		}
		p.metrics.mu.Unlock()
	}

	// 通知调用者进入队列
	if notify != nil {
		notify(core.AdmissionResult{
			Admitted:         false,
			Waiting:          true,
			Position:         position,
			Limit:            limit,
			QueueRemaining:   queueRemaining,
			EstimatedWaitSec: estimatedWaitSec,
			Timestamp:        now,
		})
	}

	// 等待准入
	waitStart := time.Now()
	for {
		wait := p.timeUntilNextBucket()
		if wait > p.config.MaxWaitTime {
			wait = p.config.MaxWaitTime
		}
		timer := time.NewTimer(wait)

		select {
		case <-waiter.ready:
			timer.Stop()
			waitDuration := time.Since(waitStart)

			// 更新指标
			if p.config.EnableMetrics {
				p.metrics.mu.Lock()
				p.metrics.admittedRequests++
				p.metrics.totalWaitTime += waitDuration
				p.metrics.currentQueueTotal--
				p.metrics.mu.Unlock()
			}

			return &core.BucketResult{
				RowsAffected: 1,
				Data:         []core.BucketState{},
				Metadata: map[string]interface{}{
					"admitted":      true,
					"limit":         limit,
					"wait_duration": waitDuration,
				},
			}, nil

		case <-ctx.Done():
			timer.Stop()
			p.cancel(bucketKey, waiter)

			// 更新指标
			if p.config.EnableMetrics {
				p.metrics.mu.Lock()
				p.metrics.rejectedRequests++
				p.metrics.currentQueueTotal--
				p.metrics.mu.Unlock()
			}

			return &core.BucketResult{
				RowsAffected: 0,
				Data:         []core.BucketState{},
				Metadata: map[string]interface{}{
					"admitted": false,
					"limit":    limit,
					"position": position,
					"reason":   "context_canceled",
				},
			}, ctx.Err()

		case <-timer.C:
			if wait == p.config.MaxWaitTime {
				p.cancel(bucketKey, waiter)

				// 更新指标
				if p.config.EnableMetrics {
					p.metrics.mu.Lock()
					p.metrics.rejectedRequests++
					p.metrics.currentQueueTotal--
					p.metrics.mu.Unlock()
				}

				return &core.BucketResult{
					RowsAffected: 0,
					Data:         []core.BucketState{},
					Metadata: map[string]interface{}{
						"admitted": false,
						"limit":    limit,
						"position": position,
						"reason":   "wait_timeout",
					},
				}, core.ErrBucketWaitTimeout
			}

			// 检查是否进入新窗口
			p.mu.Lock()
			if waiter.queued {
				now := p.clock()
				bucket := p.buckets[bucketKey]
				if bucket == nil || !p.sameWindow(bucket.start, now) {
					bucket = &minuteBucket{start: p.bucketStart(now)}
					p.buckets[bucketKey] = bucket
					p.releaseWaitersLocked(bucketKey, limit)
				}
			}
			p.mu.Unlock()
		}
	}
}

// Reset resets a bucket's state
func (p *RateLimitProvider) Reset(ctx context.Context, key core.BucketKey) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	bucketKey := key.String()
	delete(p.buckets, bucketKey)

	// 释放所有等待者
	queue := p.queues[bucketKey]
	for _, waiter := range queue {
		if waiter.queued {
			waiter.queued = false
			close(waiter.ready)
		}
	}
	delete(p.queues, bucketKey)

	return nil
}

// GetMetrics returns current provider metrics
func (p *RateLimitProvider) GetMetrics(ctx context.Context) (*core.BucketMetrics, error) {
	if !p.config.EnableMetrics {
		return nil, fmt.Errorf("metrics not enabled")
	}

	p.metrics.mu.RLock()
	defer p.metrics.mu.RUnlock()

	avgWaitTime := time.Duration(0)
	if p.metrics.queuedRequests > 0 {
		avgWaitTime = p.metrics.totalWaitTime / time.Duration(p.metrics.queuedRequests)
	}

	cacheHitRate := float64(0)
	if p.metrics.totalRequests > 0 {
		cacheHitRate = float64(p.metrics.admittedRequests) / float64(p.metrics.totalRequests)
	}

	now := time.Now()
	return &core.BucketMetrics{
		TotalRequests:   p.metrics.totalRequests,
		SuccessRequests: p.metrics.admittedRequests,
		FailedRequests:  p.metrics.rejectedRequests,
		AvgLatency:      avgWaitTime,
		CacheHits:       p.metrics.queuedRequests,
		UsageRate:       cacheHitRate,
		WindowStart:     now.Add(-time.Hour),
		WindowEnd:       now,
	}, nil
}

// Helper methods

func (p *RateLimitProvider) releaseWaitersLocked(bucketKey string, limit int) {
	bucket := p.buckets[bucketKey]
	queue := p.queues[bucketKey]

	for bucket.count < limit && len(queue) > 0 {
		waiter := queue[0]
		queue = queue[1:]
		waiter.queued = false
		bucket.count++
		close(waiter.ready)
	}

	if len(queue) == 0 {
		delete(p.queues, bucketKey)
	} else {
		p.queues[bucketKey] = queue
	}
}

func (p *RateLimitProvider) cancel(bucketKey string, waiter *queuedRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !waiter.queued {
		return
	}

	queue := p.queues[bucketKey]
	for i, item := range queue {
		if item == waiter {
			p.queues[bucketKey] = append(queue[:i], queue[i+1:]...)
			waiter.queued = false
			return
		}
	}
}

func (p *RateLimitProvider) timeUntilNextBucket() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timeUntilNextBucketLocked()
}

func (p *RateLimitProvider) timeUntilNextBucketLocked() time.Duration {
	now := p.clock()
	elapsed := now.Sub(p.bucketStart(now))
	remaining := p.config.Window - elapsed
	if remaining <= 0 {
		return time.Millisecond
	}
	return remaining
}

func (p *RateLimitProvider) estimateWaitLocked(position, limit int) time.Duration {
	if position <= 1 {
		return p.timeUntilNextBucketLocked()
	}
	windowsAhead := (position - 1 + limit - 1) / limit
	return p.timeUntilNextBucketLocked() + time.Duration(windowsAhead-1)*p.config.Window
}

func (p *RateLimitProvider) bucketStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Unix(0, u.UnixNano()/p.config.Window.Nanoseconds()*p.config.Window.Nanoseconds()).UTC()
}

func (p *RateLimitProvider) sameWindow(a, b time.Time) bool {
	return p.bucketStart(a).Equal(p.bucketStart(b))
}
