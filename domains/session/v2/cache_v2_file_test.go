// Package v2: FileCache（L1.5 本地文件缓存层）单元测试
//
// 覆盖：Set/Get/Delete 往返、未命中、TTL 过期清理、maxSize LRU 淘汰、
// 损坏 JSON 处理、启动 Walk 统计、Stats 数值、并发混合读写删（-race）。
// 运行：go test -race -run 'TestFileCache' ./domains/session/v2/
package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestFileCache 在临时目录中创建 FileCache，测试结束自动清理。
func newTestFileCache(t *testing.T, dir string, ttl time.Duration, maxSize int64) *FileCache {
	t.Helper()
	fc, err := NewFileCache(filepath.Join(dir, "l15"), ttl, maxSize)
	if err != nil {
		t.Fatalf("NewFileCache(%s, %v, %d): %v", dir, ttl, maxSize, err)
	}
	return fc
}

// newTestFileState 构造测试用 SessionStateV2。
// UpdatedAt 使用固定时间，保证 JSON 序列化长度稳定（LRU 淘汰测试依赖等长条目）。
func newTestFileState(tenantID, sessionID string, turn int) *SessionStateV2 {
	return &SessionStateV2{
		SessionID:  sessionID,
		TenantID:   tenantID,
		LastTurnNo: turn,
		UpdatedAt:  time.Unix(1700000000, 123456789).UTC(),
		CompressionMeta: CompressionMeta{
			Strategy:      "truncate",
			TokenEstimate: 100 * turn,
			MsgCount:      turn,
		},
		GovernanceMeta: GovernanceMeta{
			LastInjectionVerdict: "pass",
		},
	}
}

// assertFileStateEqual 比较往返后的关键字段（嵌套 map 经过 JSON 往返后数值
// 类型会变为 float64，因此做字段级比较而非整体 DeepEqual）。
func assertFileStateEqual(t *testing.T, got, want *SessionStateV2) {
	t.Helper()
	if got == nil {
		t.Fatal("Get returned nil state")
	}
	if got.SessionID != want.SessionID || got.TenantID != want.TenantID || got.LastTurnNo != want.LastTurnNo {
		t.Fatalf("identity mismatch: got %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("UpdatedAt mismatch: got %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if got.CompressionMeta.Strategy != want.CompressionMeta.Strategy ||
		got.CompressionMeta.TokenEstimate != want.CompressionMeta.TokenEstimate ||
		got.CompressionMeta.MsgCount != want.CompressionMeta.MsgCount {
		t.Fatalf("CompressionMeta mismatch: got %+v, want %+v", got.CompressionMeta, want.CompressionMeta)
	}
	if got.GovernanceMeta.LastInjectionVerdict != want.GovernanceMeta.LastInjectionVerdict {
		t.Fatalf("GovernanceMeta mismatch: got %+v, want %+v", got.GovernanceMeta, want.GovernanceMeta)
	}
}

func TestFileCacheRoundTrip(t *testing.T) {
	fc := newTestFileCache(t, t.TempDir(), time.Minute, 1<<20)

	// 构造器应已创建目录（0755）
	if fi, err := os.Stat(fc.baseDir); err != nil || !fi.IsDir() {
		t.Fatalf("baseDir not created: %v", err)
	}

	want := newTestFileState("tenant-a", "sess-abcdef", 3)
	if err := fc.Set(want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := fc.Get("tenant-a", "sess-abcdef")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertFileStateEqual(t, got, want)

	// 文件落在规格约定的布局 {baseDir}/{tenant}/{前2位}/{session}.json
	expected := filepath.Join(fc.baseDir, "tenant-a", "se", "sess-abcdef.json")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("layout file missing: %v", err)
	}

	// sessionID 不足 2 位时用全量作为分片目录
	short := newTestFileState("tenant-a", "s", 1)
	if err := fc.Set(short); err != nil {
		t.Fatalf("Set short: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fc.baseDir, "tenant-a", "s", "s.json")); err != nil {
		t.Fatalf("short session layout missing: %v", err)
	}
	if got, err = fc.Get("tenant-a", "s"); err != nil {
		t.Fatalf("Get short: %v", err)
	}
	assertFileStateEqual(t, got, short)

	// 覆盖写：同 key 二次 Set 后读到新值
	updated := newTestFileState("tenant-a", "sess-abcdef", 9)
	if err := fc.Set(updated); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	got, err = fc.Get("tenant-a", "sess-abcdef")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	assertFileStateEqual(t, got, updated)

	// Delete 幂等且生效
	if err := fc.Delete("tenant-a", "sess-abcdef"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := fc.Delete("tenant-a", "sess-abcdef"); err != nil {
		t.Fatalf("Delete again (idempotent): %v", err)
	}
	if _, err := fc.Get("tenant-a", "sess-abcdef"); !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get after delete: want errCacheMiss, got %v", err)
	}

	// Close 幂等
	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := fc.Close(); err != nil {
		t.Fatalf("Close twice: %v", err)
	}
}

func TestFileCacheGetNotFound(t *testing.T) {
	fc := newTestFileCache(t, t.TempDir(), time.Minute, 1<<20)

	state, err := fc.Get("tenant-x", "sess-missing")
	if state != nil {
		t.Fatalf("Get missing: want nil state, got %+v", state)
	}
	if !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get missing: want errCacheMiss, got %v", err)
	}

	// 非法 ID 同样按未命中处理
	if _, err := fc.Get("tenant-x", ""); !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get empty session: want errCacheMiss, got %v", err)
	}
}

func TestFileCacheTTLExpiry(t *testing.T) {
	fc := newTestFileCache(t, t.TempDir(), 50*time.Millisecond, 1<<20)

	if err := fc.Set(newTestFileState("tenant-ttl", "sess-ttl", 1)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if used := fc.Stats()["size_used_bytes"].(int64); used <= 0 {
		t.Fatalf("sizeUsed after Set = %d, want > 0", used)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := fc.Get("tenant-ttl", "sess-ttl"); !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get expired: want errCacheMiss, got %v", err)
	}
	// 过期文件被清理
	if _, err := os.Stat(fc.buildPath("tenant-ttl", "sess-ttl")); !os.IsNotExist(err) {
		t.Fatalf("expired file should be removed, stat err = %v", err)
	}
	// 过期清理后记账归零
	if used := fc.Stats()["size_used_bytes"].(int64); used != 0 {
		t.Fatalf("sizeUsed after expiry = %d, want 0", used)
	}
}

func TestFileCacheEvictionLRU(t *testing.T) {
	// 用探针条目测量单条 JSON 大小，maxSize 恰好容纳 2 条：
	// 写第 3 条时必须淘汰最旧的 s1，最终 sizeUsed == 2*entrySize <= maxSize。
	// 探针与循环内条目同构（同 tenant、同长度 sessionID、同位数 turn），
	// 保证所有条目序列化后等长。
	probe := newTestFileState("tenant-lru", "sess-key-1", 1)
	probe.CompressionMeta.CutMarker = map[string]interface{}{"pad": strings.Repeat("x", 512)}
	raw, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	entrySize := int64(len(raw))

	fc := newTestFileCache(t, t.TempDir(), time.Hour, entrySize*2)

	pad := func(st *SessionStateV2) {
		st.CompressionMeta.CutMarker = map[string]interface{}{"pad": strings.Repeat("x", 512)}
	}
	mtimes := make([]time.Time, 4) // 下标 1..3
	for i := 1; i <= 3; i++ {
		sessionID := fmt.Sprintf("sess-key-%d", i)
		st := newTestFileState("tenant-lru", sessionID, i)
		pad(st)
		if err := fc.Set(st); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
		// 显式错开 mtime，保证「最旧先删」判定与写入顺序一致，
		// 不依赖文件系统 mtime 精度。
		mtimes[i] = time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(fc.buildPath("tenant-lru", sessionID), mtimes[i], mtimes[i]); err != nil {
			t.Fatalf("Chtimes %d: %v", i, err)
		}
	}

	// 最旧的 s1 被淘汰，s2/s3 保留
	if _, err := fc.Get("tenant-lru", "sess-key-1"); !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get evicted oldest: want errCacheMiss, got %v", err)
	}
	for _, sessionID := range []string{"sess-key-2", "sess-key-3"} {
		if _, err := fc.Get("tenant-lru", sessionID); err != nil {
			t.Fatalf("Get %s after eviction: %v", sessionID, err)
		}
	}

	// sizeUsed 不超限
	stats := fc.Stats()
	used := stats["size_used_bytes"].(int64)
	if used > stats["max_size_bytes"].(int64) {
		t.Fatalf("sizeUsed %d exceeds maxSize %d", used, stats["max_size_bytes"].(int64))
	}
	if used != entrySize*2 {
		t.Fatalf("sizeUsed = %d, want %d", used, entrySize*2)
	}
	if got, want := stats["usage_percent"].(float64), 100.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("usage_percent = %v, want %v", got, want)
	}
}

func TestFileCacheStats(t *testing.T) {
	fc := newTestFileCache(t, t.TempDir(), time.Minute, 10_000)

	stats := fc.Stats()
	if got := stats["size_used_bytes"].(int64); got != 0 {
		t.Fatalf("initial size_used_bytes = %d, want 0", got)
	}
	if got := stats["max_size_bytes"].(int64); got != 10_000 {
		t.Fatalf("max_size_bytes = %d, want 10000", got)
	}
	if got := stats["usage_percent"].(float64); got != 0 {
		t.Fatalf("initial usage_percent = %v, want 0", got)
	}

	s1 := newTestFileState("tenant-st", "sess-one", 1)
	s2 := newTestFileState("tenant-st", "sess-two", 2)
	if err := fc.Set(s1); err != nil {
		t.Fatalf("Set s1: %v", err)
	}
	if err := fc.Set(s2); err != nil {
		t.Fatalf("Set s2: %v", err)
	}

	raw1, err := json.Marshal(s1)
	if err != nil {
		t.Fatalf("marshal s1: %v", err)
	}
	raw2, err := json.Marshal(s2)
	if err != nil {
		t.Fatalf("marshal s2: %v", err)
	}
	want := int64(len(raw1) + len(raw2))

	stats = fc.Stats()
	if got := stats["size_used_bytes"].(int64); got != want {
		t.Fatalf("size_used_bytes = %d, want %d", got, want)
	}
	wantPercent := float64(want) / 10000 * 100
	if got := stats["usage_percent"].(float64); math.Abs(got-wantPercent) > 1e-9 {
		t.Fatalf("usage_percent = %v, want %v", got, wantPercent)
	}
}

func TestFileCacheCorruptJSON(t *testing.T) {
	fc := newTestFileCache(t, t.TempDir(), time.Minute, 1<<20)

	if err := fc.Set(newTestFileState("tenant-bad", "sess-bad", 1)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	path := fc.buildPath("tenant-bad", "sess-bad")

	// 人为写坏文件，模拟异常断电留下的半截 JSON
	if err := os.WriteFile(path, []byte(`{"SessionID":"sess-bad"`), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	got, err := fc.Get("tenant-bad", "sess-bad")
	if got != nil {
		t.Fatalf("Get corrupt: want nil state, got %+v", got)
	}
	if !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get corrupt: want errCacheMiss, got %v", err)
	}
	// 损坏文件按缓存未命中被清理
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt file should be removed, stat err = %v", statErr)
	}
	if used := fc.Stats()["size_used_bytes"].(int64); used < 0 {
		t.Fatalf("sizeUsed after corrupt cleanup = %d, want >= 0", used)
	}
}

func TestFileCacheStartupWalk(t *testing.T) {
	dir := t.TempDir()
	fc1 := newTestFileCache(t, dir, time.Minute, 1<<20)

	if err := fc1.Set(newTestFileState("tenant-w", "sess-one", 1)); err != nil {
		t.Fatalf("Set s1: %v", err)
	}
	if err := fc1.Set(newTestFileState("tenant-w", "sess-two", 2)); err != nil {
		t.Fatalf("Set s2: %v", err)
	}
	want := fc1.Stats()["size_used_bytes"].(int64)
	if want <= 0 {
		t.Fatalf("sizeUsed before reopen = %d, want > 0", want)
	}
	_ = fc1.Close()

	// 重新打开同一目录：启动 Walk 应统计出现有占用
	fc2 := newTestFileCache(t, dir, time.Minute, 1<<20)
	if got := fc2.Stats()["size_used_bytes"].(int64); got != want {
		t.Fatalf("startup sizeUsed = %d, want %d", got, want)
	}
	// 重启后旧数据仍可读
	if _, err := fc2.Get("tenant-w", "sess-one"); err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	_ = fc2.Close()
}

func TestFileCacheConcurrentAccess(t *testing.T) {
	t.Run("mixed get set delete", func(t *testing.T) {
		// TTL 很短（20ms）+ 每轮小睡，保证过期清理与 Set/Delete 在并发下交错
		fc := newTestFileCache(t, t.TempDir(), 20*time.Millisecond, 64*1024)

		const (
			goroutines = 16
			iterations = 50
			keyCount   = 8
		)

		var wg sync.WaitGroup
		errCh := make(chan error, goroutines)
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(seed int64) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(seed))
				for i := 0; i < iterations; i++ {
					tenant := "tenant-cc"
					sessionID := fmt.Sprintf("sess-%02d", rng.Intn(keyCount))
					switch rng.Intn(3) {
					case 0:
						if err := fc.Set(newTestFileState(tenant, sessionID, i+1)); err != nil {
							errCh <- fmt.Errorf("Set %s: %w", sessionID, err)
							return
						}
					case 1:
						if _, err := fc.Get(tenant, sessionID); err != nil && !errors.Is(err, errCacheMiss) {
							errCh <- fmt.Errorf("Get %s: %w", sessionID, err)
							return
						}
					default:
						if err := fc.Delete(tenant, sessionID); err != nil {
							errCh <- fmt.Errorf("Delete %s: %w", sessionID, err)
							return
						}
					}
					time.Sleep(2 * time.Millisecond)
				}
			}(int64(g))
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Errorf("concurrent op failed: %v", err)
		}

		// 静默后核对记账与磁盘一致：sizeUsed == 目录下所有 *.json 体积之和，
		// 且不超过 maxSize。
		stats := fc.Stats()
		used := stats["size_used_bytes"].(int64)
		if used < 0 {
			t.Fatalf("sizeUsed = %d, want >= 0", used)
		}
		if used > stats["max_size_bytes"].(int64) {
			t.Fatalf("sizeUsed %d exceeds maxSize %d", used, stats["max_size_bytes"].(int64))
		}
		var disk int64
		_ = filepath.Walk(fc.baseDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".json") {
				disk += info.Size()
			}
			return nil
		})
		if disk != used {
			t.Fatalf("sizeUsed %d != disk usage %d (accounting drift)", used, disk)
		}
	})
}

// TestFileCacheSizeUsedSelfHeal 回归：外部清理者（bg.CacheTrimmer 按 mtime 直接删文件）
// 不经过 FileCache 记账，sizeUsed 会虚高；溢出触发全树遍历时必须以磁盘实况重置，
// 否则每次 Set 都会触发 Walk 并误删仍活跃的新文件（审计 P1）。
func TestFileCacheSizeUsedSelfHeal(t *testing.T) {
	dir := t.TempDir()
	fc := newTestFileCache(t, dir, time.Hour, 1<<30)

	if err := fc.Set(newTestFileState("t1", "s-selfheal", 1)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// 模拟 CacheTrimmer 绕过记账直接删文件
	if err := os.Remove(fc.buildPath("t1", "s-selfheal")); err != nil {
		t.Fatalf("external remove: %v", err)
	}

	// 新实例（sizeUsed 按空目录为 0，随后强制溢出走 ensureSpaceLocked）
	fc2 := newTestFileCache(t, dir, time.Hour, 1<<30)
	fc2.mu.Lock()
	fc2.maxSize = 1
	fc2.mu.Unlock()
	if err := fc2.Set(newTestFileState("t1", "s-selfheal", 1)); err != nil {
		t.Fatalf("Set after overflow: %v", err)
	}

	got := fc2.Stats()["size_used_bytes"].(int64)
	if want := fileCacheOnDiskBytes(t, dir); got != want {
		t.Fatalf("溢出后 sizeUsed = %d，磁盘实况 = %d，记账未自愈", got, want)
	}
}

// fileCacheOnDiskBytes 统计 dir 下所有 .json 文件实际体积。
func fileCacheOnDiskBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		total += info.Size()
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return total
}
