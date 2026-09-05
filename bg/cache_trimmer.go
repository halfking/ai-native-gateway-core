package bg

// cache_trimmer.go — 双模式存储架构 Task 5.1：L1.5 文件缓存过期清理 Worker。
//
// L1.5 文件缓存目录结构：{cacheDir}/{tenantID}/{sid前2位}/{sessionID}.json。
// 本 worker 定期（默认 1 小时）遍历 cacheDir，按文件 mtime 删除超过
// retention 的缓存文件，并统计删除文件数与释放字节数。
//
// 与具体存储实现解耦：只操作目录，不 import storage 包。
//
// 生命周期：与包内 audit_trimmer 等不同，本 worker 的 Start(ctx) 是阻塞式
// 的（设计为由调用方 `go trimmer.Start(ctx)` 启动，不内部再起新协程），
// ctx 取消后优雅退出并输出"已停止"。测试可用 WithInterval 缩短周期。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// defaultCacheTrimInterval 是 L1.5 缓存清理的默认周期。
const defaultCacheTrimInterval = 1 * time.Hour

// CacheTrimmer 定期清理过期的 L1.5 文件缓存。
type CacheTrimmer struct {
	cacheDir  string        // L1.5 缓存根目录
	retention time.Duration // 缓存文件保留时长，超过即删除
	interval  time.Duration // 清理周期，默认 1 小时（测试可经 WithInterval 覆盖）

	// lastDeletedFiles / lastFreedBytes 是最近一次 TrimOnce 的统计快照，
	// 供测试与监控读取。
	lastDeletedFiles int
	lastFreedBytes   int64
}

// NewCacheTrimmer 构造 L1.5 缓存清理 worker，清理周期固定 1 小时。
func NewCacheTrimmer(cacheDir string, retention time.Duration) *CacheTrimmer {
	return &CacheTrimmer{
		cacheDir:  cacheDir,
		retention: retention,
		interval:  defaultCacheTrimInterval,
	}
}

// WithInterval 覆盖默认清理周期（主要供测试使用），返回自身以便链式调用。
// 与 bg/apihub_watcher.go 的 AssetWatcher.WithInterval 同一惯例。
func (t *CacheTrimmer) WithInterval(d time.Duration) *CacheTrimmer {
	if d > 0 {
		t.interval = d
	}
	return t
}

// Start 阻塞式运行清理循环：启动即执行一次（与包内 trimmer 惯例一致，
// 避免新部署要等一个完整周期才首次清理），之后按 ticker 周期执行；
// ctx 取消时优雅退出。设计为由调用方 `go trimmer.Start(ctx)` 启动，
// 内部不再另起新协程。
func (t *CacheTrimmer) Start(ctx context.Context) {
	slog.Info("cache trimmer 已启动",
		"dir", t.cacheDir,
		"retention", t.retention.String(),
		"interval", t.interval.String())

	// 启动立即执行一次，排空历史积压的过期缓存。
	if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("cache_trimmer: 首次清理失败", "error", err)
	}

	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("cache_trimmer: 已停止")
			return
		case <-tk.C:
			if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("cache_trimmer: 清理失败", "error", err)
			}
		}
	}
}

// TrimOnce 执行一轮清理（供测试与手动触发）：遍历 cacheDir，删除
// mtime 早于 cutoff 的文件。单条目删除失败只记日志不中断整体；
// 目录不存在时静默返回（首次启动可能还没建目录）。
func (t *CacheTrimmer) TrimOnce(ctx context.Context) error {
	if !dirExistsBG(t.cacheDir) {
		return nil
	}
	// Walk 前检查 ctx 取消。
	if err := ctx.Err(); err != nil {
		return err
	}

	cutoff := time.Now().Add(-t.retention)
	var (
		deletedFiles int
		freedBytes   int64
	)
	err := filepath.Walk(t.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 单个条目读取失败不中断整体清理，记日志后跳过。
			slog.Warn("cache_trimmer: walk 跳过条目", "path", path, "error", err)
			return nil
		}
		// 遍历过程中响应 ctx 取消，尽快中断。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if rmErr := os.Remove(path); rmErr != nil {
				slog.Warn("cache_trimmer: 删除过期缓存文件失败", "path", path, "error", rmErr)
				return nil
			}
			deletedFiles++
			freedBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}

	t.lastDeletedFiles = deletedFiles
	t.lastFreedBytes = freedBytes
	if deletedFiles > 0 {
		slog.Info("cache_trimmer: 清理完成",
			"deleted_files", deletedFiles,
			"freed_bytes", freedBytes,
			"freed_mb", float64(freedBytes)/(1024*1024))
	}
	return nil
}
