// Package monitoring 提供进程内、无外部依赖的轻量监控指标。
//
// 当前仅包含双模式存储架构（Task 5.3）的存储分层指标 StorageMetrics：
// 以 sync/atomic 计数器记录 L1 / L1.5 / L2 缓存命中、L3 查询延迟与
// 写入统计，供 GET /metrics/storage 端点（cmd/gateway）以 JSON 快照形式
// 暴露。刻意不接入 prometheus registry：本指标面向双模式（full 模式下
// 分层结构与 lite 不同），JSON 快照比固定 label 的 Prometheus 序列更贴合。
// 2026-09-24 方案 §3-H5（P5）：hotzone 维度——镜像写入与 L1.5 命中按存储
// 模式（full/lite）分列，以新增 JSON 字段透出，既有键保持兼容。
package monitoring

import (
	"sync"
	"sync/atomic"
	"time"
)

// 存储模式标签值域（H5 维度）。与 storage.StorageMode 及端点 mode 字段
// 同词表，但在本包本地定义：storage/file 反向 import 了 monitoring，
// monitoring 再依赖 storage 会成环。
const (
	// ModeFull 完整模式（PG + Redis，热区为本地镜像层）。
	ModeFull = "full"
	// ModeLite 轻量模式（SQLite + File + Memory）。
	ModeLite = "lite"
)

// modeIndex 把模式标签映射到分桶下标；未知/空标签返回 -1（不落桶，
// 仅累计无维度计数器，保证未接线路径行为与历史一致）。
func modeIndex(mode string) int {
	switch mode {
	case ModeFull:
		return 0
	case ModeLite:
		return 1
	}
	return -1
}

// modeIndexOf 把 activeMode 的编码值转回桶下标。
func modeIndexOf(v uint32) int {
	switch v {
	case 1:
		return 0 // ModeFull
	case 2:
		return 1 // ModeLite
	}
	return -1
}

// StorageMetrics 存储分层指标（并发安全）：
// 全部字段为 atomic 计数器，Record* 可在任意请求路径热路径上无锁调用，
// Snapshot 读取全部走 atomic.Load，保证不出现数据竞争。
type StorageMetrics struct {
	l1Hits          atomic.Uint64 // L1（进程内 SessionCacheV2）命中
	l1Misses        atomic.Uint64 // L1 未命中
	l15Hits         atomic.Uint64 // L1.5（本地文件缓存 FileCache，两模式共享）命中
	l15Misses       atomic.Uint64 // L1.5 未命中
	l2Hits          atomic.Uint64 // L2（Redis）命中
	l2Misses        atomic.Uint64 // L2 未命中
	l3Queries       atomic.Uint64 // L3（数据库回源）查询次数
	l3LatencyUS     atomic.Uint64 // L3 查询累计延迟（微秒），与 l3Queries 配合求均值
	writes          atomic.Uint64 // 成功写入次数
	writeBytes      atomic.Uint64 // 成功写入字节总数
	writeErrors     atomic.Uint64 // 失败写入次数
	mirrorWrites    atomic.Uint64 // 请求侧镜像（H3）成功写入次数
	mirrorErrors    atomic.Uint64 // 请求侧镜像失败次数
	hotzoneEnabled  atomic.Bool   // 热区是否启用（runtime 标记，便于 /metrics/storage 透出）
	mirrorHotZoneOn atomic.Bool   // 镜像是否仍记录到 hotzone（H4 开关）

	// hotzone 维度（H5，P5）：按模式分列的计数。固定两槽（modeIndex 映射）
	// 而非 map+锁，Record* 路径保持无锁 atomic 语义。
	activeMode    atomic.Uint32    // 进程级模式戳：0=未设置 1=full 2=lite（见 SetStorageMode）
	modeMirrorOK  [2]atomic.Uint64 // 按 mode 分列的镜像成功写入
	modeMirrorErr [2]atomic.Uint64 // 按 mode 分列的镜像失败
	modeL15Hits   [2]atomic.Uint64 // 按 mode 分列的 L1.5（= 热区读层）命中
	modeL15Misses [2]atomic.Uint64 // 按 mode 分列的 L1.5 未命中

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
// 无维度合计口径，保留给既有调用方；生产读链请用 RecordL15HitForMode。
func (m *StorageMetrics) RecordL15Hit() { m.l15Hits.Add(1) }

// RecordL15Miss 记录一次 L1.5（文件缓存）未命中。
// 无维度合计口径，保留给既有调用方；生产读链请用 RecordL15MissForMode。
func (m *StorageMetrics) RecordL15Miss() { m.l15Misses.Add(1) }

// RecordL15HitForMode 记录一次 L1.5 命中并计入 mode 维度（H5）。
// 与 RecordL15Hit 同源累加合计计数器，另落 per-mode 桶；mode 取
// ModeFull/ModeLite，其他值仅累计合计（不落桶）。
func (m *StorageMetrics) RecordL15HitForMode(mode string) {
	m.l15Hits.Add(1)
	if i := modeIndex(mode); i >= 0 {
		m.modeL15Hits[i].Add(1)
	}
}

// RecordL15MissForMode 同 RecordL15HitForMode 的未命中版。
func (m *StorageMetrics) RecordL15MissForMode(mode string) {
	m.l15Misses.Add(1)
	if i := modeIndex(mode); i >= 0 {
		m.modeL15Misses[i].Add(1)
	}
}

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

// RecordMirrorWrite 记录一次请求侧镜像写入（H3）。
// failed=true 时仅 mirrorErrors 累加（不计入 mirrorWrites）。nil 镜像器
// 调用方也会走此路径以保持「请求路径热计数」语义。
// 兼容说明（H5，P5）：签名与无维度计数口径不变；另按进程模式戳
// （SetStorageMode）落 per-mode 桶——镜像写入方（storage/file/
// request_mirror.go）不感知模式，而进程只运行一种存储模式（initStorageMode：
// lite 建 runtime / 其余为 full 语义，端点 storageModeLabel 同口径），故由
// 启动装配统一盖章；未盖章时 per-mode 桶恒 0，无维度计数不受影响。
func (m *StorageMetrics) RecordMirrorWrite(failed bool) {
	if failed {
		m.mirrorErrors.Add(1)
		if i := modeIndexOf(m.activeMode.Load()); i >= 0 {
			m.modeMirrorErr[i].Add(1)
		}
		return
	}
	m.mirrorWrites.Add(1)
	if i := modeIndexOf(m.activeMode.Load()); i >= 0 {
		m.modeMirrorOK[i].Add(1)
	}
}

// SetStorageMode 盖进程级存储模式戳（H5，P5）。启动装配期调用一次：
// initStorageMode 在 lite 装配处传 ModeLite、full/未启用双模式传 ModeFull。
// 未盖章时 per-mode 桶恒为 0，无维度计数不受影响。重复调用以最后一次为准
// （测试进程多次装配同一单例即此语义；数值断言类测试请注入独立实例）。
func (m *StorageMetrics) SetStorageMode(mode string) {
	switch mode {
	case ModeFull:
		m.activeMode.Store(1)
	case ModeLite:
		m.activeMode.Store(2)
	}
}

// SetHotZoneEnabled 标记当前进程是否启用了热区层（H2/H4 接线）。
// 启动期 initStorageMode 调用一次；hotconfig 变更后也可再次调用。
func (m *StorageMetrics) SetHotZoneEnabled(enabled bool) {
	m.hotzoneEnabled.Store(enabled)
}

// SetMirrorHotZoneOn 标记镜像是否仍写入热区（H4 控制开关）。
func (m *StorageMetrics) SetMirrorHotZoneOn(enabled bool) {
	m.mirrorHotZoneOn.Store(enabled)
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
	mirrorWrites, mirrorErrors := m.mirrorWrites.Load(), m.mirrorErrors.Load()

	var uptimeSeconds float64
	if startNs := m.startTime.Load(); startNs > 0 {
		uptimeSeconds = time.Since(time.Unix(0, startNs)).Seconds()
		if uptimeSeconds < 0 {
			uptimeSeconds = 0 // 时钟回拨防御
		}
	}

	// H5 per-mode 分列：与上方合计读数同批 Load（单快照内近似自洽；
	// per-mode 命中率分子分母来自各自桶的一次 Load 对，做法与 layerSnapshot 一致）。
	l15ByMode := map[string]interface{}{
		ModeFull: layerSnapshot(m.modeL15Hits[0].Load(), m.modeL15Misses[0].Load()),
		ModeLite: layerSnapshot(m.modeL15Hits[1].Load(), m.modeL15Misses[1].Load()),
	}
	mirrorByMode := map[string]interface{}{
		ModeFull: map[string]interface{}{
			"total":  m.modeMirrorOK[0].Load(),
			"errors": m.modeMirrorErr[0].Load(),
		},
		ModeLite: map[string]interface{}{
			"total":  m.modeMirrorOK[1].Load(),
			"errors": m.modeMirrorErr[1].Load(),
		},
	}

	return map[string]interface{}{
		"uptime_seconds": uptimeSeconds,
		"l1":             layerSnapshot(l1Hits, l1Misses),
		"l1_5":           layerSnapshot(l15Hits, l15Misses),
		"l1_5_by_mode":   l15ByMode,
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
		"mirror": map[string]interface{}{
			"total":         mirrorWrites,
			"errors":        mirrorErrors,
			"by_mode":       mirrorByMode,
			"hotzone_on":    m.mirrorHotZoneOn.Load(),
			"hotzone_bytes": writeBytes, // alias：当前实现与 writes 共用字节计数
		},
		"hotzone": map[string]interface{}{
			// hotzone_hit_total（方案 §3-H5.1 命名）：热区读层即 L1.5，
			// 与 l1_5_by_mode[*].hits 同源别名，不设独立计数器（避免双计数漂移）。
			"hit_total_by_mode": map[string]interface{}{
				ModeFull: m.modeL15Hits[0].Load(),
				ModeLite: m.modeL15Hits[1].Load(),
			},
		},
		"hotzone_enabled": m.hotzoneEnabled.Load(),
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
