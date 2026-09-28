package bg

// hot_zone_trimmer.go — 双模式热区层方案（2026-09-24 H4）：data/hotzone/
// 三子树（cache / session_bodies / requests）的过期与配额清理 Worker。
//
// 与 cache_trimmer.go（仅清 L1.5 cache 目录）的区别：
//   - 遍历 HotZone.Dir 整树，三个子树共享同一配额与保留期；
//   - 先按 mtime 删除过期文件，再按总盘占超限时最旧先删（与现有 FileCache
//     「先移除旧的」语义一致）；
//   - 默认周期 30 分钟（Plan §3 H4 "每 30 分钟"），覆盖文件级保留与容量回收。
//
// 生命周期：与 cache_trimmer 同款，由调用方 `go trimmer.Start(ctx)` 启动，
// 内部不再起新协程；ctx 取消后优雅退出。
//
// 接线（2026-09-28 H4 P2）：cmd/gateway initStorageMode 在 lite 装配时构造并
// go Start，经 WithReload 注入 settings 直查闭包（GetPlatform* 无缓存，读即
// 最新已提交值），每轮 tick 前刷新 retention / maxBytes / enabled——变更在
// ≤1 个 trimmer 周期（30 分钟）内生效。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// defaultHotZoneTrimInterval 是热区清理的默认周期（Plan §3 H4：30 分钟）。
const defaultHotZoneTrimInterval = 30 * time.Minute

// HotZoneTrimmer 定期清理 data/hotzone/ 三子树（cache / session_bodies / requests）：
//  1. 按 retention 删除 mtime 早于 cutoff 的文件（过期清理）；
//  2. 总盘占超 maxBytes 时按 mtime 从旧到新淘汰（配额回收）。
//
// 三个子树共享同一配额；保留期独立可配。dirExists 缺失时静默返回（首次启动
// 可能还没建目录）。所有外部依赖仅为文件系统，不引入新接口。
//
// retention/maxBytes 用 atomic 存取：热重载入口（SetRetention/SetMaxBytes）
// 由 admin goroutine 调用，与 Start 所在 goroutine 的 TrimOnce 并发读写
// （2026-09-25 12h 审计轮：原实现为裸字段写，注释宣称的"原子替换"不成立，
// 接线 feature/wire-hotzone-trimmer 前修复）。interval/lastDeletedFiles/
// lastFreedBytes 仅构造期与 Start goroutine 内访问，保持普通字段。
type HotZoneTrimmer struct {
	dir       string        // 热区根目录（HotZone.Dir）
	retention atomic.Int64  // 保留时长（time.Duration，HotZone.RetentionHours 换算）
	maxBytes  atomic.Int64  // 三子树共享的字节上限（HotZone.MaxSizeGB << 30）
	interval  time.Duration // 清理周期，默认 30 分钟

	// reloadFn 是热重载钩子（WithReload 注入，接线层闭包直查 settings）。
	// Start 在每轮 TrimOnce 前调用一次；返回值语义：retention/maxBytes 非正
	// = 维持现值，enabled=false = 本轮跳过清理。仅 Start goroutine 调用，
	// 无并发访问。TrimOnce 直调（单测）不经过本钩子。
	reloadFn func() (retention time.Duration, maxBytes int64, enabled bool)

	lastDeletedFiles int
	lastFreedBytes   int64
}

// NewHotZoneTrimmer 构造热区清理 worker：清理周期固定 30 分钟（Plan §3 H4）。
func NewHotZoneTrimmer(dir string, retention time.Duration, maxBytes int64) *HotZoneTrimmer {
	t := &HotZoneTrimmer{
		dir:      dir,
		interval: defaultHotZoneTrimInterval,
	}
	t.retention.Store(int64(retention))
	t.maxBytes.Store(maxBytes)
	return t
}

// WithInterval 覆盖默认清理周期（主要供测试使用）。
func (t *HotZoneTrimmer) WithInterval(d time.Duration) *HotZoneTrimmer {
	if d > 0 {
		t.interval = d
	}
	return t
}

// WithReload 注入热重载钩子（H4 接线：接线层闭包直查 settings.GetPlatform*，
// 无缓存读即最新已提交值）。Start 在每轮 TrimOnce 前应用一次；retention/
// maxBytes 非正返回值维持现值，enabled=false 跳过本轮清理（目录内容不动，
// 下一轮 tick 再判定——运行期关开关即停清，不删已有文件）。
func (t *HotZoneTrimmer) WithReload(fn func() (retention time.Duration, maxBytes int64, enabled bool)) *HotZoneTrimmer {
	if fn != nil {
		t.reloadFn = fn
	}
	return t
}

// applyReload 执行一次热重载钩子并报告本轮是否应执行清理。nil 钩子恒 true。
func (t *HotZoneTrimmer) applyReload() bool {
	if t.reloadFn == nil {
		return true
	}
	retention, maxBytes, enabled := t.reloadFn()
	if retention > 0 {
		t.retention.Store(int64(retention))
	}
	if maxBytes > 0 {
		t.maxBytes.Store(maxBytes)
	}
	return enabled
}

// SetRetention 原子替换保留期（热重载入口，Plan §3 H4 「trimmer 的 retention 原子替换」）。
func (t *HotZoneTrimmer) SetRetention(d time.Duration) {
	if d > 0 {
		t.retention.Store(int64(d))
	}
}

// SetMaxBytes 原子替换配额上限。立即生效（按新上限做配额回收）。
func (t *HotZoneTrimmer) SetMaxBytes(b int64) {
	if b > 0 {
		t.maxBytes.Store(b)
	}
}

// Start 阻塞式运行清理循环：启动立即执行一次（排空历史积压的过期文件），
// 之后按 ticker 周期执行；每轮 TrimOnce 前先应用热重载钩子（WithReload，
// settings 直查 → 原子换 retention/maxBytes，enabled=false 跳过本轮）；
// ctx 取消时优雅退出。
func (t *HotZoneTrimmer) Start(ctx context.Context) {
	slog.Info("hotzone trimmer 已启动",
		"dir", t.dir,
		"retention", time.Duration(t.retention.Load()).String(),
		"max_bytes", t.maxBytes.Load(),
		"interval", t.interval.String())

	if t.applyReload() {
		if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("hotzone_trimmer: 首次清理失败", "error", err)
		}
	}

	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("hotzone_trimmer: 已停止")
			return
		case <-tk.C:
			if !t.applyReload() {
				slog.Debug("hotzone_trimmer: 热重载 enabled=false，本轮跳过")
				continue
			}
			if err := t.TrimOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("hotzone_trimmer: 清理失败", "error", err)
			}
		}
	}
}

// TrimOnce 执行一轮清理：
//  1. 遍历 dir 下所有文件，按 mtime 删除早于 cutoff 的过期条目；
//  2. 删完后若总盘占仍超 maxBytes，按 mtime 从旧到新继续删（配额回收）。
//
// 单条目删除失败只记日志不中断整体；目录不存在时静默返回。
func (t *HotZoneTrimmer) TrimOnce(ctx context.Context) error {
	if !dirExistsBG(t.dir) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// 一轮内快照一次，保证本轮的过期判定与配额口径自洽（中途热重载下一轮生效）。
	retention := time.Duration(t.retention.Load())
	maxBytes := t.maxBytes.Load()
	cutoff := time.Now().Add(-retention)
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var (
		files       []entry
		onDiskBytes int64
		deleted     int
		freed       int64
	)
	err := filepath.Walk(t.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("hotzone_trimmer: walk 跳过条目", "path", path, "error", err)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if info.IsDir() {
			return nil
		}
		// 临时文件不参与 trimmer（与 AsyncFileWriter 的 .tmp / .tmp- 写入约定一致）。
		if isTempFile(path) {
			return nil
		}
		files = append(files, entry{path: path, size: info.Size(), mod: info.ModTime()})
		onDiskBytes += info.Size()
		return nil
	})
	if err != nil {
		return err
	}

	// 阶段 1：过期清理（mtime < cutoff）。按 mtime 升序遍历保证最旧先删。
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	kept := files[:0]
	for _, e := range files {
		if e.mod.Before(cutoff) {
			if rmErr := os.Remove(e.path); rmErr != nil {
				slog.Warn("hotzone_trimmer: 删除过期文件失败", "path", e.path, "error", rmErr)
				kept = append(kept, e)
				continue
			}
			deleted++
			freed += e.size
			continue
		}
		kept = append(kept, e)
	}

	// 阶段 2：配额回收（按 mtime 最旧先删）。afterExpiry 是阶段 1 后的剩余文件，
	// 已按 mtime 升序排列，直接遍历即可。
	for _, e := range kept {
		if onDiskBytes <= maxBytes {
			break
		}
		if rmErr := os.Remove(e.path); rmErr != nil {
			slog.Warn("hotzone_trimmer: 配额回收删除失败", "path", e.path, "error", rmErr)
			continue
		}
		deleted++
		freed += e.size
		onDiskBytes -= e.size
	}

	t.lastDeletedFiles = deleted
	t.lastFreedBytes = freed
	if deleted > 0 {
		slog.Info("hotzone_trimmer: 清理完成",
			"deleted_files", deleted,
			"freed_bytes", freed,
			"freed_mb", float64(freed)/(1024*1024),
			"after_bytes", onDiskBytes,
			"max_bytes", maxBytes)
	}
	return nil
}

// isTempFile 判断文件是否属于异步写入器的临时文件（保留中，不参与 trimmer）。
// AsyncFileWriter 写入约定：`.{base}.tmp-{rand}` 与 `{base}.{rand}.tmp`；
// 任何含 ".tmp" 段的文件均视为写入中，不被 trimmer 删除。
func isTempFile(name string) bool {
	return strings.Contains(name, ".tmp")
}
