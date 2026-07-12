package enrollment

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// HeartbeatSender 心跳发送器
type HeartbeatSender struct {
	client   *Client
	interval time.Duration
	payload  func() HeartbeatPayload // 动态获取 payload 的函数

	mu       sync.Mutex
	ticker   *time.Ticker
	stopChan chan struct{}
	stopped  bool
}

// NewHeartbeatSender 创建心跳发送器
func NewHeartbeatSender(client *Client, interval time.Duration, payloadFunc func() HeartbeatPayload) *HeartbeatSender {
	return &HeartbeatSender{
		client:   client,
		interval: interval,
		payload:  payloadFunc,
		stopChan: make(chan struct{}),
	}
}

// Start 启动心跳发送器（每 interval 发送一次）
func (s *HeartbeatSender) Start(ctx context.Context) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		slog.Warn("heartbeat sender already stopped, cannot start")
		return
	}
	if s.ticker != nil {
		s.mu.Unlock()
		slog.Warn("heartbeat sender already started")
		return
	}

	s.ticker = time.NewTicker(s.interval)
	ticker := s.ticker // 保存本地副本，防止 Stop() 后置 nil
	s.mu.Unlock()

	slog.Info("heartbeat sender started", "interval", s.interval)

	// 立即发送一次心跳
	s.sendWithRetry(ctx)

	// 定时发送
	go func() {
		for {
			select {
			case <-ticker.C:
				s.sendWithRetry(ctx)
			case <-s.stopChan:
				slog.Info("heartbeat sender stopped")
				return
			case <-ctx.Done():
				slog.Info("heartbeat sender context canceled")
				return
			}
		}
	}()
}

// Stop 优雅关闭心跳发送器
func (s *HeartbeatSender) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	s.stopped = true

	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}

	// 关闭 stopChan 通知 goroutine 退出
	select {
	case <-s.stopChan:
		// 已关闭
	default:
		close(s.stopChan)
	}

	slog.Info("heartbeat sender shutdown completed")
}

// sendWithRetry 发送心跳，失败自动重试（指数退避）
// 失败 3 次后退避：5s / 30s / 120s
func (s *HeartbeatSender) sendWithRetry(ctx context.Context) {
	payload := s.payload()

	var lastErr error
	backoffDurations := []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

	for attempt := 0; attempt < 3; attempt++ {
		// 为每次请求创建超时 context（避免在测试中长时间等待）
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.client.SendHeartbeat(reqCtx, payload)
		cancel()

		if err == nil {
			if attempt > 0 {
				slog.Info("heartbeat sent successfully after retry", "attempt", attempt+1)
			}
			return
		}

		lastErr = err
		slog.Warn("heartbeat send failed",
			"attempt", attempt+1,
			"error", err.Error(),
			"next_retry", backoffDurations[attempt])

		// 指数退避
		select {
		case <-time.After(backoffDurations[attempt]):
			// 继续重试
		case <-ctx.Done():
			slog.Warn("heartbeat retry canceled by context")
			return
		case <-s.stopChan:
			slog.Warn("heartbeat retry canceled by stop signal")
			return
		}
	}

	// 3 次失败后放弃，不阻塞主循环
	slog.Error("heartbeat failed after 3 retries",
		"last_error", lastErr.Error(),
		"instance_id", payload.InstanceID)
}
