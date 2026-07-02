// Package clientprofile — RetryableWorker（Task E）。
//
// RetryableWorker 是 ProfileWorker 的薄包装：在 ProfileWorker 之上
// 加上指数退避重试。ProfileWorker 自身已经处理了"无效事件返回 nil"，
// 因此重试仅对真正的 UpdateProfile 错误有意义。
//
// 使用模式：
//
//	rw := clientprofile.NewRetryableWorker(inner, 3, 100*time.Millisecond, logger)
//	if err := rw.Handle(ctx, evt); err != nil { ... }
package clientprofile

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// RetryableWorker 带指数退避的 worker 装饰器。
type RetryableWorker struct {
	worker     *ProfileWorker
	maxRetries int
	backoff    time.Duration
	logger     *slog.Logger
}

// NewRetryableWorker 构造装饰器。
//
//   - maxRetries <= 0：禁用重试（等价于直接调 inner.Handle）
//   - backoff <= 0：默认 100ms
func NewRetryableWorker(worker *ProfileWorker, maxRetries int, backoff time.Duration, logger *slog.Logger) *RetryableWorker {
	if worker == nil {
		return nil
	}
	if maxRetries <= 0 {
		maxRetries = 1
	}
	if backoff <= 0 {
		backoff = 100 * time.Millisecond
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RetryableWorker{
		worker:     worker,
		maxRetries: maxRetries,
		backoff:    backoff,
		logger:     logger,
	}
}

// Name 透传 inner Name。
func (r *RetryableWorker) Name() string { return r.worker.Name() }

// SubscribedTypes 透传 inner SubscribedTypes。
func (r *RetryableWorker) SubscribedTypes() []analysis.EventType {
	return r.worker.SubscribedTypes()
}

// Handle 重试 inner.Handle 直到成功或用完次数。
//
// 退避策略：2^i * backoff（i 从 0 起），最后一次失败不等待。
// 整体等待时间：backoff * (2^maxRetries - 1) — 调用方应设置
// maxRetries <= 5 以避免 goroutine 长时间持有。
func (r *RetryableWorker) Handle(ctx context.Context, evt analysis.AnalysisEvent) error {
	if r == nil {
		return nil
	}
	var lastErr error
	for i := 0; i < r.maxRetries; i++ {
		err := r.worker.Handle(ctx, evt)
		if err == nil {
			return nil
		}
		lastErr = err

		// 退避前判 ctx 取消：上游退出时不浪费等待
		if i < r.maxRetries-1 {
			wait := r.backoff << i
			select {
			case <-ctx.Done():
				return fmt.Errorf("retry aborted: %w", ctx.Err())
			case <-time.After(wait):
			}
		}
	}
	r.logger.Warn("client_profile_worker: retries exhausted",
		"event_id", evt.EventID,
		"max_retries", r.maxRetries,
		"error", lastErr)
	return fmt.Errorf("failed after %d retries: %w", r.maxRetries, lastErr)
}
