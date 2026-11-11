package contextagent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────
// 可还原记录的**保留策略**（★ 2026-11-11 审计 A-9）
//
// ── 为什么需要它 ──────────────────────────────────────────────────────
//
// A-9 的原形态：★ FileRecorder 每请求写一个 JSON 文件（★ 内含**三份**
// ★ base64 正文，★ 单文件可达 MB 级）★★ **无轮转、无清理、无 TTL** ★★
// ★★ ⇒ ★★ 磁盘占用随请求量**单调增长** ⇒ ★★ 没有任何上界 ★★。
//
// ★ 这不是「优雅降级」，是**无界增长**：★ 长跑实例会把磁盘写满，
// ★★ 而磁盘写满之后**所有**写路径（含日志、DB 连接）都会失败 ★★。
//
// ── 策略的三个参数 ────────────────────────────────────────────────────
//
//	MaxFiles       最多保留多少个记录文件（★ 按 mtime 淘汰最旧的）
//	TTL            单个记录的最长保留时长（★ 超过即淘汰）
//	SweepInterval  两次清扫之间的最小间隔（★ **限频**：★ 不可每次 Save 都 O(n)）
//	SweepBatch     自上次清扫以来写满多少个文件就触发一次清扫
//
// ★ 为什么**同时**要 MaxFiles 与 TTL：★
//
//	MaxFiles  ⇒ ★★ 封住**空间**上界（★ 写多快都不会超过）★★
//	TTL       ⇒ ★★ 封住**时间**上界（★ 冷记录不会永久占位）★★
//
// ★ 两者**都**可以单独关掉（★ 0 ⇒ 不限）★ ⇒ ★★ 但**默认都不关**
// ★★（★ 默认即有界 ⇒ ★★ 「忘了配」不会退化成无界增长）★★。
//
// ★ 为什么清扫要**限频**：★ Save 在**请求路径**上 ★ ⇒ ★★ 每次 Save 都
// ★★ readdir + stat 全部文件 ⇒ ★★ O(n) × 每请求 ⇒ ★★ 高 QPS 下自己变成瓶颈。
// ⇒ ★★ 两个触发器：★ ① 时间间隔到了（★ 覆盖低流量 ⇒ ★ TTL 能及时生效）
// ★★ ② 自上次清扫以来写了足够多（★ 覆盖高流量 ⇒ ★ 空间上界不被冲破）★★
// ⇒ ★★ 清扫的**摊还成本**是 O(1) 级别（★ 每写 N 个文件才做一次 O(n)）★★。

// 保留策略的默认值。
//
// ★ DefaultFileRecorderMaxFiles 是**空间**上界：★ 256 个文件 × 单文件可达
// ★ MB 级 ⇒ ★ 最坏情形几百 MB 级 ⇒ ★ 与「可还原记录」这个调试型功能的
// ★ 定位相称（★ 它不是审计归档面）。
//
// ★ DefaultFileRecorderTTL 是**时间**上界：★ 72h（★ 3 天）★ ⇒ ★
// ★ 足以覆盖「昨天那次请求为什么变了」这类排查窗口。
const (
	// DefaultFileRecorderMaxFiles 是记录文件的默认数量上限（★ 0 = 不限）。
	DefaultFileRecorderMaxFiles = 256
	// DefaultFileRecorderTTL 是记录的默认最长保留时长（★ 0 = 不过期）。
	DefaultFileRecorderTTL = 72 * time.Hour
	// DefaultFileRecorderSweepInterval 是两次清扫之间的默认最小间隔。
	DefaultFileRecorderSweepInterval = 5 * time.Minute
	// DefaultFileRecorderSweepBatch 是「写满多少个就触发清扫」的默认批大小。
	DefaultFileRecorderSweepBatch = 32
)

// FileRetentionPolicy 是可还原记录的保留策略。
//
// ★ 零值（★ 全零）表示「**三项都不限**」★ —— ★★ 因此**构造器必须显式填
// ★★ 默认值**，★★ 不能靠零值兜底（★ 那是「忘了配 ⇒ 无界」的老毛病）。
type FileRetentionPolicy struct {
	// MaxFiles 是最多保留的记录文件数（★ 0 = 不限）。
	MaxFiles int
	// TTL 是单个记录的最长保留时长（★ 0 = 不过期）。
	TTL time.Duration
	// SweepInterval 是两次清扫之间的最小间隔（★ 0 = 每次 Save 都清扫，
	// ★ ★★ 只应在测试里用 ★★ —— ★ 生产会退化成 O(n) × 每请求）。
	SweepInterval time.Duration
	// SweepBatch 是自上次清扫以来写满多少个文件就触发一次清扫（★ 0 = 禁用）。
	SweepBatch int
}

// DefaultFileRetentionPolicy 返回生产默认的保留策略。
func DefaultFileRetentionPolicy() FileRetentionPolicy {
	return FileRetentionPolicy{
		MaxFiles:      DefaultFileRecorderMaxFiles,
		TTL:           DefaultFileRecorderTTL,
		SweepInterval: DefaultFileRecorderSweepInterval,
		SweepBatch:    DefaultFileRecorderSweepBatch,
	}
}

// sweepLocked 执行一次清扫：★ 淘汰过期的（★ TTL）与超量的（★ MaxFiles）。
//
// ★ keepPath 是**刚写入**的那个文件路径 ⇒ ★★ 它**绝不**被删
// ★★（★ 否则「刚存完就查不到」⇒ ★★ 那是最难查的一类故障）★★。
//
// ★ 错误处理：★ 清扫是**尽力而为**的 ⇒ ★ 单个文件删不掉不致命
// ★（★ 下一轮清扫会再试）★ ⇒ ★★ 清扫失败**不会**让 Save 失败
// ★★（★ 记录已经落盘了 ⇒ ★★ 记录功能本身是成功的）★★。
func (f *FileRecorder) sweepLocked(keepPath string, now time.Time) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	type recFile struct {
		path string
		mod  time.Time
	}
	var files []recFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// ★ 只认本 recorder 写的 ``<request_id>.json``；★ 跳过 ``.tmp``
		// （★ 临时文件由 Save 负责 rename/清理 ⇒ 不该留在这里）。
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, recFile{path: filepath.Join(f.dir, name), mod: info.ModTime()})
	}

	// ① TTL 淘汰。
	if f.policy.TTL > 0 {
		cutoff := now.Add(-f.policy.TTL)
		kept := files[:0]
		for _, it := range files {
			if it.path != keepPath && it.mod.Before(cutoff) {
				_ = os.Remove(it.path)
				continue
			}
			kept = append(kept, it)
		}
		files = kept
	}

	// ② 超量淘汰（★ 按 mtime 最旧优先 ★ ⇒保留的是最近写的那些）。
	if f.policy.MaxFiles > 0 && len(files) > f.policy.MaxFiles {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		remaining := len(files)
		for _, it := range files {
			if remaining <= f.policy.MaxFiles {
				break
			}
			if it.path == keepPath {
				continue
			}
			if err := os.Remove(it.path); err == nil {
				remaining--
			}
		}
	}
}

// maybeSweepLocked 判断是否该清扫，必要时执行。
//
// ★ 触发器两个（★ 见文件头）：★ ① 时间间隔到了 ② 写满一批。
func (f *FileRecorder) maybeSweepLocked(keepPath string, now time.Time) {
	f.pending++
	due := f.policy.SweepInterval > 0 && now.Sub(f.lastSweep) >= f.policy.SweepInterval
	batch := f.policy.SweepBatch > 0 && f.pending >= f.policy.SweepBatch
	if !due && !batch {
		return
	}
	f.sweepLocked(keepPath, now)
	f.lastSweep = now
	f.pending = 0
	f.sweeps++
}
