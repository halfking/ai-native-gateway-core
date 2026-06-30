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
	if c, ok := effectiveLogWriter.(syncCloser); ok {
		var firstErr error
		if err := c.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
			firstErr = err
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	return nil
}

// Writer returns the active file writer, or nil if file logging is
// disabled. Useful for tests that need to inspect rotated files.
func Writer() io.Writer {
	return effectiveLogWriter
}
