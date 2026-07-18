package logger

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	logger := New("test_module")
	assert.NotNil(t, logger)

	// 不会 panic
	logger.Debug("debug message", "key", "value")
	logger.Info("info message", "key", "value")
	logger.Warn("warn message", "key", "value")
	logger.Error("error message", "key", "value")
}

func TestNewWithComponent(t *testing.T) {
	logger := NewWithComponent("test_module", "test_component")
	assert.NotNil(t, logger)

	logger.Info("test message", "key", "value")
}

func TestWithContext(t *testing.T) {
	// Given: 带 trace_id 的上下文
	ctx := context.Background()
	ctx = context.WithValue(ctx, "trace_id", "trace_123")
	ctx = context.WithValue(ctx, "span_id", "span_456")
	ctx = context.WithValue(ctx, "request_id", "req_789")

	// When: 创建 logger
	logger := WithContext(ctx, "test_module")

	// Then: 不会 panic
	assert.NotNil(t, logger)
	logger.Info("test message with context", "key", "value")
}

func TestWith(t *testing.T) {
	logger := New("test_module")
	logger2 := logger.With("extra_key", "extra_value")

	assert.NotNil(t, logger2)
	logger2.Info("test message", "key", "value")
}

func TestEnabled(t *testing.T) {
	logger := New("test_module")

	// 默认级别应该启用 Info
	assert.True(t, logger.Enabled(slog.LevelInfo))
}

func TestSetLevel(t *testing.T) {
	// 设置为 Warn 级别
	SetLevel(slog.LevelWarn)

	logger := New("test_module")

	// Debug 和 Info 不应记录
	logger.Debug("debug message")
	logger.Info("info message")

	// Warn 和 Error 应记录
	logger.Warn("warn message")
	logger.Error("error message")

	// 恢复默认级别
	SetLevel(slog.LevelInfo)
}

func TestNoopLogger(t *testing.T) {
	logger := NewNoop()
	assert.NotNil(t, logger)

	// 不会 panic
	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")

	logger2 := logger.With("key", "value")
	assert.NotNil(t, logger2)
	logger2.Info("test")

	assert.False(t, logger.Enabled(slog.LevelInfo))
}

// BenchmarkLogger_Info 日志性能
func BenchmarkLogger_Info(b *testing.B) {
	logger := New("benchmark")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "key", "value", "count", i)
	}
}

// BenchmarkLogger_With 添加字段性能
func BenchmarkLogger_With(b *testing.B) {
	logger := New("benchmark")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger2 := logger.With("key", "value")
		logger2.Info("benchmark message")
	}
}

// BenchmarkWithContext 上下文 Logger 性能
func BenchmarkWithContext(b *testing.B) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, "trace_id", "trace_123")
	ctx = context.WithValue(ctx, "span_id", "span_456")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger := WithContext(ctx, "benchmark")
		logger.Info("benchmark message", "key", "value")
	}
}

// BenchmarkNoopLogger Noop Logger 性能
func BenchmarkNoopLogger(b *testing.B) {
	logger := NewNoop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "key", "value")
	}
}
