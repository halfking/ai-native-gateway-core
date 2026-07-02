// Package attachments - storage_retry.go
//
// 提供存储操作的重试机制，提高云存储操作的成功率

package attachments

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// RetryConfig 重试配置
type RetryConfig struct {
	// MaxRetries 最大重试次数（0 表示不重试）
	MaxRetries int
	
	// InitialBackoff 初始退避时间
	InitialBackoff time.Duration
	
	// MaxBackoff 最大退避时间
	MaxBackoff time.Duration
	
	// BackoffMultiplier 退避倍数（指数退避）
	BackoffMultiplier float64
	
	// RetryableErrors 可重试的错误类型（为空表示所有错误都重试）
	RetryableErrors []string
}

// DefaultRetryConfig 返回默认重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableErrors: []string{
			"timeout",
			"connection reset",
			"connection refused",
			"temporary failure",
			"TooManyRequests",
			"ServiceUnavailable",
			"InternalError",
		},
	}
}

// RetryBackend 带重试机制的存储后端包装器
type RetryBackend struct {
	backend StorageBackend
	config  RetryConfig
}

// NewRetryBackend 创建带重试的后端
func NewRetryBackend(backend StorageBackend, config RetryConfig) *RetryBackend {
	return &RetryBackend{
		backend: backend,
		config:  config,
	}
}

// SaveFile 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) SaveFile(relPath string, data []byte) error {
	return r.retryOperation("save", func() error {
		return r.backend.SaveFile(relPath, data)
	})
}

// LoadFile 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) LoadFile(relPath string) ([]byte, error) {
	var result []byte
	err := r.retryOperation("load", func() error {
		data, err := r.backend.LoadFile(relPath)
		if err == nil {
			result = data
		}
		return err
	})
	return result, err
}

// FileExists 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) FileExists(relPath string) (bool, error) {
	var result bool
	err := r.retryOperation("exists", func() error {
		exists, err := r.backend.FileExists(relPath)
		if err == nil {
			result = exists
		}
		return err
	})
	return result, err
}

// StatFile 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) StatFile(relPath string) (*FileInfo, error) {
	var result *FileInfo
	err := r.retryOperation("stat", func() error {
		info, err := r.backend.StatFile(relPath)
		if err == nil {
			result = info
		}
		return err
	})
	return result, err
}

// OpenStream 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) OpenStream(relPath string) (io.ReadCloser, error) {
	var result io.ReadCloser
	err := r.retryOperation("open", func() error {
		stream, err := r.backend.OpenStream(relPath)
		if err == nil {
			result = stream
		}
		return err
	})
	return result, err
}

// DeleteFile 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) DeleteFile(relPath string) error {
	return r.retryOperation("delete", func() error {
		return r.backend.DeleteFile(relPath)
	})
}

// HealthCheck 实现 StorageBackend 接口（带重试）
func (r *RetryBackend) HealthCheck() error {
	return r.retryOperation("health_check", func() error {
		return r.backend.HealthCheck()
	})
}

// Info 实现 StorageBackend 接口（不重试）
func (r *RetryBackend) Info() BackendInfo {
	// Info 是幂等的只读操作，直接调用不重试
	info := r.backend.Info()
	// 标记这是带重试的后端
	if info.Metadata == nil {
		info.Metadata = make(map[string]string)
	}
	info.Metadata["retry_enabled"] = "true"
	info.Metadata["max_retries"] = fmt.Sprintf("%d", r.config.MaxRetries)
	return info
}

// retryOperation 执行重试逻辑
func (r *RetryBackend) retryOperation(op string, fn func() error) error {
	var lastErr error
	
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		// 执行操作
		err := fn()
		
		// 成功则返回
		if err == nil {
			if attempt > 0 {
				// 记录重试成功（用于监控）
				recordRetry(op, attempt, true)
			}
			return nil
		}
		
		lastErr = err
		
		// 检查是否可重试
		if !r.isRetryable(err) {
			return fmt.Errorf("non-retryable error: %w", err)
		}
		
		// 最后一次尝试失败，不再重试
		if attempt == r.config.MaxRetries {
			recordRetry(op, attempt, false)
			return fmt.Errorf("max retries exceeded (%d attempts): %w", attempt+1, lastErr)
		}
		
		// 计算退避时间
		backoff := r.calculateBackoff(attempt)
		
		// 记录重试尝试
		recordRetryAttempt(op, attempt, backoff, err)
		
		// 等待后重试
		time.Sleep(backoff)
	}
	
	return lastErr
}

// isRetryable 判断错误是否可重试
func (r *RetryBackend) isRetryable(err error) bool {
	if err == nil {
		return false
	}
	
	// 如果没有配置可重试错误列表，所有错误都重试
	if len(r.config.RetryableErrors) == 0 {
		return true
	}
	
	errStr := err.Error()
	for _, retryableErr := range r.config.RetryableErrors {
		if strings.Contains(strings.ToLower(errStr), strings.ToLower(retryableErr)) {
			return true
		}
	}
	
	return false
}

// calculateBackoff 计算退避时间（指数退避）
func (r *RetryBackend) calculateBackoff(attempt int) time.Duration {
	// 指数退避：backoff = initial * multiplier^attempt
	backoff := float64(r.config.InitialBackoff) * math.Pow(r.config.BackoffMultiplier, float64(attempt))
	
	// 限制最大退避时间
	if backoff > float64(r.config.MaxBackoff) {
		backoff = float64(r.config.MaxBackoff)
	}
	
	return time.Duration(backoff)
}

// recordRetryAttempt 记录重试尝试（用于监控）
func recordRetryAttempt(op string, attempt int, backoff time.Duration, err error) {
	// 这里可以集成到 metrics 系统
	// 暂时使用简单日志
	_ = op
	_ = attempt
	_ = backoff
	_ = err
}

// recordRetry 记录重试结果（用于监控）
func recordRetry(op string, attempts int, success bool) {
	// 集成到 metrics 系统
	// 可以记录：
	// - storage_retry_total{op, success}
	// - storage_retry_attempts{op}
	_ = op
	_ = attempts
	_ = success
}
