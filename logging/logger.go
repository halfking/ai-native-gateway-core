package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Level 日志级别
type Level = zapcore.Level

const (
	DebugLevel = zapcore.DebugLevel
	InfoLevel  = zapcore.InfoLevel
	WarnLevel  = zapcore.WarnLevel
	ErrorLevel = zapcore.ErrorLevel
	FatalLevel = zapcore.FatalLevel
)

// Config 日志配置
type Config struct {
	Level       Level
	Format      string // json | console
	Output      io.Writer
	Development bool
	ServiceName string
	Environment string
}

// DefaultConfig 默认配置
func DefaultConfig() Config {
	return Config{
		Level:       InfoLevel,
		Format:      "json",
		Output:      os.Stdout,
		Development: false,
		ServiceName: "llm-gateway",
		Environment: "development",
	}
}

// Logger 日志器
type Logger struct {
	zap    *zap.Logger
	sugar  *zap.SugaredLogger
	config Config
}

// NewLogger 创建日志器
func NewLogger(config Config) (*Logger, error) {
	// 编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 选择编码器
	var encoder zapcore.Encoder
	if config.Format == "console" {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	}

	// 创建 core
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(config.Output),
		config.Level,
	)

	// 创建 logger
	var zapLogger *zap.Logger
	if config.Development {
		zapLogger = zap.New(core, zap.Development(), zap.AddCaller(), zap.AddStacktrace(ErrorLevel))
	} else {
		zapLogger = zap.New(core, zap.AddCaller())
	}

	// 添加全局字段
	zapLogger = zapLogger.With(
		zap.String("service", config.ServiceName),
		zap.String("environment", config.Environment),
	)

	return &Logger{
		zap:    zapLogger,
		sugar:  zapLogger.Sugar(),
		config: config,
	}, nil
}

// Debug 调试日志
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	l.zap.Debug(msg, fields...)
}

// Info 信息日志
func (l *Logger) Info(msg string, fields ...zap.Field) {
	l.zap.Info(msg, fields...)
}

// Warn 警告日志
func (l *Logger) Warn(msg string, fields ...zap.Field) {
	l.zap.Warn(msg, fields...)
}

// Error 错误日志
func (l *Logger) Error(msg string, fields ...zap.Field) {
	l.zap.Error(msg, fields...)
}

// Fatal 致命错误日志
func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	l.zap.Fatal(msg, fields...)
}

// With 添加字段
func (l *Logger) With(fields ...zap.Field) *Logger {
	return &Logger{
		zap:    l.zap.With(fields...),
		sugar:  l.zap.With(fields...).Sugar(),
		config: l.config,
	}
}

// WithContext 从 context 添加追踪信息
func (l *Logger) WithContext(ctx context.Context) *Logger {
	// 提取 trace ID 和 span ID（如果有）
	fields := []zap.Field{}

	// 这里可以从 ctx 提取追踪信息
	if traceID := ctx.Value("trace_id"); traceID != nil {
		fields = append(fields, zap.String("trace_id", traceID.(string)))
	}
	if spanID := ctx.Value("span_id"); spanID != nil {
		fields = append(fields, zap.String("span_id", spanID.(string)))
	}

	if len(fields) > 0 {
		return l.With(fields...)
	}

	return l
}

// Sync 同步日志
func (l *Logger) Sync() error {
	return l.zap.Sync()
}

// Sugar 返回 SugaredLogger
func (l *Logger) Sugar() *zap.SugaredLogger {
	return l.sugar
}

// Zap 返回原始 zap.Logger
func (l *Logger) Zap() *zap.Logger {
	return l.zap
}

// Fields 辅助函数

// String 字符串字段
func String(key, val string) zap.Field {
	return zap.String(key, val)
}

// Int 整数字段
func Int(key string, val int) zap.Field {
	return zap.Int(key, val)
}

// Int64 64位整数字段
func Int64(key string, val int64) zap.Field {
	return zap.Int64(key, val)
}

// Float64 浮点数字段
func Float64(key string, val float64) zap.Field {
	return zap.Float64(key, val)
}

// Bool 布尔字段
func Bool(key string, val bool) zap.Field {
	return zap.Bool(key, val)
}

// Duration 持续时间字段
func Duration(key string, val time.Duration) zap.Field {
	return zap.Duration(key, val)
}

// Err 错误字段
func Err(err error) zap.Field {
	return zap.Error(err)
}

// Any 任意类型字段
func Any(key string, val interface{}) zap.Field {
	return zap.Any(key, val)
}

// LogRequest 记录请求日志
func (l *Logger) LogRequest(ctx context.Context, method, path string, statusCode int, duration time.Duration, err error) {
	fields := []zap.Field{
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status_code", statusCode),
		zap.Duration("duration", duration),
	}

	if err != nil {
		fields = append(fields, zap.Error(err))
	}

	logger := l.WithContext(ctx)

	if statusCode >= 500 {
		logger.Error("request failed", fields...)
	} else if statusCode >= 400 {
		logger.Warn("request error", fields...)
	} else {
		logger.Info("request completed", fields...)
	}
}

// LogAPICall 记录 API 调用日志
func (l *Logger) LogAPICall(ctx context.Context, provider, model string, duration time.Duration, inputTokens, outputTokens int, err error) {
	fields := []zap.Field{
		zap.String("provider", provider),
		zap.String("model", model),
		zap.Duration("duration", duration),
		zap.Int("input_tokens", inputTokens),
		zap.Int("output_tokens", outputTokens),
	}

	if err != nil {
		fields = append(fields, zap.Error(err))
	}

	logger := l.WithContext(ctx)

	if err != nil {
		logger.Error("api call failed", fields...)
	} else {
		logger.Info("api call completed", fields...)
	}
}

// StructuredLog 结构化日志辅助
type StructuredLog struct {
	logger *Logger
	fields []zap.Field
}

// NewStructuredLog 创建结构化日志
func (l *Logger) NewStructuredLog() *StructuredLog {
	return &StructuredLog{
		logger: l,
		fields: make([]zap.Field, 0),
	}
}

// AddField 添加字段
func (s *StructuredLog) AddField(field zap.Field) *StructuredLog {
	s.fields = append(s.fields, field)
	return s
}

// AddString 添加字符串字段
func (s *StructuredLog) AddString(key, val string) *StructuredLog {
	s.fields = append(s.fields, zap.String(key, val))
	return s
}

// AddInt 添加整数字段
func (s *StructuredLog) AddInt(key string, val int) *StructuredLog {
	s.fields = append(s.fields, zap.Int(key, val))
	return s
}

// AddError 添加错误字段
func (s *StructuredLog) AddError(err error) *StructuredLog {
	s.fields = append(s.fields, zap.Error(err))
	return s
}

// Info 记录 Info 日志
func (s *StructuredLog) Info(msg string) {
	s.logger.Info(msg, s.fields...)
}

// Warn 记录 Warn 日志
func (s *StructuredLog) Warn(msg string) {
	s.logger.Warn(msg, s.fields...)
}

// Error 记录 Error 日志
func (s *StructuredLog) Error(msg string) {
	s.logger.Error(msg, s.fields...)
}

// GlobalLogger 全局日志器
var GlobalLogger *Logger

// InitGlobalLogger 初始化全局日志器
func InitGlobalLogger(config Config) error {
	logger, err := NewLogger(config)
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	GlobalLogger = logger
	return nil
}

// Debug 全局 Debug
func Debug(msg string, fields ...zap.Field) {
	if GlobalLogger != nil {
		GlobalLogger.Debug(msg, fields...)
	}
}

// Info 全局 Info
func Info(msg string, fields ...zap.Field) {
	if GlobalLogger != nil {
		GlobalLogger.Info(msg, fields...)
	}
}

// Warn 全局 Warn
func Warn(msg string, fields ...zap.Field) {
	if GlobalLogger != nil {
		GlobalLogger.Warn(msg, fields...)
	}
}

// LogError 全局 Error (重命名避免冲突)
func LogError(msg string, fields ...zap.Field) {
	if GlobalLogger != nil {
		GlobalLogger.Error(msg, fields...)
	}
}
