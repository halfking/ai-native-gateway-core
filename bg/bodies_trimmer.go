package bg

// bodies_trimmer.go — 双模式存储架构 Task 5.2：会话 bodies 保留策略 Worker。
//
// 会话 body 目录结构：{bodiesDir}/{tenantID}/{sid前2位}/{sessionID}/turn_N.json.gz。
// 本 worker 定期（默认 6 小时）三层遍历 租户/前缀/会话 目录，会话目录
// mtime 超过 retention 时整目录删除（先 dirSize 统计体积再 RemoveAll），
// 并统计删除会话数与释放字节数。清理后顺带删除变空的父目录（前缀/租户）。
//
// 与具体存储实现解耦：只操作目录，不 import storage 包。
//
// 生命周期：与 CacheTrimmer 一致，Start(ctx) 是阻塞式的（调用方
// `go trimmer.Start(ctx)` 启动），启动先立即执行一次，ctx 取消后优雅
// 退出并输出"已停止"。测试可用 WithInterval 缩短周期。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// defaultBodiesTrimInterval 是 bodies 保留策略清理的默认周期。
const defaultBodiesTrimInterval = 6 * time.Hour

// BodiesTrimmer 定期删除超过保留期的会话 body 目录。
type BodiesTrimmer struct {
	bodiesDir string        // bodies 根目录
	retention time.Duration // 会话目录保留时长，超过即整目录删除
	interval  time.Duration // 清理周期，默认 6 小时（测试可经 WithInterval 覆盖）

	// lastDeletedSessions / lastFreedBytes 是最近一次 TrimOnce 的统计
	// 快照，供测试与监控读取。
	lastDeletedSessions int
	lastFreedBytes      int64
}

// NewBodiesTrimmer 构造 bodies 保留策略 worker，清理周期固定 6 小时。
func NewBodiesTrimmer(bodiesDir string, retention time.Duration) *BodiesTrimmer {
	return &BodiesTrimmer{
		bodiesDir: bodiesDir,
		retention: retention,
		interval:  defaultBodiesTrimInterval,
	}
}

// WithInterval 覆盖默认清理周期（主要供测试使用），返回自身以便链式调用。
// 与 bg/apihub_watcher.go 的 AssetWatcher.WithInterval 同一惯例。
func (t *BodiesTrimmer) WithInterval(d time.Duration) *BodiesTrimmer {
	if d > 0 {
		t.interval = d
	}
	return t
}

// Start 阻塞式运行清理循环：与 CacheTrimmer 保持一致，启动先立即执行
// 一次（排空历史积压），之后按 ticker 周期执行；ctx 取消时优雅退出。
// 设计为由调用方 `go trimmer.Start(ctx)` 启动，内部不再另起新协程。
func (t *BodiesTrimmer) Start(ctx context.Context) {
	slog.Info("bodies trimmer 已启动",
		"dir", t.bodiesDir,
		"retention", t.retention.String(),
		"interval", t.interval.String())

	if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("bodies_trimmer: 首次清理失败", "error", err)
	}

	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("bodies_trimmer: 已停止")
			return
		case <-tk.C:
			if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("bodies_trimmer: 清理失败", "error", err)
			}
		}
	}
}

// TrimOnce 执行一轮清理（供测试与手动触发）：三层 os.ReadDir 遍历
// 租户/前缀/会话目录，会话目录 mtime 早于 cutoff 时先统计体积再整目录
// 删除；随后顺带删除变空的前缀/租户父目录（os.Remove 仅在目录为空时
// 成功，非空报错被忽略）。bodiesDir 不存在时静默返回；ctx 取消时中断。
func (t *BodiesTrimmer) TrimOnce(ctx context.Context) error {
	if !dirExistsBG(t.bodiesDir) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	cutoff := time.Now().Add(-t.retention)
	var (
		deletedSessions int
		freedBytes      int64
	)

	tenants, err := os.ReadDir(t.bodiesDir)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if !tenant.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		tenantPath := filepath.Join(t.bodiesDir, tenant.Name())

		prefixes, err := os.ReadDir(tenantPath)
		if err != nil {
			slog.Warn("bodies_trimmer: 读取租户目录失败", "path", tenantPath, "error", err)
			continue
		}
		for _, prefix := range prefixes {
			if !prefix.IsDir() {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			prefixPath := filepath.Join(tenantPath, prefix.Name())

			sessions, err := os.ReadDir(prefixPath)
			if err != nil {
				slog.Warn("bodies_trimmer: 读取前缀目录失败", "path", prefixPath, "error", err)
				continue
			}
			for _, sess := range sessions {
				if !sess.IsDir() {
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				sessPath := filepath.Join(prefixPath, sess.Name())
				info, err := sess.Info()
				if err != nil {
					slog.Warn("bodies_trimmer: 读取会话目录信息失败", "path", sessPath, "error", err)
					continue
				}
				if info.ModTime().Before(cutoff) {
					// 删除前先统计目录体积，用于释放字节数上报。
					size := dirSize(sessPath)
					if rmErr := os.RemoveAll(sessPath); rmErr != nil {
						slog.Warn("bodies_trimmer: 删除过期会话目录失败", "path", sessPath, "error", rmErr)
						continue
					}
					deletedSessions++
					freedBytes += size
				}
			}
			// 顺带删除变空的前缀目录（非空时 os.Remove 报错，忽略）。
			_ = os.Remove(prefixPath)
		}
		// 顺带删除变空的租户目录。
		_ = os.Remove(tenantPath)
	}

	t.lastDeletedSessions = deletedSessions
	t.lastFreedBytes = freedBytes
	if deletedSessions > 0 {
		slog.Info("bodies_trimmer: 清理完成",
			"deleted_sessions", deletedSessions,
			"freed_bytes", freedBytes,
			"freed_mb", float64(freedBytes)/(1024*1024))
	}
	return nil
}

// dirSize 递归统计目录内所有文件的总字节数（Walk 累加）。
// 包内此前无同名函数（已 grep 确认）；单个条目读取失败时跳过，尽力统计。
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
