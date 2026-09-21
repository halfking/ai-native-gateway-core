package bg

// cache_trimmer_test.go — 双模式存储架构 Task 5.1：CacheTrimmer 单元测试。
// 纯文件系统测试：构造 t.TempDir() 目录树，用 os.Chtimes 把部分文件
// mtime 设为过去（超过 retention），验证删除/保留/统计与生命周期。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCacheFile 在 cacheDir 下按 L1.5 布局写一个缓存文件，返回其路径。
func writeCacheFile(t *testing.T, cacheDir, tenant, prefix, name, content string) string {
	t.Helper()
	dir := filepath.Join(cacheDir, tenant, prefix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", p, err)
	}
	return p
}

// ageFile 把路径的 mtime 设为过去（now - age）。
func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	past := time.Now().Add(-age)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}

func TestNewCacheTrimmer_Defaults(t *testing.T) {
	tr := NewCacheTrimmer("/tmp/cache", 24*time.Hour)
	if tr.cacheDir != "/tmp/cache" {
		t.Errorf("cacheDir = %q, want /tmp/cache", tr.cacheDir)
	}
	if tr.retention != 24*time.Hour {
		t.Errorf("retention = %v, want 24h", tr.retention)
	}
	if tr.interval != time.Hour {
		t.Errorf("interval = %v, want 1h", tr.interval)
	}
}

func TestCacheTrimmer_WithInterval(t *testing.T) {
	tr := NewCacheTrimmer("/tmp/cache", time.Hour).WithInterval(10 * time.Millisecond)
	if tr.interval != 10*time.Millisecond {
		t.Errorf("interval = %v, want 10ms", tr.interval)
	}
	// 非正值应被忽略，保持默认。
	tr2 := NewCacheTrimmer("/tmp/cache", time.Hour).WithInterval(0)
	if tr2.interval != time.Hour {
		t.Errorf("interval = %v, want default 1h", tr2.interval)
	}
}

func TestCacheTrimmer_TrimOnce_RemovesExpiredKeepsFresh(t *testing.T) {
	cacheDir := t.TempDir()

	old1 := writeCacheFile(t, cacheDir, "tenant-a", "ab", "sess-1111.json", "0123456789")     // 10B
	fresh := writeCacheFile(t, cacheDir, "tenant-a", "ab", "sess-2222.json", "0123456789")    // 10B
	old2 := writeCacheFile(t, cacheDir, "tenant-b", "cd", "sess-3333.json", "01234567890123") // 14B
	retention := time.Hour
	ageFile(t, old1, 2*retention)
	ageFile(t, old2, 3*retention)
	// fresh 保持新鲜 mtime，不动。

	tr := NewCacheTrimmer(cacheDir, retention)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	// 过期文件已删、新鲜文件保留。
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Errorf("expired file %s still exists (err=%v)", old1, err)
	}
	if _, err := os.Stat(old2); !os.IsNotExist(err) {
		t.Errorf("expired file %s still exists (err=%v)", old2, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file %s should survive: %v", fresh, err)
	}
	// 统计正确：2 个文件、24 字节。
	if tr.lastDeletedFiles != 2 {
		t.Errorf("lastDeletedFiles = %d, want 2", tr.lastDeletedFiles)
	}
	if tr.lastFreedBytes != 24 {
		t.Errorf("lastFreedBytes = %d, want 24", tr.lastFreedBytes)
	}
}

func TestCacheTrimmer_TrimOnce_MissingDir(t *testing.T) {
	// 目录不存在时应静默返回（首次启动可能还没建目录）。
	tr := NewCacheTrimmer(filepath.Join(t.TempDir(), "not-exist"), time.Hour)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Errorf("TrimOnce on missing dir = %v, want nil", err)
	}
}

func TestCacheTrimmer_TrimOnce_CanceledCtx(t *testing.T) {
	cacheDir := t.TempDir()
	writeCacheFile(t, cacheDir, "tenant-a", "ab", "sess-1111.json", "data")

	tr := NewCacheTrimmer(cacheDir, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消
	if err := tr.TrimOnce(ctx); err == nil {
		t.Error("TrimOnce with canceled ctx should return error")
	}
}

func TestCacheTrimmer_Start_TrimLoopAndGracefulExit(t *testing.T) {
	cacheDir := t.TempDir()
	old := writeCacheFile(t, cacheDir, "tenant-a", "ab", "sess-1111.json", "data")
	retention := time.Hour
	ageFile(t, old, 2*retention)

	// 新鲜文件与过期文件同时就位，验证循环运行期间不会误删新鲜数据。
	fresh := writeCacheFile(t, cacheDir, "tenant-a", "ab", "sess-2222.json", "keep")

	tr := NewCacheTrimmer(cacheDir, retention).WithInterval(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()

	// 等待首轮清理把过期文件删掉（Start 启动即执行一次）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start 首轮清理未在期限内删除过期文件")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
		// Start 在 ctx 取消后优雅返回。
	case <-time.After(2 * time.Second):
		t.Fatal("Start 未在 ctx 取消后返回")
	}
	// 新鲜文件全程保持存活。
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file should survive: %v", err)
	}
}
