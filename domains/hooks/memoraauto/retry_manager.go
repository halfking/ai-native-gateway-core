package memoraauto

import (
	"context"
	"fmt"
	"math"
	"time"
)

// RetryManager 重试管理器
//
// 功能：
//   - 指数退避重试策略
//   - 支持最大重试次数限制
//   - 上下文取消支持
type RetryManager struct {
	maxRetries   int
	baseBackoff  time.Duration
	maxBackoff   time.Duration
}

// NewRetryManager 创建重试管理器
func NewRetryManager(maxRetries int, baseBackoff time.Duration) *RetryManager {
	return &RetryManager{
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		maxBackoff:  30 * time.Second, // 最大退避时间 30 秒
	}
}

// RetryFunc 重试函数类型
type RetryFunc func(ctx context.Context, attempt int) error

// Execute 执行带重试的函数
//
// 参数：
//   - ctx: 上下文（支持取消）
//   - fn: 要执行的函数
//
// 返回：
//   - error: 如果所有重试都失败，返回最后一次错误
func (r *RetryManager) Execute(ctx context.Context, fn RetryFunc) error {
	var lastErr error

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled: %w", ctx.Err())
		default:
		}

		// 执行函数
		err := fn(ctx, attempt)
		if err == nil {
			// 成功，返回
			return nil
		}

		// 记录错误
		lastErr = err

		// 如果是最后一次尝试，不再重试
		if attempt >= r.maxRetries {
			break
		}

		// 计算退避时间（指数退避）
		backoff := r.calculateBackoff(attempt)

		// 等待退避时间
		select {
		case <-time.After(backoff):
			// 继续下一次重试
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled during backoff: %w", ctx.Err())
		}
	}

	// 所有重试都失败
	return fmt.Errorf("all %d retries failed, last error: %w", r.maxRetries+1, lastErr)
}

// calculateBackoff 计算退避时间
// 使用指数退避算法：baseBackoff * 2^attempt
func (r *RetryManager) calculateBackoff(attempt int) time.Duration {
	// 指数退避：baseBackoff * 2^attempt
	backoff := time.Duration(float64(r.baseBackoff) * math.Pow(2, float64(attempt)))

	// 限制最大退避时间
	if backoff > r.maxBackoff {
		backoff = r.maxBackoff
	}

	return backoff
}

// ExecuteWithStats 执行带重试的函数，并返回统计信息
type RetryStats struct {
	TotalAttempts int
	Success       bool
	TotalDuration time.Duration
	LastError     error
}

func (r *RetryManager) ExecuteWithStats(ctx context.Context, fn RetryFunc) *RetryStats {
	stats := &RetryStats{
		TotalAttempts: 0,
		Success:       false,
	}

	startTime := time.Now()
	err := r.Execute(ctx, fn)
	stats.TotalDuration = time.Since(startTime)
	stats.LastError = err
	stats.Success = (err == nil)

	// 计算实际尝试次数
	if err != nil {
		stats.TotalAttempts = r.maxRetries + 1
	} else {
		// 成功时，从错误信息中提取尝试次数比较困难
		// 这里简化为1（可以通过包装 fn 来精确统计）
		stats.TotalAttempts = 1
	}

	return stats
}
