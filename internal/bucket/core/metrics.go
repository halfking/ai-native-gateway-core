package core

import (
	"sync"
	"sync/atomic"
	"time"
)

// SimpleMetricsCollector 简单的指标收集器实现
type SimpleMetricsCollector struct {
	// 按桶类型分组的指标
	metrics map[BucketType]*bucketMetrics
	mu      sync.RWMutex
}

type bucketMetrics struct {
	totalRequests   atomic.Int64
	successRequests atomic.Int64
	failedRequests  atomic.Int64
	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64

	// 延迟统计
	latencies    []time.Duration
	latenciesMu  sync.Mutex
	maxLatency   atomic.Int64 // 纳秒
	totalLatency atomic.Int64 // 纳秒

	// 配额统计
	quotaUsages map[string]*quotaUsage // key: tenantID
	quotaMu     sync.RWMutex

	// 准入统计
	admittedCount atomic.Int64
	rejectedCount atomic.Int64
	totalWaitTime atomic.Int64 // 纳秒
}

type quotaUsage struct {
	Used     int64
	Capacity int64
}

// NewSimpleMetricsCollector 创建新的指标收集器
func NewSimpleMetricsCollector() *SimpleMetricsCollector {
	return &SimpleMetricsCollector{
		metrics: make(map[BucketType]*bucketMetrics),
	}
}

// getOrCreateMetrics 获取或创建指定类型的指标
func (c *SimpleMetricsCollector) getOrCreateMetrics(bucketType BucketType) *bucketMetrics {
	c.mu.RLock()
	m, ok := c.metrics[bucketType]
	c.mu.RUnlock()

	if ok {
		return m
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 双重检查
	if m, ok := c.metrics[bucketType]; ok {
		return m
	}

	m = &bucketMetrics{
		latencies:   make([]time.Duration, 0, 1000),
		quotaUsages: make(map[string]*quotaUsage),
	}
	c.metrics[bucketType] = m

	return m
}

// RecordRequest 记录一次请求
func (c *SimpleMetricsCollector) RecordRequest(bucketType BucketType, success bool, latency time.Duration) {
	m := c.getOrCreateMetrics(bucketType)

	m.totalRequests.Add(1)
	if success {
		m.successRequests.Add(1)
	} else {
		m.failedRequests.Add(1)
	}

	// 记录延迟
	ns := latency.Nanoseconds()
	m.totalLatency.Add(ns)

	// 更新最大延迟
	for {
		current := m.maxLatency.Load()
		if ns <= current {
			break
		}
		if m.maxLatency.CompareAndSwap(current, ns) {
			break
		}
	}

	// 保存延迟样本（用于计算百分位数）
	m.latenciesMu.Lock()
	m.latencies = append(m.latencies, latency)
	// 限制样本数量
	if len(m.latencies) > 10000 {
		m.latencies = m.latencies[1:]
	}
	m.latenciesMu.Unlock()
}

// RecordCacheHit 记录缓存命中
func (c *SimpleMetricsCollector) RecordCacheHit(bucketType BucketType, hit bool) {
	m := c.getOrCreateMetrics(bucketType)

	if hit {
		m.cacheHits.Add(1)
	} else {
		m.cacheMisses.Add(1)
	}
}

// RecordQuotaUsage 记录配额使用
func (c *SimpleMetricsCollector) RecordQuotaUsage(bucketType BucketType, tenantID string, used int64, capacity int64) {
	m := c.getOrCreateMetrics(bucketType)

	m.quotaMu.Lock()
	m.quotaUsages[tenantID] = &quotaUsage{
		Used:     used,
		Capacity: capacity,
	}
	m.quotaMu.Unlock()
}

// RecordAdmission 记录准入结果
func (c *SimpleMetricsCollector) RecordAdmission(bucketType BucketType, admitted bool, waitTime time.Duration) {
	m := c.getOrCreateMetrics(bucketType)

	if admitted {
		m.admittedCount.Add(1)
	} else {
		m.rejectedCount.Add(1)
	}

	if waitTime > 0 {
		m.totalWaitTime.Add(waitTime.Nanoseconds())
	}
}

// GetMetrics 获取指标
func (c *SimpleMetricsCollector) GetMetrics(bucketType BucketType) (*BucketMetrics, error) {
	m := c.getOrCreateMetrics(bucketType)

	totalReq := m.totalRequests.Load()
	avgLatency := time.Duration(0)
	if totalReq > 0 {
		avgLatency = time.Duration(m.totalLatency.Load() / totalReq)
	}

	// 计算百分位数
	m.latenciesMu.Lock()
	p50, p95, p99 := calculatePercentiles(m.latencies)
	m.latenciesMu.Unlock()

	// 计算平均使用率
	m.quotaMu.RLock()
	var totalUsage, totalCapacity int64
	for _, usage := range m.quotaUsages {
		totalUsage += usage.Used
		totalCapacity += usage.Capacity
	}
	m.quotaMu.RUnlock()

	usageRate := 0.0
	if totalCapacity > 0 {
		usageRate = float64(totalUsage) / float64(totalCapacity)
	}

	return &BucketMetrics{
		TotalRequests:    totalReq,
		SuccessRequests:  m.successRequests.Load(),
		FailedRequests:   m.failedRequests.Load(),
		AvgLatency:       avgLatency,
		MaxLatency:       time.Duration(m.maxLatency.Load()),
		P50Latency:       p50,
		P95Latency:       p95,
		P99Latency:       p99,
		CacheHits:        m.cacheHits.Load(),
		CacheMisses:      m.cacheMisses.Load(),
		CurrentCapacity:  totalCapacity,
		CurrentUsed:      totalUsage,
		UsageRate:        usageRate,
		WindowStart:      time.Now().Add(-1 * time.Hour), // 假设1小时窗口
		WindowEnd:        time.Now(),
	}, nil
}

// Reset 重置指标
func (c *SimpleMetricsCollector) Reset(bucketType BucketType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.metrics, bucketType)
}

// calculatePercentiles 计算百分位数
func calculatePercentiles(latencies []time.Duration) (p50, p95, p99 time.Duration) {
	if len(latencies) == 0 {
		return 0, 0, 0
	}

	// 简单实现：对延迟排序并取相应位置的值
	// 注意：这里为了简化，直接遍历计算，实际应该使用更高效的算法
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)

	// 简单的插入排序（对小数据集足够）
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}

	p50Index := int(float64(len(sorted)) * 0.50)
	p95Index := int(float64(len(sorted)) * 0.95)
	p99Index := int(float64(len(sorted)) * 0.99)

	if p50Index >= len(sorted) {
		p50Index = len(sorted) - 1
	}
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	if p99Index >= len(sorted) {
		p99Index = len(sorted) - 1
	}

	return sorted[p50Index], sorted[p95Index], sorted[p99Index]
}
