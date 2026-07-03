package memoraauto

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// IdleDetector 会话空闲检测器
//
// 功能：
//   - 跟踪会话的请求计数和最后活动时间
//   - 判断会话是否满足空闲条件
//   - 线程安全
type IdleDetector struct {
	mu             sync.RWMutex
	sessions       map[string]*SessionStats
	idleThreshold  time.Duration
	minRequestCount int
}

// NewIdleDetector 创建空闲检测器
func NewIdleDetector(idleThreshold time.Duration, minRequestCount int) *IdleDetector {
	return &IdleDetector{
		sessions:        make(map[string]*SessionStats),
		idleThreshold:   idleThreshold,
		minRequestCount: minRequestCount,
	}
}

// Track 跟踪会话活动
func (d *IdleDetector) Track(ctx context.Context, sessionKey, taskID, tenantID string) error {
	if sessionKey == "" {
		return fmt.Errorf("session_key is required")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	stats, exists := d.sessions[sessionKey]
	if !exists {
		// 首次见到该会话
		stats = &SessionStats{
			SessionKey:   sessionKey,
			TaskID:       taskID,
			TenantID:     tenantID,
			RequestCount: 0,
			CreatedAt:    time.Now(),
			LastActive:   time.Now(),
		}
		d.sessions[sessionKey] = stats
	}

	// 更新统计
	stats.RequestCount++
	// 只更新 LastActive（每次请求都更新）
	stats.LastActive = time.Now()
	
	return nil
}

// CheckIdle 检查会话是否空闲
func (d *IdleDetector) CheckIdle(ctx context.Context, sessionKey string) (bool, *SessionStats, error) {
	if sessionKey == "" {
		return false, nil, fmt.Errorf("session_key is required")
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	stats, exists := d.sessions[sessionKey]
	if !exists {
		return false, nil, fmt.Errorf("session not tracked: %s", sessionKey)
	}

	// 检查是否满足空闲条件
	if stats.RequestCount < d.minRequestCount {
		return false, stats, nil
	}

	idleDuration := time.Since(stats.LastActive)
	isIdle := idleDuration > d.idleThreshold

	return isIdle, stats, nil
}

// MarkProcessed 标记会话已处理（从跟踪中移除）
func (d *IdleDetector) MarkProcessed(ctx context.Context, sessionKey string) error {
	if sessionKey == "" {
		return fmt.Errorf("session_key is required")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.sessions, sessionKey)
	return nil
}

// GetStats 获取会话统计信息
func (d *IdleDetector) GetStats(ctx context.Context, sessionKey string) (*SessionStats, error) {
	if sessionKey == "" {
		return nil, fmt.Errorf("session_key is required")
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	stats, exists := d.sessions[sessionKey]
	if !exists {
		return nil, fmt.Errorf("session not tracked: %s", sessionKey)
	}

	// 返回副本，避免外部修改
	statsCopy := *stats
	return &statsCopy, nil
}

// CleanupOldSessions 清理旧会话（定期维护）
// 移除超过指定时间未活动的会话记录
func (d *IdleDetector) CleanupOldSessions(ctx context.Context, maxAge time.Duration) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	removed := 0

	for key, stats := range d.sessions {
		if now.Sub(stats.LastActive) > maxAge {
			delete(d.sessions, key)
			removed++
		}
	}

	return removed
}

// Size 返回当前跟踪的会话数
func (d *IdleDetector) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.sessions)
}

// SetLastActiveForTest 设置会话的最后活动时间（仅用于测试）
func (d *IdleDetector) SetLastActiveForTest(sessionKey string, lastActive time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	stats, exists := d.sessions[sessionKey]
	if !exists {
		return fmt.Errorf("session not tracked: %s", sessionKey)
	}

	stats.LastActive = lastActive
	return nil
}
