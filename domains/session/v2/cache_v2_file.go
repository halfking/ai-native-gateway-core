// Package v2: FileCache 是 L1.5 本地文件缓存层
//
// 职责：
//  1. 位于内存 L1 (CompressionMetaCache) 与数据库 L3 (SessionTurnsReader) 之间，
//     用本地磁盘文件缓存 SessionStateV2，进程重启后仍可命中
//  2. 路径布局：{baseDir}/{tenantID}/{sessionID前2位}/{sessionID}.json
//     （sessionID 不足 2 位时用全量作为分片目录名）
//  3. mtime 超过 TTL 视为过期（读时顺带清理）；写入超过 maxSize 时按 mtime
//     从旧到新淘汰（最旧先删，绝不淘汰即将写入的 key 自己）
//  4. 写入走「临时文件 + rename」，读侧要么看到旧的完整文件、要么看到新的
//     完整文件，不会读到半截 JSON；读到损坏 JSON 按缓存未命中处理并清理
//
// 勘察结论（2026-09-05, Task 2.3）：
//   - SessionStateV2（cache_v2.go）自带 TenantID / SessionID 字段，因此 Set
//     直接采用 Set(state *SessionStateV2) error 签名，无需 (tenantID, sessionID,
//     state) 的适配签名，键直接取自 state。
//   - 包内没有可复用的「未找到」哨兵错误（L3 用 pgx.ErrNoRows，L2/L3 把 miss
//     当 (nil, nil) 处理），这里定义私有 errCacheMiss，不污染包级公共 API；
//     调用方通过 errors.Is(err, errCacheMiss) 判断未命中。
//
// 锁策略（与文档伪代码的差异说明）：
//
//	文档伪代码在 ensureSpace 内部 fc.mu.Lock()，Set 随后再次 Lock——两次加锁
//	之间存在竞态窗口：并发的 Set/Delete/过期清理会让 sizeUsed 记账漂移，甚至
//	短暂超过 maxSize。本实现改为：Set 全程持锁，淘汰逻辑拆为 ensureSpaceLocked
//	（要求调用方已持锁），从构造上杜绝重入死锁，并使 sizeUsed 记账与文件操作
//	严格一一对应（写成功才记账、删成功才扣减）。
package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// errCacheMiss 表示文件缓存未命中（不存在 / 已过期 / 内容损坏）。
// 私有哨兵错误：仅在本文件内定义使用，避免污染包级公共 API。
var errCacheMiss = errors.New("file cache: miss")

const (
	fileCacheDirPerm  os.FileMode = 0o755
	fileCacheFilePerm os.FileMode = 0o644

	// defaultFileCacheTTL 为 ttl <= 0 时的兜底值，与包内 defaultGovernanceTTL 同量级
	defaultFileCacheTTL = 30 * time.Minute
)

// FileCache 是 L1.5 本地文件缓存：
//   - 线程安全（sync.Mutex 保护 sizeUsed 记账与所有文件变更）
//   - Get 未命中返回包装 errCacheMiss 的错误；Set/Delete 幂等或容错（fail-open，
//     缓存层失败不阻断主链路）
type FileCache struct {
	baseDir  string
	ttl      time.Duration
	maxSize  int64
	sizeUsed int64 // 当前已占用字节数（仅统计 *.json，由 mu 保护）

	mu sync.Mutex
}

// NewFileCache 创建本地文件缓存：创建 baseDir（0755）并 filepath.Walk 统计
// 现有占用，使进程重启后 sizeUsed 与磁盘现状对齐。
func NewFileCache(baseDir string, ttl time.Duration, maxSize int64) (*FileCache, error) {
	if baseDir == "" {
		return nil, errors.New("file cache: baseDir is empty")
	}
	if maxSize <= 0 {
		return nil, fmt.Errorf("file cache: maxSize must be positive, got %d", maxSize)
	}
	if ttl <= 0 {
		ttl = defaultFileCacheTTL
	}
	if err := os.MkdirAll(baseDir, fileCacheDirPerm); err != nil {
		return nil, fmt.Errorf("file cache: mkdir %s: %w", baseDir, err)
	}
	fc := &FileCache{baseDir: baseDir, ttl: ttl, maxSize: maxSize}
	// 启动时统计现有 *.json 占用（遗留的临时文件不计入，也不清理，交由运维处理）
	if err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录被并发删除等场景，容忍
			}
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".json") {
			fc.sizeUsed += info.Size()
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("file cache: walk %s: %w", baseDir, err)
	}
	return fc, nil
}

// buildPath 生成缓存文件路径：
// {baseDir}/{tenantID}/{sessionID前2位}/{sessionID}.json
// sessionID 不足 2 位时用全量作为分片目录名。
func (fc *FileCache) buildPath(tenantID, sessionID string) string {
	shard := sessionID
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(fc.baseDir, tenantID, shard, sessionID+".json")
}

// validCacheID 校验 ID 可以安全地作为路径段（非空、不含路径分隔符、不是相对
// 路径特殊项），防止外部 ID 把文件写到 baseDir 之外。
func validCacheID(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, `/\`)
}

// expired 判断 mtime 是否已超过 TTL。
func (fc *FileCache) expired(modTime time.Time) bool {
	return fc.ttl > 0 && time.Since(modTime) >= fc.ttl
}

// Get 读取会话状态。
//
// 未命中（不存在 / 已过期 / JSON 损坏）一律返回包装 errCacheMiss 的错误；
// 已过期的文件会被顺带删除并扣减 sizeUsed。
// 任意 os.Stat 失败（含不存在）按缓存未命中处理：缓存层 fail-open，调用方
// 回源 L3 即可。
func (fc *FileCache) Get(tenantID, sessionID string) (*SessionStateV2, error) {
	if fc == nil || !validCacheID(tenantID) || !validCacheID(sessionID) {
		return nil, fmt.Errorf("file cache: get %s/%s: %w", tenantID, sessionID, errCacheMiss)
	}
	path := fc.buildPath(tenantID, sessionID)

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("file cache: get %s: %w", path, errCacheMiss)
	}
	if fc.expired(fi.ModTime()) {
		fc.removeExpired(path)
		return nil, fmt.Errorf("file cache: get %s: expired: %w", path, errCacheMiss)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// 读窗口内被并发 Delete/过期清理删除 → 视为未命中
		return nil, fmt.Errorf("file cache: read %s: %w", path, errCacheMiss)
	}
	var state SessionStateV2
	if err := json.Unmarshal(data, &state); err != nil {
		// 读到损坏 JSON（如异常断电留下的半截文件）：按缓存未命中处理并清理
		fc.removeIfUnchanged(path, fi)
		return nil, fmt.Errorf("file cache: unmarshal %s: %w", path, errCacheMiss)
	}
	return &state, nil
}

// Set 写入会话状态（覆盖写）。
//
// 流程：序列化 → 记下旧文件大小 → ensureSpaceLocked 腾空间 → MkdirAll →
// 临时文件 + rename（0644）→ 按差额记账。写失败不记账、删成功才扣减。
func (fc *FileCache) Set(state *SessionStateV2) error {
	if fc == nil || state == nil {
		// 与包内 CompressionMetaCache.Set / SessionCacheV2.Set 的 nil 容错约定一致
		return nil
	}
	if !validCacheID(state.TenantID) || !validCacheID(state.SessionID) {
		return fmt.Errorf("file cache: set: invalid tenant/session id %q/%q",
			state.TenantID, state.SessionID)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("file cache: marshal: %w", err)
	}
	path := fc.buildPath(state.TenantID, state.SessionID)

	// 全程持锁（含文件 IO）：保证 sizeUsed 记账与文件状态严格一致，
	// 也保证 ensureSpaceLocked 不会重入加锁（见文件头「锁策略」）。
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// 覆盖写时先记下旧文件大小，写入成功后按差额记账
	oldSize := int64(0)
	if fi, err := os.Stat(path); err == nil {
		oldSize = fi.Size()
	}

	// 腾空间；excludePath = 即将写入的目标文件，绝不被淘汰
	fc.ensureSpaceLocked(int64(len(data)), path)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, fileCacheDirPerm); err != nil {
		return fmt.Errorf("file cache: mkdir %s: %w", dir, err)
	}
	// 临时文件 + rename：rename 在同一文件系统内是原子的，读侧不会读到半截 JSON
	tmp, err := os.CreateTemp(dir, "."+state.SessionID+".tmp-")
	if err != nil {
		return fmt.Errorf("file cache: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("file cache: write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("file cache: close %s: %w", tmpPath, err)
	}
	// CreateTemp 固定 0600，规格要求 0644
	if err := os.Chmod(tmpPath, fileCacheFilePerm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("file cache: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("file cache: rename %s -> %s: %w", tmpPath, path, err)
	}

	// 写成功才记账：先减被覆盖的旧文件，再加新文件
	fc.sizeUsed += int64(len(data)) - oldSize
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
	return nil
}

// Delete 删除会话缓存文件。
// 文件不存在时返回 nil（幂等）；删除成功才扣减 sizeUsed。
func (fc *FileCache) Delete(tenantID, sessionID string) error {
	if fc == nil || !validCacheID(tenantID) || !validCacheID(sessionID) {
		return nil
	}
	path := fc.buildPath(tenantID, sessionID)

	fc.mu.Lock()
	defer fc.mu.Unlock()

	fi, err := os.Stat(path)
	if err != nil {
		return nil // 不存在 → 幂等成功
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil // 并发下已被删除，同样视为幂等成功
		}
		return fmt.Errorf("file cache: remove %s: %w", path, err)
	}
	fc.sizeUsed -= fi.Size()
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
	return nil
}

// ensureSpaceLocked 确保再写入 needed 字节后 sizeUsed 不超过 maxSize：
// 空间足够直接返回；否则按 mtime 从旧到新删除文件直到空间足够。
// excludePath 是即将写入的目标文件，绝不会被淘汰。
//
// 要求调用方已持有 fc.mu（对应文件头「锁策略」：不在内部重复加锁，避免重入
// 死锁，同时保证记账与淘汰的原子性）。
// 尽力而为：若淘汰完全部候选仍腾不出空间（例如单条数据超过 maxSize），放行
// 写入，缓存层不做拒绝服务（与包内 fail-open 风格一致）。
func (fc *FileCache) ensureSpaceLocked(needed int64, excludePath string) {
	if fc.sizeUsed+needed <= fc.maxSize {
		return
	}
	type candidate struct {
		path string
		size int64
		mod  time.Time
	}
	var items []candidate
	var onDisk int64
	_ = filepath.Walk(fc.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil // 单个条目遍历失败不影响整体淘汰
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") || path == excludePath {
			return nil
		}
		items = append(items, candidate{path: path, size: info.Size(), mod: info.ModTime()})
		onDisk += info.Size()
		return nil
	})
	// 记账自愈：外部清理者（bg.CacheTrimmer 按 mtime 删文件）不经过本结构，
	// sizeUsed 会单调虚高；溢出时本就全树遍历了一次，顺手以磁盘实况重置记账，
	// 否则虚高的 sizeUsed 会让每次 Set 都触发全树 Walk 并误删仍活跃的新文件。
	// excludePath 的体积不在此列（是即将被本次写入覆盖的旧值，落盘后按新值累加）。
	fc.sizeUsed = onDisk
	// mtime 从旧到新排序（最旧先删）
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })

	for _, it := range items {
		if fc.sizeUsed+needed <= fc.maxSize {
			break
		}
		if err := os.Remove(it.path); err == nil {
			// 删成功才扣减
			fc.sizeUsed -= it.size
			if fc.sizeUsed < 0 {
				fc.sizeUsed = 0
			}
		}
	}
}

// removeExpired 在锁内复检 mtime 后删除过期文件并扣减 sizeUsed。
// 锁内复检是为了避免误删并发 Set 刚刚刷新过的同名文件。
func (fc *FileCache) removeExpired(path string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fi, err := os.Stat(path)
	if err != nil || !fc.expired(fi.ModTime()) {
		return // 已被并发删除，或已被并发 Set 刷新
	}
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("file cache: remove expired failed", "path", path, "error", err)
			return // 删除失败不扣减记账
		}
		return
	}
	fc.sizeUsed -= fi.Size()
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
}

// removeIfUnchanged 在锁内复检文件自读取后未被替换（mtime/size 一致），
// 再删除损坏文件并扣减 sizeUsed，避免误删并发 Set 刚写入的新文件。
func (fc *FileCache) removeIfUnchanged(path string, seen os.FileInfo) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fi, err := os.Stat(path)
	if err != nil || !fi.ModTime().Equal(seen.ModTime()) || fi.Size() != seen.Size() {
		return // 文件已被并发替换/删除，不动它
	}
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("file cache: remove corrupt failed", "path", path, "error", err)
			return
		}
		return
	}
	fc.sizeUsed -= fi.Size()
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
}

// Stats 返回缓存占用统计：size_used_bytes / max_size_bytes / usage_percent。
func (fc *FileCache) Stats() map[string]interface{} {
	if fc == nil {
		return map[string]interface{}{
			"size_used_bytes": int64(0),
			"max_size_bytes":  int64(0),
			"usage_percent":   0.0,
		}
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()

	percent := 0.0
	if fc.maxSize > 0 {
		percent = float64(fc.sizeUsed) / float64(fc.maxSize) * 100
	}
	return map[string]interface{}{
		"size_used_bytes": fc.sizeUsed,
		"max_size_bytes":  fc.maxSize,
		"usage_percent":   percent,
	}
}

// Close 释放缓存。当前实现没有后台协程与外部资源，幂等返回 nil。
func (fc *FileCache) Close() error { return nil }
