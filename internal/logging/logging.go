// Package logging provides the gateway's file-based slog rotation
// pipeline. The rotation policy defaults match the operator spec
// (100MB per file × 10 backups = ~1GB ceiling, gzip on rotation,
// auto-delete after MaxAge days) and every knob is overridable via
// the LLM_GATEWAY_LOG_* env vars documented in config/config.go.
//
// Why lumberjack (gopkg.in/natefinch/lumberjack.v2)
// ────────────────────────────────────────────────
// Lumberjack is the de facto Go log-rotation library (Docker, K8s,
// etcd, Vault all depend on it). It implements exactly the four
// behaviours the operator spec requires:
//
//  1. Size-based rotation   (MaxSize, in MB)
//  2. Backup count cap      (MaxBackups; 0 = unlimited)
//  3. Age-based expiry      (MaxAge, in days; 0 = no expiry)
//  4. Transparent gzip      (Compress; runs in a background goroutine)
//
// Hand-rolling rotation is strictly worse: rotation races, atomic
// rename on Windows, gzip on close, and stat-based age pruning
// are all subtle. Lumberjack has been in production for >9 years.
//
// Layout on disk
// ──────────────
// With defaults, a gateway.log file plus rotated backups appear as:
//
//	gateway.log                                 # current
//	gateway-2026-07-01T02-15-04.000.log.gz     # rotated + gzipped
//	gateway-2026-06-30T18-09-11.000.log.gz
//	...
//	gateway-2026-06-25T03-22-44.000.log.gz     # at most 10; oldest
//	                                           # is deleted on rotate
//
// MaxAge=0 keeps the rotated files forever (capped by MaxBackups).
// MaxAge=7 deletes any backup whose mtime is older than 7 days,
// regardless of MaxBackups. The two knobs compose: with
// MaxBackups=10 + MaxAge=7, a busy gateway that rotates every
// 5 minutes will keep 1 day of history; a quiet one keeps 7 days.
//
// Failure modes
// ─────────────
//   - Disk full or permission denied on rotate → lumberjack retries
//     on the next write; we also mirror to stderr so a misconfigured
//     LLM_GATEWAY_LOG_FILE never silently drops logs.
//   - LLM_GATEWAY_LOG_FILE empty → Init() is a no-op and slog
//     continues to write to stderr (default behaviour).
//
// Thread-safety
// ─────────────
// lumberjack.Logger.Write is safe for concurrent use, so a single
// instance is shared between every goroutine via the package-level
// io.Writer returned by Writer().
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Config holds the user-overridable knobs for the rotation pipeline.
// All fields have sensible defaults (see DefaultConfig); zero values
// are NOT treated as "use the default" — pass the result of
// DefaultConfig() or apply the result of ApplyEnv() if you want
// environment variable overrides.
type Config struct {
	// File is the absolute or relative path of the active log file.
	// The directory is created on Init if it does not exist.
	// An empty File disables file logging (slog keeps writing to
	// stderr only).
	File string

	// MaxSizeMB is the size threshold in MB at which the current
	// log file is rotated. Must be > 0; defaults to 100.
	MaxSizeMB int

	// MaxBackups is the maximum number of rotated backup files to
	// retain. Must be ≥ 0; 0 means "keep all backups" (only
	// bounded by MaxAge). Defaults to 10.
	MaxBackups int

	// MaxAgeDays is the maximum age in days of a rotated backup.
	// 0 means "never expire by age" (only bounded by MaxBackups).
	// Defaults to 7.
	MaxAgeDays int

	// Compress enables gzip on rotated backups. Defaults to true
	// (matches the operator spec "过期的 log 需要压缩").
	Compress bool

	// LocalTime controls whether rotated file names use the local
	// time zone (true) or UTC (false). Defaults to true so the
	// filenames match wall-clock operator expectations.
	LocalTime bool
}

// DefaultConfig returns the operator-spec defaults:
//   - 100 MB per file × 10 backups ≈ 1 GB ceiling
//   - 7-day rolling retention with gzip on rotation
//
// The File field is left empty; callers (typically cmd/gateway) set
// it from LLM_GATEWAY_LOG_FILE.
func DefaultConfig() Config {
	return Config{
		File:       "",
		MaxSizeMB:  100,
		MaxBackups: 10,
		MaxAgeDays: 7,
		Compress:   true,
		LocalTime:  true,
	}
}

// Validate returns an error if cfg has an invalid knob. Call this
// before passing cfg to Init so misconfigurations are surfaced
// at startup rather than on the first rotate.
func (c Config) Validate() error {
	if c.File == "" {
		return nil
	}
	if c.MaxSizeMB <= 0 {
		return fmt.Errorf("logging: MaxSizeMB must be > 0 (got %d)", c.MaxSizeMB)
	}
	if c.MaxBackups < 0 {
		return fmt.Errorf("logging: MaxBackups must be ≥ 0 (got %d)", c.MaxBackups)
	}
	if c.MaxAgeDays < 0 {
		return fmt.Errorf("logging: MaxAgeDays must be ≥ 0 (got %d)", c.MaxAgeDays)
	}
	return nil
}

// effectiveLogWriter is the package-level io.Writer used by slog.
// It is nil until Init() succeeds with a non-empty File.
var effectiveLogWriter io.Writer

// activeLogger 持有当前 lumberjack 实例引用，用于运行时热加载（Reconfigure）。
// nil 表示文件日志未启用。
var (
	activeLogger *lumberjack.Logger
	activeConfig Config
	loggerMu     sync.RWMutex
)

// Init installs the file-based rotation writer as the destination
// of the default slog handler. If cfg.File is empty, Init is a
// no-op and slog keeps its existing handler (typically stderr).
//
// On success, slog.Default() writes JSON records to the rotated
// log file; the returned io.Writer is the underlying file writer
// (useful for tests and for callers that want to write raw bytes).
//
// On failure (e.g. permission denied on the log directory), Init
// returns an error AND leaves slog on its current handler so the
// service can still start; the caller should log the failure
// prominently.
func Init(cfg Config, level slog.Level) (io.Writer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.File == "" {
		return nil, nil
	}

	if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
		return nil, fmt.Errorf("logging: create log dir: %w", err)
	}

	lj := &lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
		LocalTime:  cfg.LocalTime,
	}

	// Mirror to stderr so a misconfigured LLM_GATEWAY_LOG_FILE never
	// silently drops logs. Operators can `tail -F` either stream
	// interchangeably; JSON records are identical line-for-line.
	mw := io.MultiWriter(lj, os.Stderr)
	effectiveLogWriter = lj
	loggerMu.Lock()
	activeLogger = lj
	activeConfig = cfg
	loggerMu.Unlock()

	handler := slog.NewJSONHandler(mw, &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))

	slog.Info("logging: file rotation enabled",
		"file", cfg.File,
		"max_size_mb", cfg.MaxSizeMB,
		"max_backups", cfg.MaxBackups,
		"max_age_days", cfg.MaxAgeDays,
		"compress", cfg.Compress,
	)
	return lj, nil
}

// Shutdown flushes any buffered records and closes the rotated log
// file. Safe to call multiple times. After Shutdown, the next write
// via slog reopens the file (lumberjack transparently reopens on
// Write), so callers do NOT need to re-Init.
func Shutdown() error {
	if effectiveLogWriter == nil {
		return nil
	}
	type syncCloser interface {
		Sync() error
		Close() error
	}
	var firstErr error
	if c, ok := effectiveLogWriter.(syncCloser); ok {
		if err := c.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
			firstErr = err
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	loggerMu.Lock()
	activeLogger = nil
	activeConfig = Config{}
	effectiveLogWriter = nil
	loggerMu.Unlock()
	return firstErr
}

// Writer returns the active file writer, or nil if file logging is
// disabled. Useful for tests that need to inspect rotated files.
func Writer() io.Writer {
	return effectiveLogWriter
}

// ActiveConfig 返回当前生效的日志配置（线程安全）。
// 文件日志未启用时返回的 Config.File 为空。
func ActiveConfig() Config {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return activeConfig
}

// Reconfigure 在运行时修改 lumberjack 的轮转参数（MaxSize/MaxBackups/
// MaxAge/Compress），立即生效，无需重启服务进程。
//
// 注意：
//   - 仅当文件日志已启用（activeLogger != nil）时生效
//   - 修改的是同一个 lumberjack.Logger 实例的字段，下一次 Write/Rotate
//     即应用新参数
//   - File（日志文件路径）变更不在热加载范围——改路径需要重建 writer，
//     避免竞态，调用方应通过重启生效
//   - 返回 nil 表示成功；err 非 nil 时配置未改动
func Reconfigure(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	loggerMu.Lock()
	defer loggerMu.Unlock()
	if activeLogger == nil {
		return errors.New("logging: file logging is not enabled, cannot reconfigure")
	}
	// 只热加载轮转参数，不改文件路径（避免并发写竞态）
	activeLogger.MaxSize = cfg.MaxSizeMB
	activeLogger.MaxBackups = cfg.MaxBackups
	activeLogger.MaxAge = cfg.MaxAgeDays
	activeLogger.Compress = cfg.Compress
	// 保留原 File 和 LocalTime
	activeConfig.MaxSizeMB = cfg.MaxSizeMB
	activeConfig.MaxBackups = cfg.MaxBackups
	activeConfig.MaxAgeDays = cfg.MaxAgeDays
	activeConfig.Compress = cfg.Compress
	slog.Info("logging: reconfigured (hot reload)",
		"max_size_mb", cfg.MaxSizeMB,
		"max_backups", cfg.MaxBackups,
		"max_age_days", cfg.MaxAgeDays,
		"compress", cfg.Compress)
	return nil
}

// LogFileInfo 描述单个日志文件（当前文件或轮转备份）。
type LogFileInfo struct {
	Name         string    `json:"name"`          // 文件名（不含目录）
	SizeBytes    int64     `json:"size_bytes"`    // 字节数
	ModTime      time.Time `json:"mod_time"`      // 最后修改时间
	IsCurrent    bool      `json:"is_current"`    // 是否为当前活动日志
	IsCompressed bool      `json:"is_compressed"` // 是否已 gzip 压缩（.gz）
	IsArchived   bool      `json:"is_archived"`   // 是否在 archive/ 子目录
}

// ListFiles 列出当前日志目录下的所有日志文件（含轮转备份和归档）。
// 文件日志未启用时返回空切片和 nil 错误。
// 结果按 ModTime 倒序（最新在前）。
func ListFiles() ([]LogFileInfo, error) {
	loggerMu.RLock()
	cfg := activeConfig
	loggerMu.RUnlock()
	if cfg.File == "" {
		return nil, nil
	}
	dir := filepath.Dir(cfg.File)
	currentName := filepath.Base(cfg.File)
	return scanLogDir(dir, currentName)
}

// scanLogDir 扫描日志目录，汇总文件信息。导出以便测试。
func scanLogDir(dir, currentName string) ([]LogFileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []LogFileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		// 只收录 .log 和 .log.gz 文件，忽略无关文件
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") &&
			!strings.HasSuffix(name, ".gz") {
			continue
		}
		out = append(out, LogFileInfo{
			Name:         name,
			SizeBytes:    info.Size(),
			ModTime:      info.ModTime(),
			IsCurrent:    name == currentName,
			IsCompressed: strings.HasSuffix(name, ".gz"),
			IsArchived:   false,
		})
	}
	// 倒序：最新在前
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}
