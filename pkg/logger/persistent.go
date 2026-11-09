package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

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

	// HandlerFactory 构造写入各持久化日志文件的 handler。
	//
	// 留空则用裸 slog.JSONHandler（字段名 time、级别大写、无必填字段）。
	// ⚠ 为什么是**注入**而不是本包直接 import internal/logging：
	//   internal/logging → metrics → pkg/logger ⇒ 反向 import 会成环。
	//   所以规范层由调用方（cmd/gateway）用 logging.NewStandardHandler 构造好再传进来。
	//
	// 详见 rule 71 §7.5：事故复盘时读这些文件的正是人和 AI，
	// 形态必须与主日志一致。
	HandlerFactory func(w io.Writer, level slog.Level) slog.Handler
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

	// rule 71：持久化日志（critical/error/panic/shutdown/audit）同样必须合规。
	//
	// 此前这五个文件走**裸 JSONHandler**：字段名是 time（不是 timestamp）、
	// 时间是本地时区、且没有 service/version/env/instance_id/severity_*。
	//
	// instance 从 slog.Group 改成**平铺字段**：规范要求顶层单层，
	// 嵌套对象会让「找一条日志里的 instance_id」退化成遍历树。
	newHandler := config.HandlerFactory
	if newHandler == nil {
		newHandler = func(w io.Writer, level slog.Level) slog.Handler {
			return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
		}
	}

	// instance_id **按路径决定谁写**：
	//
	//	- 合规路径（注入了工厂）：规范层注入权威值（进程内恒定）。
	//	  这里再传一次会造成同一行 JSON 里出现重复键——
	//	  不同解析器取重复键的行为不一致（有的取最后一个、有的报错），
	//	  AI 侧解析结果会不确定。
	//	- 降级路径（未注入工厂，走裸 JSONHandler）：没有规范层，
	//	  instance_id 只能由这里提供，否则这条信息彻底丢失。
	var flatInstance []any
	if config.HandlerFactory == nil {
		flatInstance = []any{"instance_id", config.InstanceID, "pid", os.Getpid()}
	} else {
		flatInstance = []any{"pid", os.Getpid()}
	}

	pl.criticalLogger = slog.New(newHandler(pl.criticalWriter, slog.LevelError)).With(flatInstance...)
	pl.errorLogger = slog.New(newHandler(pl.errorWriter, slog.LevelError)).With(flatInstance...)
	pl.panicLogger = slog.New(newHandler(pl.panicWriter, slog.LevelError)).With(flatInstance...)
	pl.shutdownLogger = slog.New(newHandler(pl.shutdownWriter, slog.LevelInfo)).With(flatInstance...)
	pl.auditLogger = slog.New(newHandler(pl.auditWriter, slog.LevelInfo)).With(flatInstance...)

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
			"severity", "critical")...)
	}
}

// LogError 记录一般错误
func (pl *PersistentLogger) LogError(msg string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.errorLogger != nil {
		pl.errorLogger.Error(msg, withFields(args)...)
	}
}

// LogPanic 记录panic信息
func (pl *PersistentLogger) LogPanic(msg string, stackTrace string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.panicLogger != nil {
		pl.panicLogger.Error(msg, withFields(args,
			"stack_trace", stackTrace,
			"severity", "panic")...)
	}
}

// LogShutdown 记录服务关闭事件
func (pl *PersistentLogger) LogShutdown(reason string, graceful bool, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		// ⚠ 不传 timestamp：规范层已注入（UTC RFC3339Nano）。
		// 再传一次会造成同一行 JSON 里出现重复键（解析歧义）。
		pl.shutdownLogger.Info("service_shutdown",
			withFields(args,
				"reason", reason,
				"graceful", graceful)...)
	}
}

// LogStartup 记录服务启动事件
func (pl *PersistentLogger) LogStartup() {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		// ⚠ 不传 timestamp：规范层已注入（UTC RFC3339Nano）。
		// 再传一次会造成同一行 JSON 里出现重复键——
		// 不同解析器取重复键的行为不一致（有的取最后一个、有的报错），
		// AI 侧解析结果会不确定。
		pl.shutdownLogger.Info("service_startup",
			"go_version", runtime.Version(),
		)
	}
}

// LogAbnormalExit 记录异常退出（信号、panic等）
func (pl *PersistentLogger) LogAbnormalExit(exitType string, details string) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.shutdownLogger != nil {
		// ⚠ 不传 timestamp：规范层已注入（UTC RFC3339Nano）。
		pl.shutdownLogger.Error("abnormal_exit",
			"exit_type", exitType,
			"details", details,
		)
	}
}

// LogAudit 记录审计日志（配置变更、权限操作等）
func (pl *PersistentLogger) LogAudit(action string, args ...any) {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	if pl.auditLogger != nil {
		// ⚠ 不传 timestamp：规范层已注入（UTC RFC3339Nano）。
		pl.auditLogger.Info(action, withFields(args)...)
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
