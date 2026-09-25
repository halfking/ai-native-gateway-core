package bg

// hot_zone_trimmer_test.go — 双模式热区层方案 H4：HotZoneTrimmer 单测。
//
// 覆盖：
//   - 过期清理（mtime < cutoff → 删除）；
//   - 配额回收（总盘占 > maxBytes → 最旧先删）；
//   - 三子树共享配额（cache + session_bodies + requests 共同计费）；
//   - 临时文件 (.tmp / .tmp-) 不参与 trimmer；
//   - SetRetention / SetMaxBytes 热重载；
//   - dir 缺失时静默 no-op。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newHotZoneDir 构造三子树临时目录（cache / session_bodies / requests）。
func newHotZoneDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"cache", "session_bodies", "requests"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return dir
}

// touchFile 在 path 写入 len(data) 字节并设置 mtime 为 mtime。
func touchFile(t *testing.T, path string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestHotZoneTrimmerExpiry(t *testing.T) {
	dir := newHotZoneDir(t)

	// 三个子树各放一个文件：两个过期（2h 前）+ 一个保留（10min 前）
	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(-10 * time.Minute)
	touchFile(t, filepath.Join(dir, "cache", "old.json"), []byte("aaaaaaaa"), old)
	touchFile(t, filepath.Join(dir, "session_bodies", "old.gz"), []byte("bbbbbbbb"), old)
	touchFile(t, filepath.Join(dir, "requests", "fresh.json"), []byte("cccccccc"), fresh)

	tr := NewHotZoneTrimmer(dir, time.Hour, 1<<30)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	// 过期文件应被删除，保留文件保留
	for _, removed := range []string{
		filepath.Join(dir, "cache", "old.json"),
		filepath.Join(dir, "session_bodies", "old.gz"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Errorf("expected removed: %s", removed)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "requests", "fresh.json")); err != nil {
		t.Errorf("expected kept: %v", err)
	}
	if got := tr.lastDeletedFiles; got != 2 {
		t.Errorf("lastDeletedFiles = %d, want 2", got)
	}
	if got := tr.lastFreedBytes; got != 16 {
		t.Errorf("lastFreedBytes = %d, want 16", got)
	}
}

func TestHotZoneTrimmerQuotaEvictionOldestFirst(t *testing.T) {
	dir := newHotZoneDir(t)

	// 三个文件各 1KB，mtime 错开 1s；maxBytes = 1500 字节 → 最旧先删
	t0 := time.Now().Add(-time.Hour) // 都未过期
	touchFile(t, filepath.Join(dir, "cache", "a.json"), make([]byte, 1024), t0)
	touchFile(t, filepath.Join(dir, "session_bodies", "b.json"), make([]byte, 1024), t0.Add(time.Second))
	touchFile(t, filepath.Join(dir, "requests", "c.json"), make([]byte, 1024), t0.Add(2*time.Second))

	tr := NewHotZoneTrimmer(dir, 24*time.Hour, 1500)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	// 1500 配额：3×1024=3076 总盘占，超限 → 最旧先删直到 ≤ 1500。
	// 删 a (剩 2048) > 1500，再删 b (剩 1024) ≤ 1500 → 共删 2。
	if _, err := os.Stat(filepath.Join(dir, "cache", "a.json")); !os.IsNotExist(err) {
		t.Errorf("a.json should be evicted (oldest)")
	}
	if _, err := os.Stat(filepath.Join(dir, "session_bodies", "b.json")); !os.IsNotExist(err) {
		t.Errorf("b.json should be evicted (next-oldest, still over quota)")
	}
	if _, err := os.Stat(filepath.Join(dir, "requests", "c.json")); err != nil {
		t.Errorf("c.json should remain (within quota): %v", err)
	}
	if got := tr.lastDeletedFiles; got != 2 {
		t.Errorf("lastDeletedFiles = %d, want 2", got)
	}
}

func TestHotZoneTrimmerTempFilesIgnored(t *testing.T) {
	dir := newHotZoneDir(t)

	// .tmp / .tmp- 前缀临时文件：trimmer 应跳过（AsyncFileWriter 写入约定）
	touchFile(t, filepath.Join(dir, "requests", "abc.json.tmp"), []byte("xxxx"), time.Now().Add(-2*time.Hour))
	touchFile(t, filepath.Join(dir, "requests", "abc.json.tmp-12345"), []byte("yyyy"), time.Now().Add(-2*time.Hour))
	touchFile(t, filepath.Join(dir, "requests", "real.json"), []byte("zzzz"), time.Now().Add(-2*time.Hour))

	tr := NewHotZoneTrimmer(dir, time.Hour, 1<<30)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	// .tmp 仍存在（不在 trimmer 范围），real.json 被删（过期）
	for _, kept := range []string{
		filepath.Join(dir, "requests", "abc.json.tmp"),
		filepath.Join(dir, "requests", "abc.json.tmp-12345"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf(".tmp file should be kept: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "requests", "real.json")); !os.IsNotExist(err) {
		t.Errorf("real.json should be removed (expired)")
	}
}

func TestHotZoneTrimmerMissingDir(t *testing.T) {
	tr := NewHotZoneTrimmer(filepath.Join(t.TempDir(), "no-such-dir"), time.Hour, 1024)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("missing dir should be no-op, got %v", err)
	}
}

func TestHotZoneTrimmerHotReload(t *testing.T) {
	dir := newHotZoneDir(t)

	// retention = 24h → 文件保留
	touchFile(t, filepath.Join(dir, "cache", "x.json"), []byte("aaaa"), time.Now().Add(-time.Hour))

	tr := NewHotZoneTrimmer(dir, 24*time.Hour, 1<<30)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce initial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "x.json")); err != nil {
		t.Fatalf("x.json should remain under 24h retention: %v", err)
	}

	// 热重载 retention = 30min → 文件过期
	tr.SetRetention(30 * time.Minute)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce after retention shrink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "x.json")); !os.IsNotExist(err) {
		t.Errorf("x.json should be evicted after retention shrink")
	}

	// 热重载 maxBytes：再次写入并设置小配额
	touchFile(t, filepath.Join(dir, "cache", "y.json"), make([]byte, 1024), time.Now())
	touchFile(t, filepath.Join(dir, "session_bodies", "z.json"), make([]byte, 1024), time.Now())
	tr.SetMaxBytes(1500)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce after maxBytes shrink: %v", err)
	}
	// y（mtime 较早）应被淘汰；z 保留
	if _, err := os.Stat(filepath.Join(dir, "cache", "y.json")); !os.IsNotExist(err) {
		t.Errorf("y.json should be evicted under quota")
	}
	if _, err := os.Stat(filepath.Join(dir, "session_bodies", "z.json")); err != nil {
		t.Errorf("z.json should remain: %v", err)
	}
}
