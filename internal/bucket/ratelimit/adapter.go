package ratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

// 兼容原有的错误定义
var (
	ErrMinuteBucketFull        = errors.New("rate limit queue full")
	ErrMinuteBucketWaitTimeout = errors.New("rate limit queue wait timeout")
	ErrQueueBudgetExceeded     = errors.New("queue budget exceeded")
)

// AdmissionResult 兼容原有的准入结果结构
// 保持与 ratelimit.AdmissionResult 完全相同的字段
type AdmissionResult struct {
	Admitted       bool
	Waiting        bool
	Position       int
	Limit          int
	Remaining      int
	QueueRemaining int
	// EstimatedWaitSec is the server's estimate of queue wait time in
	// seconds, populated when the request is queued or rejected for budget
	// reasons. Used to write Retry-After on the 429 fast-reject path.
	EstimatedWaitSec int
}

// MinuteBucketAdmission 适配器，保持与原有接口完全兼容
// 内部使用新的 RateLimitProvider 实现
type MinuteBucketAdmission struct {
	provider *RateLimitProvider
	window   time.Duration
}

// NewMinuteBucketAdmission 创建一个新的分钟桶准入控制器
// 保持与原有构造函数签名相同
func NewMinuteBucketAdmission() *MinuteBucketAdmission {
	config := DefaultConfig()
	return &MinuteBucketAdmission{
		provider: NewRateLimitProvider(config),
		window:   config.Window,
	}
}

// NewMinuteBucketAdmissionWithConfig 使用自定义配置创建准入控制器
func NewMinuteBucketAdmissionWithConfig(config Config) *MinuteBucketAdmission {
	return &MinuteBucketAdmission{
		provider: NewRateLimitProvider(config),
		window:   config.Window,
	}
}

// Admit 请求准入（快速失败，不等待）
// 保持与原有 Admit 方法签名相同
func (a *MinuteBucketAdmission) Admit(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, 0, nil)
}

// AdmitRPM 请求准入（RPM限流，快速失败）
// 保持与原有 AdmitRPM 方法签名相同
func (a *MinuteBucketAdmission) AdmitRPM(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return a.Admit(ctx, keyID, limit)
}

// AdmitRPMWithWait 请求准入并等待（如果队列未满）
// 保持与原有 AdmitRPMWithWait 方法签名相同
func (a *MinuteBucketAdmission) AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, 0, notify)
}

// AdmitRPMWithBudget 请求准入并带预算控制
// 保持与原有 AdmitRPMWithBudget 方法签名相同
func (a *MinuteBucketAdmission) AdmitRPMWithBudget(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, maxWait, notify)
}

// admit 内部准入逻辑实现
func (a *MinuteBucketAdmission) admit(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error) {
	// 构造 BucketKey
	now := time.Now().UTC()
	windowStart := now.Truncate(a.window)
	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "",
		ResourceID: string(rune(keyID)), // 将 keyID 转换为字符串
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(a.window),
			Size:  a.window,
		},
	}

	// 构造查询参数
	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "",
		ResourceID: string(rune(keyID)),
		Window:     key.Window,
		Params: map[string]interface{}{
			"limit":    limit,
			"max_wait": maxWait,
		},
	}

	// 如果有notify回调，包装为core.AdmissionResult格式
	if notify != nil {
		query.Params["notify"] = func(coreResult core.AdmissionResult) {
			// 转换为兼容格式
			legacyResult := AdmissionResult{
				Admitted:         coreResult.Admitted,
				Waiting:          coreResult.Waiting,
				Position:         coreResult.Position,
				Limit:            coreResult.Limit,
				Remaining:        coreResult.Remaining,
				QueueRemaining:   coreResult.QueueRemaining,
				EstimatedWaitSec: coreResult.EstimatedWaitSec,
			}
			notify(legacyResult)
		}
	}

	// 调用底层provider
	result, err := a.provider.Query(ctx, query)

	// 转换结果
	if err != nil {
		// 转换错误类型
		if errors.Is(err, core.ErrBucketExceeded) {
			if reason, ok := result.Metadata["reason"].(string); ok && reason == "queue_full" {
				return a.resultFromMetadata(result.Metadata, limit), ErrMinuteBucketFull
			}
			if reason, ok := result.Metadata["reason"].(string); ok && reason == "budget_exceeded" {
				return a.resultFromMetadata(result.Metadata, limit), ErrQueueBudgetExceeded
			}
		}
		if errors.Is(err, core.ErrBucketWaitTimeout) {
			return a.resultFromMetadata(result.Metadata, limit), ErrMinuteBucketWaitTimeout
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return a.resultFromMetadata(result.Metadata, limit), err
		}
		return AdmissionResult{}, err
	}

	// 成功情况
	return a.resultFromMetadata(result.Metadata, limit), nil
}

// resultFromMetadata 从metadata构造AdmissionResult
func (a *MinuteBucketAdmission) resultFromMetadata(metadata map[string]interface{}, limit int) AdmissionResult {
	result := AdmissionResult{
		Limit: limit,
	}

	if admitted, ok := metadata["admitted"].(bool); ok {
		result.Admitted = admitted
	}

	if remaining, ok := metadata["remaining"].(int); ok {
		result.Remaining = remaining
	}

	if position, ok := metadata["position"].(int); ok {
		result.Position = position
		result.Waiting = true
	}

	if queueRemaining, ok := metadata["queue_remaining"].(int); ok {
		result.QueueRemaining = queueRemaining
	}

	if estimatedWaitSec, ok := metadata["estimated_wait_sec"].(int); ok {
		result.EstimatedWaitSec = estimatedWaitSec
	}

	return result
}

// GetProvider 获取底层的 Provider（用于高级用途）
func (a *MinuteBucketAdmission) GetProvider() *RateLimitProvider {
	return a.provider
}

// GetMetrics 获取指标（新增方法）
func (a *MinuteBucketAdmission) GetMetrics(ctx context.Context) (*core.BucketMetrics, error) {
	return a.provider.GetMetrics(ctx)
}

// Reset 重置指定key的桶状态（新增方法）
func (a *MinuteBucketAdmission) Reset(ctx context.Context, keyID int) error {
	now := time.Now().UTC()
	windowStart := now.Truncate(a.window)
	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "",
		ResourceID: string(rune(keyID)),
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(a.window),
			Size:  a.window,
		},
	}
	return a.provider.Reset(ctx, key)
}
