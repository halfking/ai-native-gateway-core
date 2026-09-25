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
// 索引（2026-09-24 H1，全模式热区层方案）：
//   - 内存中维护 map[string]*indexEntry，key=tenantID+"/"+sessionID
//   - 与 sizeUsed 同受 fc.mu 保护；启动期 Walk 阶段顺带填充
//   - Get 命中索引可跳过 Stat（mtime/size 以索引为准，读后仍校验内容可解析）
//   - 索引与磁盘漂移时按 miss 处理并摘除索引（fail-open 风格）
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

// indexEntry 是 FileCache 内存索引的单条目。key=tenantID+"/"+sessionID
// （cacheIndexKey 构造）；path 是相对 baseDir 的缓存文件路径；modTime
// 是最近一次观测到的 mtime（启动期 Walk 填一次，写入时更新）；size
// 与 sizeUsed 记账共享，删除时同步扣减。
type indexEntry struct {
	path    string
	size    int64
	modTime time.Time
}

// FileCache 是 L1.5 本地文件缓存：
//   - 线程安全（sync.Mutex 保护 sizeUsed 记账、index 与所有文件变更）
//   - Get 未命中返回包装 errCacheMiss 的错误；Set/Delete 幂等或容错（fail-open，
//     缓存层失败不阻断主链路）
//   - 内存索引（index）作为磁盘实况的加速视图；任何漂移按 miss 兜底
type FileCache struct {
	baseDir  string
	ttl      time.Duration
	maxSize  int64
	sizeUsed int64 // 当前已占用字节数（仅统计 *.json，由 mu 保护）

	// index 是磁盘实况的内存索引视图（H1 接线，2026-09-24）。Get 命中索引
	// 可减少一次 os.Stat；任何「索引有 / 磁盘无」按 miss 处理并摘除索引。
	index map[string]*indexEntry

	mu sync.Mutex
}

// NewFileCache 创建本地文件缓存：创建 baseDir（0755）并 filepath.Walk 统计
// 现有占用，使进程重启后 sizeUsed 与磁盘现状对齐；Walk 阶段同步填充内存索引。
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
	fc := &FileCache{
		baseDir: baseDir,
		ttl:     ttl,
		maxSize: maxSize,
		index:   make(map[string]*indexEntry),
	}
	// 启动时统计现有 *.json 占用 + 填充索引（遗留的临时文件不计入，也不清理，交由运维处理）
	if err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录被并发删除等场景，容忍
			}
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".json") {
			fc.sizeUsed += info.Size()
			// 路径里隐含 tenantID 与 sessionID：{baseDir}/{tenant}/{shard}/{session}.json
			// rel 形如 {tenant}/{shard}/{session}.json，逆解析首段为 tenant，
			// 文件名去后缀为 sessionID；shard 仅作分片，不参与 key。
			if rel, relErr := filepath.Rel(baseDir, path); relErr == nil {
				parts := strings.SplitN(rel, string(filepath.Separator), 2)
				if len(parts) == 2 {
					tenant := parts[0]
					session := strings.TrimSuffix(filepath.Base(parts[1]), ".json")
					if tenant != "" && session != "" {
						fc.index[cacheIndexKey(tenant, session)] = &indexEntry{
							path:    path,
							size:    info.Size(),
							modTime: info.ModTime(),
						}
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("file cache: walk %s: %w", baseDir, err)
	}
	return fc, nil
}

// ResizeMax 修改 maxSize 上限。仅立即生效扩容；缩容交由下一轮 trimmer
// 执行（按 mtime 最旧先删），避免强制删除仍活跃的会话文件。
// 要求调用方持有 fc.mu。零值或负值拒绝（与构造时校验一致）。
func (fc *FileCache) ResizeMax(newMaxSize int64) {
	if newMaxSize <= 0 {
		return
	}
	fc.maxSize = newMaxSize
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

// cacheIndexKey 构造内存索引 key：tenantID+"/"+sessionID。
// 用 "/" 作分隔符以避免 tenant 与 session 拼接时的碰撞（与 cache_v2.go
// 的 cacheKey 风格对齐，但保留独立命名以免误用）。
func cacheIndexKey(tenantID, sessionID string) string {
	return tenantID + "/" + sessionID
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
//
// 索引优先（H1）：命中索引时可跳过 Stat（仍校验内容可解析）；索引与磁盘
// 漂移时（"索引说有、磁盘说无"）按 miss 处理并摘除索引。
func (fc *FileCache) Get(tenantID, sessionID string) (*SessionStateV2, error) {
	if fc == nil || !validCacheID(tenantID) || !validCacheID(sessionID) {
		return nil, fmt.Errorf("file cache: get %s/%s: %w", tenantID, sessionID, errCacheMiss)
	}
	path := fc.buildPath(tenantID, sessionID)
	key := cacheIndexKey(tenantID, sessionID)

	fc.mu.Lock()
	entry, indexed := fc.index[key]
	if indexed && fc.expired(entry.modTime) {
		// 索引条目已过期：复检磁盘后删除（避免误删并发 Set 刚刷新的同名文件）
		fc.removeExpiredLocked(path)
		delete(fc.index, key)
		fc.mu.Unlock()
		return nil, fmt.Errorf("file cache: get %s: expired: %w", path, errCacheMiss)
	}
	fc.mu.Unlock()

	if !indexed {
		// 索引 miss：Stat 磁盘一次；存在则回填索引。
		fi, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("file cache: get %s: %w", path, errCacheMiss)
		}
		if fc.expired(fi.ModTime()) {
			fc.removeExpired(path)
			return nil, fmt.Errorf("file cache: get %s: expired: %w", path, errCacheMiss)
		}
		fc.mu.Lock()
		fc.index[key] = &indexEntry{path: path, size: fi.Size(), modTime: fi.ModTime()}
		fc.mu.Unlock()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// 读窗口内被并发 Delete/过期清理删除 → 视为未命中（同时摘除残留索引）
		fc.mu.Lock()
		delete(fc.index, key)
		fc.mu.Unlock()
		return nil, fmt.Errorf("file cache: read %s: %w", path, errCacheMiss)
	}
	var state SessionStateV2
	if err := json.Unmarshal(data, &state); err != nil {
		// 读到损坏 JSON（如异常断电留下的半截文件）：按缓存未命中处理并清理。
		// 索引条目可能与磁盘现状漂移（文件已被外部修改），必须 Stat 一次取磁盘
		// 实况再走 removeIfUnchanged，否则 fakeFileInfo 与磁盘 modTime 不一致
		// 会让"复检 mtime/size 一致"误判，导致损坏文件残留。
		fc.mu.Lock()
		seen, statErr := os.Stat(path)
		fc.mu.Unlock()
		if statErr != nil {
			fc.mu.Lock()
			delete(fc.index, key)
			fc.mu.Unlock()
		} else {
			fc.removeIfUnchanged(path, seen)
			fc.mu.Lock()
			delete(fc.index, key)
			fc.mu.Unlock()
		}
		return nil, fmt.Errorf("file cache: unmarshal %s: %w", path, errCacheMiss)
	}
	return &state, nil
}

// fakeFileInfo 在索引命中但读路径需要 removeIfUnchanged 时构造的最小
// os.FileInfo 视图（仅有 path/size/modTime 三字段参与比较，name/dir/isDir
// 等调用方不消费）。
type fileInfoShim struct {
	os.FileInfo
	size    int64
	modTime time.Time
	path    string
}

func (f *fileInfoShim) Name() string       { return filepath.Base(f.path) }
func (f *fileInfoShim) Size() int64        { return f.size }
func (f *fileInfoShim) ModTime() time.Time { return f.modTime }
func (f *fileInfoShim) IsDir() bool        { return false }
func (f *fileInfoShim) Mode() os.FileMode  { return fileCacheFilePerm }
func (f *fileInfoShim) Sys() interface{}   { return nil }

func fakeFileInfo(path string, size int64, modTime time.Time) os.FileInfo {
	return &fileInfoShim{path: path, size: size, modTime: modTime}
}

// Set 写入会话状态（覆盖写）。
//
// 流程：序列化 → 记下旧文件大小 → ensureSpaceLocked 腾空间 → MkdirAll →
// 临时文件 + rename（0644）→ 按差额记账 → 登记索引。写失败不记账、删成功才扣减。
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
	key := cacheIndexKey(state.TenantID, state.SessionID)

	// 全程持锁（含文件 IO）：保证 sizeUsed 记账与文件状态严格一致，
	// 也保证 ensureSpaceLocked 不会重入加锁（见文件头「锁策略」）。
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// 覆盖写时先记下旧文件大小，写入成功后按差额记账
	oldSize := int64(0)
	if fi, err := os.Stat(path); err == nil {
		oldSize = fi.Size()
	}

	// 腾空间；excludePath = 即将写入的目标文件，绝不被淘汰。
	// healed = 本次触发了记账自愈（sizeUsed 已重置为不含 excludePath 的
	// 磁盘实况），调用方须按「新增文件」口径累加而非按差额。
	healed := fc.ensureSpaceLocked(int64(len(data)), path)

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

	// 写成功才记账：先减被覆盖的旧文件，再加新文件。
	// ensureSpaceLocked 返回 healed：自愈路径已把 sizeUsed 重置为「不含
	// excludePath」的磁盘实况，此时再减 oldSize 会把从未计入的体积重复扣减
	//（2026-09-05 审计 B3 负漂移），直接按新值累加即可。
	if healed {
		fc.sizeUsed += int64(len(data))
	} else {
		fc.sizeUsed += int64(len(data)) - oldSize
	}
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
	// 登记索引（H1）：覆盖写时只更新，不删旧条目后再插入；
	// 保证在锁内的"sizeUsed 扣减→重增"与"索引 path/size/modTime 替换"
	// 对外是不可分割的操作。
	fc.index[key] = &indexEntry{
		path:    path,
		size:    int64(len(data)),
		modTime: time.Now(),
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
	key := cacheIndexKey(tenantID, sessionID)

	fc.mu.Lock()
	defer fc.mu.Unlock()

	fi, err := os.Stat(path)
	if err != nil {
		// 不存在 → 幂等成功（同时摘除可能存在的残留索引）
		delete(fc.index, key)
		return nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			delete(fc.index, key)
			return nil // 并发下已被删除，同样视为幂等成功
		}
		return fmt.Errorf("file cache: remove %s: %w", path, err)
	}
	fc.sizeUsed -= fi.Size()
	if fc.sizeUsed < 0 {
		fc.sizeUsed = 0
	}
	delete(fc.index, key)
	return nil
}

// ensureSpaceLocked 确保再写入 needed 字节后 sizeUsed 不超过 maxSize：
// 空间足够直接返回；否则按 mtime 从旧到新删除文件直到空间足够。
// excludePath 是即将写入的目标文件，绝不会被淘汰。
//
// 返回 healed：是否执行了记账自愈（sizeUsed 以磁盘实况重置，且不含
// excludePath —— 它是即将被本次写入覆盖的旧值，落盘后由调用方按新值累加）。
//
// 要求调用方已持有 fc.mu（对应文件头「锁策略」：不在内部重复加锁，避免重入
// 死锁，同时保证记账与淘汰的原子性）。
// 尽力而为：若淘汰完全部候选仍腾不出空间（例如单条数据超过 maxSize），放行
// 写入，缓存层不做拒绝服务（与包内 fail-open 风格一致）。
func (fc *FileCache) ensureSpaceLocked(needed int64, excludePath string) (healed bool) {
	if fc.sizeUsed+needed <= fc.maxSize {
		return
	}
	type candidate struct {
		path string
		size int64
		mod  time.Time
		key  string
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
	// excludePath 的体积不在此列（是即将被本次写入覆盖的旧值，落盘后由
	// 调用方按新值累加）。
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
			// 同步重建索引：路径 → {tenant,session} 逆向解析
			if rel, relErr := filepath.Rel(fc.baseDir, it.path); relErr == nil {
				parts := strings.SplitN(rel, string(filepath.Separator), 2)
				if len(parts) == 2 {
					tenant := parts[0]
					session := strings.TrimSuffix(filepath.Base(parts[1]), ".json")
					delete(fc.index, cacheIndexKey(tenant, session))
				}
			}
		}
	}
	return true
}

// removeExpired 在锁内复检 mtime 后删除过期文件并扣减 sizeUsed。
// 锁内复检是为了避免误删并发 Set 刚刚刷新过的同名文件。
// （同时摘除索引条目，保持索引与磁盘一致。）
func (fc *FileCache) removeExpired(path string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.removeExpiredLocked(path)
}

// removeExpiredLocked 复用 removeExpired 的核心逻辑但要求调用方已持锁。
// Get 在检测到索引过期时会先持锁调用本函数，避免重复加锁。
func (fc *FileCache) removeExpiredLocked(path string) {
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
	if rel, relErr := filepath.Rel(fc.baseDir, path); relErr == nil {
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) == 2 {
			delete(fc.index, cacheIndexKey(parts[0], strings.TrimSuffix(filepath.Base(parts[1]), ".json")))
		}
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
	if rel, relErr := filepath.Rel(fc.baseDir, path); relErr == nil {
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) == 2 {
			delete(fc.index, cacheIndexKey(parts[0], strings.TrimSuffix(filepath.Base(parts[1]), ".json")))
		}
	}
}

// Stats 返回缓存占用统计：size_used_bytes / max_size_bytes / usage_percent / index_entries。
func (fc *FileCache) Stats() map[string]interface{} {
	if fc == nil {
		return map[string]interface{}{
			"size_used_bytes": int64(0),
			"max_size_bytes":  int64(0),
			"usage_percent":   0.0,
			"index_entries":   0,
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
		"index_entries":   len(fc.index),
	}
}

// Close 释放缓存。当前实现没有后台协程与外部资源，幂等返回 nil。
func (fc *FileCache) Close() error { return nil }