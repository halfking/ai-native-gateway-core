// Package monitoring 提供进程内、无外部依赖的轻量监控指标。
//
// 当前仅包含双模式存储架构（Task 5.3）的存储分层指标 StorageMetrics：
// 以 sync/atomic 计数器记录 L1 / L1.5 / L2 缓存命中、L3 查询延迟与
// 写入统计，供 GET /metrics/storage 端点（cmd/gateway）以 JSON 快照形式
// 暴露。刻意不接入 prometheus registry：本指标面向双模式（full 模式下
// 分层结构与 lite 不同），JSON 快照比固定 label 的 Prometheus 序列更贴合。
package monitoring

import (
	"sync"
	"sync/atomic"
	"time"
)

// StorageMetrics 存储分层指标（并发安全）：
// 全部字段为 atomic 计数器，Record* 可在任意请求路径热路径上无锁调用，
// Snapshot 读取全部走 atomic.Load，保证不出现数据竞争。
type StorageMetrics struct {
	l1Hits      atomic.Uint64 // L1（进程内 SessionCacheV2）命中
	l1Misses    atomic.Uint64 // L1 未命中
	l15Hits     atomic.Uint64 // L1.5（lite 模式本地文件缓存 FileCache）命中
	l15Misses   atomic.Uint64 // L1.5 未命中
	l2Hits      atomic.Uint64 // L2（Redis）命中
	l2Misses    atomic.Uint64 // L2 未命中
	l3Queries   atomic.Uint64 // L3（数据库回源）查询次数
	l3LatencyUS atomic.Uint64 // L3 查询累计延迟（微秒），与 l3Queries 配合求均值
	writes      atomic.Uint64 // 成功写入次数
	writeBytes  atomic.Uint64 // 成功写入字节总数
	writeErrors atomic.Uint64 // 失败写入次数

	startTime atomic.Int64 // 实例创建时刻（Unix 纳秒），供 uptime_seconds 计算
}

// NewStorageMetrics 创建归零的指标实例，startTime 记为当前时刻。
func NewStorageMetrics() *StorageMetrics {
	m := &StorageMetrics{}
	m.startTime.Store(time.Now().UnixNano())
	return m
}

var (
	defaultMetricsOnce sync.Once
	defaultMetrics     *StorageMetrics
)

// Default 返回进程级单例（sync.Once 惰性初始化）。
// 生产端点与各打点方共享同一实例；测试请用 NewStorageMetrics 注入独立实例，
// 避免单例状态串扰。
func Default() *StorageMetrics {
	defaultMetricsOnce.Do(func() {
		defaultMetrics = NewStorageMetrics()
	})
	return defaultMetrics
}

// RecordL1Hit 记录一次 L1 命中。
func (m *StorageMetrics) RecordL1Hit() { m.l1Hits.Add(1) }

// RecordL1Miss 记录一次 L1 未命中。
func (m *StorageMetrics) RecordL1Miss() { m.l1Misses.Add(1) }

// RecordL15Hit 记录一次 L1.5（文件缓存）命中。
func (m *StorageMetrics) RecordL15Hit() { m.l15Hits.Add(1) }

// RecordL15Miss 记录一次 L1.5（文件缓存）未命中。
func (m *StorageMetrics) RecordL15Miss() { m.l15Misses.Add(1) }

// RecordL2Hit 记录一次 L2（Redis）命中。
func (m *StorageMetrics) RecordL2Hit() { m.l2Hits.Add(1) }

// RecordL2Miss 记录一次 L2（Redis）未命中。
func (m *StorageMetrics) RecordL2Miss() { m.l2Misses.Add(1) }

// RecordL3Query 记录一次 L3（数据库）回源查询及其耗时。
func (m *StorageMetrics) RecordL3Query(latency time.Duration) {
	m.l3Queries.Add(1)
	m.l3LatencyUS.Add(uint64(latency.Microseconds()))
}

// RecordWrite 记录一次底层存储写入。
// 成功：writes / writeBytes 累加；失败：仅 writeErrors 累加（不计入
// total 与 total_bytes），与 storage/file.AsyncFileWriter.record 语义一致。
func (m *StorageMetrics) RecordWrite(bytes int, err error) {
	if err != nil {
		m.writeErrors.Add(1)
		return
	}
	m.writes.Add(1)
	m.writeBytes.Add(uint64(bytes))
}

// Snapshot 返回指标快照（JSON 友好）。
//
// 读取顺序：先把全部计数器 Load 到局部变量再计算比率，保证单次快照内
// 自洽（命中率分子分母来自同一批读数，避免除法前后计数变化导致 >1 或
// 负值）。命中率与平均延迟在分母为 0 时返回 0。
func (m *StorageMetrics) Snapshot() map[string]interface{} {
	// 局部变量快照：后续所有派生值均由这批读数计算。
	l1Hits, l1Misses := m.l1Hits.Load(), m.l1Misses.Load()
	l15Hits, l15Misses := m.l15Hits.Load(), m.l15Misses.Load()
	l2Hits, l2Misses := m.l2Hits.Load(), m.l2Misses.Load()
	l3Queries, l3LatencyUS := m.l3Queries.Load(), m.l3LatencyUS.Load()
	writes, writeBytes, writeErrors := m.writes.Load(), m.writeBytes.Load(), m.writeErrors.Load()

	var uptimeSeconds float64
	if startNs := m.startTime.Load(); startNs > 0 {
		uptimeSeconds = time.Since(time.Unix(0, startNs)).Seconds()
		if uptimeSeconds < 0 {
			uptimeSeconds = 0 // 时钟回拨防御
		}
	}

	return map[string]interface{}{
		"uptime_seconds": uptimeSeconds,
		"l1":             layerSnapshot(l1Hits, l1Misses),
		"l1_5":           layerSnapshot(l15Hits, l15Misses),
		"l2":             layerSnapshot(l2Hits, l2Misses),
		"l3": map[string]interface{}{
			"queries":        l3Queries,
			"avg_latency_ms": avgLatencyMS(l3LatencyUS, l3Queries),
		},
		"writes": map[string]interface{}{
			"total":       writes,
			"total_bytes": writeBytes,
			"errors":      writeErrors,
		},
	}
}

// layerSnapshot 组装单层缓存快照：hits / misses / hit_rate。
// 命中率 = hits / (hits + misses)，分母为 0 时给 0。
func layerSnapshot(hits, misses uint64) map[string]interface{} {
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	return map[string]interface{}{
		"hits":     hits,
		"misses":   misses,
		"hit_rate": rate,
	}
}

// avgLatencyMS 由累计微秒与查询次数计算平均延迟（毫秒）。
// queries 为 0 时返回 0，避免除零。
func avgLatencyMS(totalUS, queries uint64) float64 {
	if queries == 0 {
		return 0
	}
	return float64(totalUS) / float64(queries) / 1000.0
}
