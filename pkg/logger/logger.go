package logger

import (
	"context"
	"log/slog"
	"os"
)

// Logger 统一日志接口
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
	Enabled(level slog.Level) bool
}

// SlogLogger slog 适配器
type SlogLogger struct {
	logger *slog.Logger
}

// New 创建模块级 Logger
func New(module string) Logger {
	return &SlogLogger{
		logger: slog.Default().With("module", module),
	}
}

// NewWithComponent 创建带组件名的 Logger
func NewWithComponent(module, component string) Logger {
	return &SlogLogger{
		logger: slog.Default().With(
			"module", module,
			"component", component,
		),
	}
}

// WithContext 从上下文提取 trace_id/span_id
func WithContext(ctx context.Context, module string) Logger {
	logger := slog.Default().With("module", module)

	if traceID := ctx.Value("trace_id"); traceID != nil {
		logger = logger.With("trace_id", traceID)
	}

	if spanID := ctx.Value("span_id"); spanID != nil {
		logger = logger.With("span_id", spanID)
	}

	if requestID := ctx.Value("request_id"); requestID != nil {
		logger = logger.With("request_id", requestID)
	}

	return &SlogLogger{logger: logger}
}

// Debug 调试日志
func (l *SlogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

// Info 信息日志
func (l *SlogLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

// Warn 警告日志
func (l *SlogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

// Error 错误日志
func (l *SlogLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

// With 添加字段
func (l *SlogLogger) With(args ...any) Logger {
	return &SlogLogger{
		logger: l.logger.With(args...),
	}
}

// Enabled 检查日志级别是否启用
func (l *SlogLogger) Enabled(level slog.Level) bool {
	return l.logger.Enabled(context.Background(), level)
}

// SetLevel 设置全局日志级别
func SetLevel(level slog.Level) {
	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))
}

// NoopLogger 空实现，用于测试
type NoopLogger struct{}

func NewNoop() Logger {
	return &NoopLogger{}
}

func (n *NoopLogger) Debug(msg string, args ...any) {}
func (n *NoopLogger) Info(msg string, args ...any)  {}
func (n *NoopLogger) Warn(msg string, args ...any)  {}
func (n *NoopLogger) Error(msg string, args ...any) {}
func (n *NoopLogger) With(args ...any) Logger       { return n }
func (n *NoopLogger) Enabled(level slog.Level) bool { return false }
