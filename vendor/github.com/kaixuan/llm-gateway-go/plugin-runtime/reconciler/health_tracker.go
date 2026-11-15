package reconciler

import (
	"sync"
	"time"
)

// HealthTracker 跟踪插件启动后的健康窗口。
// 2026-11-15 P2审计修复：Start/Restart成功不立即Reset退避计数，
// 而是记录启动时刻，等持续健康N分钟后再重置——防止慢崩溃环
// （start成功、数分钟后崩）让RetryCount永停在1、MaxRetries不可达。
type HealthTracker struct {
	mu            sync.Mutex
	healthWindow  time.Duration
	startTimes    map[string]time.Time // pluginID -> 最后一次启动时刻
	lastResetTime map[string]time.Time // pluginID -> 最后一次reset时刻（防重复reset）
}

// NewHealthTracker 创建健康窗口跟踪器。
func NewHealthTracker(window time.Duration) *HealthTracker {
	return &HealthTracker{
		healthWindow:  window,
		startTimes:    make(map[string]time.Time),
		lastResetTime: make(map[string]time.Time),
	}
}

// RecordStart 记录插件启动时刻（Start/Restart成功时调用）。
func (h *HealthTracker) RecordStart(pluginID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startTimes[pluginID] = time.Now()
}

// ShouldReset 检查插件是否已持续健康足够长时间、且尚未重置。
// 在 computeActions 的 StateRunning 分支调用：插件正在运行、
// 且启动后已过健康窗口、且上次重置后又过了窗口（防止每30s循环都reset）。
func (h *HealthTracker) ShouldReset(pluginID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	startTime, hasStart := h.startTimes[pluginID]
	if !hasStart {
		// 未记录启动时刻（可能是reconciler重启前启动的），不reset
		return false
	}

	// 检查是否已持续健康足够长
	if time.Since(startTime) < h.healthWindow {
		return false
	}

	// 检查是否已reset过（防止每个reconcile周期都重复reset）
	lastReset, hasReset := h.lastResetTime[pluginID]
	if hasReset && time.Since(lastReset) < h.healthWindow {
		// 上次reset后未满一个窗口，不重复reset
		return false
	}

	return true
}

// MarkReset 标记已重置（ShouldReset返回true并执行Reset后调用）。
func (h *HealthTracker) MarkReset(pluginID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastResetTime[pluginID] = time.Now()
}

// Clear 清理插件的健康跟踪记录（插件被禁用或删除时调用，可选）。
func (h *HealthTracker) Clear(pluginID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.startTimes, pluginID)
	delete(h.lastResetTime, pluginID)
}
