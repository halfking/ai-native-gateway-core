package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// withFields 复制调用方变参并追加固定字段，避免 append 原地改写 args 的底层数组。
func withFields(args []any, extra ...any) []any {
	out := make([]any, 0, len(args)+len(extra))
	out = append(out, args...)
	return append(out, extra...)
}

// PersistentLogger 持久化日志记录器，确保关键日志不会在重启后丢失
type PersistentLogger struct {
	mu             sync.RWMutex
	criticalLogger *slog.Logger
	errorLogger    *slog.Logger
	panicLogger    *slog.Logger
	shutdownLogger *slog.Logger
	auditLogger    *slog.Logger
	criticalWriter io.WriteCloser
	errorWriter    io.WriteCloser
	panicWriter    io.WriteCloser
	shutdownWriter io.WriteCloser
	auditWriter    io.WriteCloser
	logDir         string
	maxSize        int // MB
	maxBackups     int
	maxAge         int // days
	compress       bool
	instanceID     string
}

// PersistentLoggerConfig 持久化日志配置
type PersistentLoggerConfig struct {
	LogDir     string // 日志目录
	MaxSize    int    // 单个日志文件最大大小(MB)，默认100MB
	MaxBackups int    // 保留的旧日志文件数量，默认10
	MaxAge     int    // 保留日志文件的天数，默认30
	Compress   bool   // 是否压缩旧日志，默认true
	InstanceID string // 实例ID，用于区分不同实例的日志
}

var (
	globalPersistentLogger *PersistentLogger
	persistentLoggerOnce   sync.Once
	persistentInitErr      error
)

// InitPersistentLogger 初始化全局持久化日志记录器
//
// 失败语义：首次失败后 sync.Once 已消费，后续调用必须继续返回 (nil, 首次错误)。
// 若用局部 err 变量，第二次调用会错误地返回 (nil, nil)，调用方会按成功分支使用 nil 实例。
func InitPersistentLogger(config PersistentLoggerConfig) (*PersistentLogger, error) {
	persistentLoggerOnce.Do(func() {
		globalPersistentLogger, persistentInitErr = NewPersistentLogger(config)
	})
	return globalPersistentLogger, persistentInitErr
}

// GetPersistentLogger 获取全局持久化日志记录器
func GetPersistentLogger() *PersistentLogger {
	return globalPersistentLogger
}

// NewPersistentLogger 创建持久化日志记录器
func NewPersistentLogger(config PersistentLoggerConfig) (*PersistentLogger, error) {
	// 设置默认值
	if config.LogDir == "" {
		config.LogDir = "./logs"
	}
	if config.MaxSize == 0 {
		config.MaxSize = 100
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = 10
	}
	if config.MaxAge == 0 {
		config.MaxAge = 30
	}
	if config.InstanceID == "" {
		hostname, _ := os.Hostname()
		config.InstanceID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}

	// 创建日志目录
	if err := os.MkdirAll(config.LogDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	pl := &PersistentLogger{
		logDir:     config.LogDir,
		maxSize:    config.MaxSize,
		maxBackups: config.MaxBackups,
		maxAge:     config.MaxAge,
		compress:   config.Compress,
		instanceID: config.InstanceID,
	}

	// 为不同级别创建独立的日志文件
	pl.criticalWriter = pl.createWriter("critical.log")
	pl.errorWriter = pl.createWriter("error.log")
	pl.panicWriter = pl.createWriter("panic.log")
	pl.shutdownWriter = pl.createWriter("shutdown.log")
	pl.auditWriter = pl.createWriter("audit.log")

	baseGroup := slog.Group("instance",
		"instance_id", config.InstanceID,
		"pid", os.Getpid(),
	)

	pl.criticalLogger = slog.New(slog.NewJSONHandler(pl.criticalWriter, &slog.HandlerOptions{
		Level: slog.LevelError,
	})).With(baseGroup)

	pl.errorLogger = slog.New(slog.NewJSONHandler(pl.errorWriter, &slog.HandlerOptions{
		Level: slog.LevelError,
	})).With(baseGroup)

	pl.panicLogger = slog.New(slog.NewJSONHandler(pl.panicWriter, &slog.HandlerOptions{
		Level: slog.LevelError,
	})).With(baseGroup)

	pl.shutdownLogger = slog.New(slog.NewJSONHandler(pl.shutdownWriter, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With(baseGroup)

	pl.auditLogger = slog.New(slog.NewJSONHandler(pl.auditWriter, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With(baseGroup)

	// 记录启动事件
	pl.LogStartup()

	return pl, nil
}

// createWriter 创建日志写入器
func (pl *PersistentLogger) createWriter(filename string) io.WriteCloser {
	return &lumberjack.Logger{
		Filename:   filepath.Join(pl.logDir, filename),
		MaxSize:    pl.maxSize,
		MaxBackups: pl.maxBackups,
		MaxAge:     pl.maxAge,
		Compress:   pl.compress,
		LocalTime:  true,
	}
}

// LogCritical 记录严重错误（数据库连接失败、核心服务启动失败等）
func (pl *PersistentLogger) LogCritical(msg string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.criticalLogger != nil {
		pl.criticalLogger.Error(msg, withFields(args,
			"severity", "critical",
			"timestamp", time.Now().Format(time.RFC3339Nano))...)
	}
}

// LogError 记录一般错误
func (pl *PersistentLogger) LogError(msg string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.errorLogger != nil {
		pl.errorLogger.Error(msg, withFields(args,
			"timestamp", time.Now().Format(time.RFC3339Nano))...)
	}
}

// LogPanic 记录panic信息
func (pl *PersistentLogger) LogPanic(msg string, stackTrace string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.panicLogger != nil {
		pl.panicLogger.Error(msg, withFields(args,
			"stack_trace", stackTrace,
			"severity", "panic",
			"timestamp", time.Now().Format(time.RFC3339Nano))...)
	}
}

// LogShutdown 记录服务关闭事件
func (pl *PersistentLogger) LogShutdown(reason string, graceful bool, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		pl.shutdownLogger.Info("service_shutdown",
			withFields(args,
				"reason", reason,
				"graceful", graceful,
				"timestamp", time.Now().Format(time.RFC3339Nano))...)
	}
}

// LogStartup 记录服务启动事件
func (pl *PersistentLogger) LogStartup() {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		pl.shutdownLogger.Info("service_startup",
			"timestamp", time.Now().Format(time.RFC3339Nano),
			"go_version", runtime.Version(),
		)
	}
}

// LogAbnormalExit 记录异常退出（信号、panic等）
func (pl *PersistentLogger) LogAbnormalExit(exitType string, details string) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		pl.shutdownLogger.Error("abnormal_exit",
			"exit_type", exitType,
			"details", details,
			"timestamp", time.Now().Format(time.RFC3339Nano),
		)
	}
}

// LogAudit 记录审计日志（配置变更、权限操作等）
func (pl *PersistentLogger) LogAudit(action string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.auditLogger != nil {
		pl.auditLogger.Info(action, withFields(args,
			"timestamp", time.Now().Format(time.RFC3339Nano))...)
	}
}

// Close 关闭所有日志写入器
func (pl *PersistentLogger) Close() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	var errs []error

	if pl.criticalWriter != nil {
		if err := pl.criticalWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close critical writer: %w", err))
		}
	}
	if pl.errorWriter != nil {
		if err := pl.errorWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close error writer: %w", err))
		}
	}
	if pl.panicWriter != nil {
		if err := pl.panicWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close panic writer: %w", err))
		}
	}
	if pl.shutdownWriter != nil {
		if err := pl.shutdownWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close shutdown writer: %w", err))
		}
	}
	if pl.auditWriter != nil {
		if err := pl.auditWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close audit writer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing loggers: %v", errs)
	}
	return nil
}

// Sync 同步所有日志缓冲区
func (pl *PersistentLogger) Sync() error {
	pl.mu.RLock()
	defer pl.mu.RUnlock()

	// lumberjack.Logger 自动同步，这里不需要额外操作
	return nil
}
