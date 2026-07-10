package dbdegradation

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MonitorConfig 监控器配置
type MonitorConfig struct {
	CheckInterval    time.Duration // 检查间隔，默认 10s
	FailThreshold    int           // 失败阈值，连续失败多少次后切换到降级模式，默认 3
	RecoverThreshold int           // 恢复阈值，连续成功多少次后切换回正常模式，默认 3
}

// Monitor 数据库健康监控器
type Monitor struct {
	db            *pgxpool.Pool
	status        atomic.Value // DBStatus
	config        MonitorConfig
	listeners     []StatusChangeListener
	listenersMu   sync.RWMutex
	stopCh        chan struct{}
	doneCh        chan struct{}
	failCount     int
	successCount  int
	lastCheckTime time.Time
	degradedSince time.Time // 降级开始时间
	mu            sync.Mutex
}

// NewMonitor 创建监控器
func NewMonitor(db *pgxpool.Pool, config MonitorConfig) *Monitor {
	if config.CheckInterval <= 0 {
		config.CheckInterval = 10 * time.Second
	}
	if config.FailThreshold <= 0 {
		config.FailThreshold = 3
	}
	if config.RecoverThreshold <= 0 {
		config.RecoverThreshold = 3
	}

	m := &Monitor{
		db:     db,
		config: config,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	m.status.Store(DBStatusUnknown)
	return m
}

// Start 启动监控
func (m *Monitor) Start(ctx context.Context) {
	go m.runLoop(ctx)
}

// Stop 停止监控
func (m *Monitor) Stop() {
	close(m.stopCh)
	<-m.doneCh
}

// GetStatus 获取当前状态
func (m *Monitor) GetStatus() DBStatus {
	return m.status.Load().(DBStatus)
}

// AddListener 注册状态变更监听器
func (m *Monitor) AddListener(listener StatusChangeListener) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	m.listeners = append(m.listeners, listener)
}

// GetLastCheckTime 获取最后检查时间
func (m *Monitor) GetLastCheckTime() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastCheckTime
}

// GetDegradedDuration 获取降级持续时间
func (m *Monitor) GetDegradedDuration() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetStatus() == DBStatusDegraded && !m.degradedSince.IsZero() {
		return time.Since(m.degradedSince)
	}
	return 0
}

// runLoop 监控循环
func (m *Monitor) runLoop(ctx context.Context) {
	defer close(m.doneCh)
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	// 首次检查
	m.checkHealth(ctx)

	for {
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkHealth(ctx)
		}
	}
}

// checkHealth 执行健康检查
func (m *Monitor) checkHealth(ctx context.Context) {
	m.mu.Lock()
	m.lastCheckTime = time.Now()
	m.mu.Unlock()

	// 2 秒超时
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	err := m.db.Ping(checkCtx)

	m.mu.Lock()
	defer m.mu.Unlock()

	currentStatus := m.GetStatus()
	var newStatus DBStatus

	if err != nil {
		slog.Debug("database health check failed", "error", err)
		m.failCount++
		m.successCount = 0

		// 连续失败达到阈值，切换到降级模式
		if m.failCount >= m.config.FailThreshold && currentStatus != DBStatusDegraded {
			newStatus = DBStatusDegraded
			m.degradedSince = time.Now()
		} else {
			newStatus = currentStatus
		}
	} else {
		slog.Debug("database health check succeeded")
		m.successCount++
		m.failCount = 0

		// 连续成功达到阈值，切换到可用模式
		if m.successCount >= m.config.RecoverThreshold && currentStatus != DBStatusAvailable {
			newStatus = DBStatusAvailable
			m.degradedSince = time.Time{} // 清零
		} else if currentStatus == DBStatusUnknown {
			// 首次检查成功
			newStatus = DBStatusAvailable
		} else {
			newStatus = currentStatus
		}
	}

	// 状态变更，触发事件
	if newStatus != currentStatus {
		m.status.Store(newStatus)
		event := StatusChangeEvent{
			OldStatus: currentStatus,
			NewStatus: newStatus,
			Timestamp: time.Now(),
			Message:   m.buildStatusMessage(newStatus, err),
		}
		m.notifyListeners(event)
	}
}

// buildStatusMessage 构建状态消息
func (m *Monitor) buildStatusMessage(status DBStatus, err error) string {
	switch status {
	case DBStatusAvailable:
		return "database is available"
	case DBStatusDegraded:
		if err != nil {
			return "database is unavailable: " + err.Error()
		}
		return "database is unavailable"
	default:
		return "database status unknown"
	}
}

// notifyListeners 通知所有监听器
func (m *Monitor) notifyListeners(event StatusChangeEvent) {
	m.listenersMu.RLock()
	listeners := make([]StatusChangeListener, len(m.listeners))
	copy(listeners, m.listeners)
	m.listenersMu.RUnlock()

	for _, listener := range listeners {
		// 异步通知，避免阻塞
		go func(l StatusChangeListener) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("listener panic", "panic", r)
				}
			}()
			l(event)
		}(listener)
	}
}
