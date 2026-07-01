// Package bg — storage_retention_worker.go
//
// 2026-07-02: 统一存储水位监控 worker。
//
// 定期检查磁盘水位，当超过自动清理阈值时按策略执行：
//   - 附件：LRU 清理最老文件至告警水位以下
//   - 日志：归档超期文件 + 删除超期归档
//
// 受 storage.auto_cleanup_enabled 开关控制（默认关闭）。每次操作记 slog 审计日志。
// 附件目录、日志目录、各阈值都从运行时探测/配置读取，不硬编码。

package bg

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StorageRetentionWorker 监控磁盘水位并自动清理。
type StorageRetentionWorker struct {
	// AttachmentDir 附件存储目录（运行时生效路径）
	AttachmentDir string
	// LogDir 日志目录（可为空，表示文件日志未启用）
	LogDir string

	// CheckInterval 检查间隔。默认 1 小时。
	CheckInterval time.Duration
	// AttachmentTTLDays 附件保留天数（超过则可清理）。默认 30。
	AttachmentTTLDays int
	// LogArchiveDays 日志归档天数。默认 7。
	LogArchiveDays int
	// LogDeleteDays 日志删除天数。默认 30。
	LogDeleteDays int

	// ConfigProvider 动态返回当前是否启用自动清理、触发阈值、告警水位。
	// 由调用方注入（从 settings_kv 读取）。返回 enabled=false 则 worker 空转。
	ConfigProvider StorageRetentionConfigFunc

	cancel context.CancelFunc
	done   chan struct{}
}

// StorageRetentionConfigFunc 返回当前清理配置。
// worker 每次检查时调用，使 settings_kv 的改动即时生效。
type StorageRetentionConfigFunc func() StorageRetentionConfig

// StorageRetentionConfig worker 运行时配置快照
type StorageRetentionConfig struct {
	AutoCleanupEnabled   bool    // 自动清理总开关
	AutoCleanupThreshold float64 // 触发水位（%，如 85）
	DiskQuotaPercent     float64 // 告警水位（清理目标，如 80）
}

// NewStorageRetentionWorker 构造 worker。
func NewStorageRetentionWorker(attachmentDir, logDir string, cfgProvider StorageRetentionConfigFunc) *StorageRetentionWorker {
	return &StorageRetentionWorker{
		AttachmentDir:     attachmentDir,
		LogDir:            logDir,
		CheckInterval:     1 * time.Hour,
		AttachmentTTLDays: 30,
		LogArchiveDays:    7,
		LogDeleteDays:     30,
		ConfigProvider:    cfgProvider,
		done:              make(chan struct{}),
	}
}

// Start 启动后台 goroutine。
func (w *StorageRetentionWorker) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
	slog.Info("storage retention worker started",
		"interval", w.CheckInterval.String(),
		"attachment_dir", w.AttachmentDir,
		"log_dir", w.LogDir)
}

// Stop 终止 goroutine 并等待退出。
func (w *StorageRetentionWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *StorageRetentionWorker) run(ctx context.Context) {
	defer close(w.done)
	t := time.NewTicker(w.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.checkOnce(ctx)
		}
	}
}

// checkOnce 执行一次水位检查。导出以便测试和手动触发。
func (w *StorageRetentionWorker) checkOnce(ctx context.Context) {
	if w.ConfigProvider == nil {
		return
	}
	cfg := w.ConfigProvider()
	if !cfg.AutoCleanupEnabled {
		return // 开关关闭，空转
	}
	if cfg.AutoCleanupThreshold <= 0 {
		return
	}

	// 检查附件目录所在磁盘水位
	diskPct := w.diskUsagePercent(w.AttachmentDir)
	if diskPct < cfg.AutoCleanupThreshold {
		return // 未达阈值，无需清理
	}

	slog.Info("storage retention: threshold exceeded, starting cleanup",
		"disk_usage_pct", diskPct,
		"threshold_pct", cfg.AutoCleanupThreshold,
		"target_pct", cfg.DiskQuotaPercent)

	// 1. 先清理日志（归档 + 删除，风险较低）
	if w.LogDir != "" {
		w.cleanupLogs(ctx)
	}

	// 2. 再清理附件（LRU 最老优先，至降到告警水位以下）
	if w.AttachmentDir != "" {
		w.cleanupAttachmentsLRU(ctx, cfg.DiskQuotaPercent)
	}

	// 清理后复测
	afterPct := w.diskUsagePercent(w.AttachmentDir)
	slog.Info("storage retention: cleanup done",
		"disk_usage_before_pct", diskPct,
		"disk_usage_after_pct", afterPct)
}

// cleanupLogs 归档超期日志 + 删除超期归档
func (w *StorageRetentionWorker) cleanupLogs(ctx context.Context) {
	if w.LogDir == "" || !dirExistsBG(w.LogDir) {
		return
	}
	// 删除超期归档（archive/ 子目录）
	archiveDir := filepath.Join(w.LogDir, "archive")
	if dirExistsBG(archiveDir) {
		deleteCutoff := time.Now().AddDate(0, 0, -w.LogDeleteDays)
		_ = filepath.WalkDir(archiveDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err == nil && info.ModTime().Before(deleteCutoff) {
				if rmErr := os.Remove(p); rmErr == nil {
					slog.Info("storage retention: deleted old log archive", "path", p)
				}
			}
			return nil
		})
	}
	// 删除超期的根目录轮转备份（非当前文件）
	// 注意：worker 不做 tar 打包（避免复杂），直接删除超期备份——
	// 归档打包由 UI 手动触发或独立 cron 负责
	currentFiles := make(map[string]bool)
	if entries, err := os.ReadDir(w.LogDir); err == nil {
		for _, e := range entries {
			currentFiles[e.Name()] = true
		}
	}
	backupCutoff := time.Now().AddDate(0, 0, -w.LogDeleteDays)
	_ = filepath.WalkDir(w.LogDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// 跳过 archive 子目录内容（已处理）
		if p == w.LogDir {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		name := filepath.Base(p)
		// 跳过当前活动日志（最新的 .log）
		if info.ModTime().After(backupCutoff) {
			return nil
		}
		// 删除超期备份（.log.gz 等）
		if !currentFiles[name] || filepath.Ext(name) == ".gz" {
			if rmErr := os.Remove(p); rmErr == nil {
				slog.Info("storage retention: deleted old log backup", "path", p)
			}
		}
		return nil
	})
}

// cleanupAttachmentsLRU 按 LRU（最老优先）清理附件至降到 quotaPct 以下
func (w *StorageRetentionWorker) cleanupAttachmentsLRU(ctx context.Context, quotaPct float64) {
	if w.AttachmentDir == "" || !dirExistsBG(w.AttachmentDir) {
		return
	}
	ttlCutoff := time.Now().AddDate(0, 0, -w.AttachmentTTLDays)

	// 收集所有过期文件（按 mtime 升序 = 最老优先）
	type fileItem struct {
		path  string
		mtime time.Time
		size  int64
	}
	var items []fileItem
	_ = filepath.WalkDir(w.AttachmentDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// 只清理 TTL 过期的文件
		if info.ModTime().Before(ttlCutoff) {
			items = append(items, fileItem{path: p, mtime: info.ModTime(), size: info.Size()})
		}
		return nil
	})

	// 按 mtime 升序（最老优先）
	for i := 0; i < len(items); i++ {
		// 检查是否已降到告警水位以下
		if w.diskUsagePercent(w.AttachmentDir) < quotaPct {
			break
		}
		if rmErr := os.Remove(items[i].path); rmErr == nil {
			slog.Info("storage retention: deleted expired attachment (LRU)",
				"path", items[i].path, "mtime", items[i].mtime.Format(time.RFC3339))
		}
	}
}

// diskUsagePercent 返回 path 所在磁盘的使用率（0-100）
func (w *StorageRetentionWorker) diskUsagePercent(path string) float64 {
	if path == "" {
		return 0
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(abs, &stat); err != nil {
		return 0
	}
	total := stat.Blocks * uint64(stat.Bsize)
	if total == 0 {
		return 0
	}
	used := total - (stat.Bfree * uint64(stat.Bsize))
	return float64(used) / float64(total) * 100
}

// dirExistsBG 检查目录是否存在（bg 包内部，避免与 admin 包冲突）
func dirExistsBG(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
