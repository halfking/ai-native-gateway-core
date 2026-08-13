package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestIsRetryableDBError 测试错误重试判断逻辑
func TestIsRetryableDBError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "ErrNoRows should not retry",
			err:      pgx.ErrNoRows,
			expected: false,
		},
		{
			name:     "context deadline exceeded should retry",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "connection refused should retry",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "timeout should retry",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "connection reset should retry",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "broken pipe should retry",
			err:      errors.New("broken pipe"),
			expected: true,
		},
		{
			name:     "SQL syntax error should not retry",
			err:      errors.New("syntax error at or near"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableDBError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableDBError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestStaleCacheFailsafe 测试过期缓存降级逻辑
func TestStaleCacheFailsafe(t *testing.T) {
	// 这个测试需要实际的数据库连接和Redis，暂时跳过
	// 在集成测试中验证完整的失败安全机制
	t.Skip("Integration test - requires database and Redis")
}

// BenchmarkIsRetryableDBError 性能基准测试
func BenchmarkIsRetryableDBError(b *testing.B) {
	err := errors.New("connection refused")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isRetryableDBError(err)
	}
}
