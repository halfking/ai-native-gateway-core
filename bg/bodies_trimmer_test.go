package bg

// bodies_trimmer_test.go — 双模式存储架构 Task 5.2：BodiesTrimmer 单元测试。
// 纯文件系统测试：构造 t.TempDir() 目录树（{bodiesDir}/{tenant}/{前缀}/
// {sessionID}/turn_N.json.gz），用 os.Chtimes 把部分会话目录 mtime 设为
// 过去，验证整目录删除/保留/空父目录清理/统计与生命周期。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSessionDir 构造一个会话目录并写入 turns 文件，返回会话目录路径。
// 注意：先写文件再由调用方 Chtimes 会话目录（写文件会刷新目录 mtime）。
func writeSessionDir(t *testing.T, bodiesDir, tenant, prefix, sessionID string, turns ...string) string {
	t.Helper()
	dir := filepath.Join(bodiesDir, tenant, prefix, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	for _, name := range turns {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("gzip-body-"+name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	return dir
}

func TestNewBodiesTrimmer_Defaults(t *testing.T) {
	tr := NewBodiesTrimmer("/tmp/bodies", 72*time.Hour)
	if tr.bodiesDir != "/tmp/bodies" {
		t.Errorf("bodiesDir = %q, want /tmp/bodies", tr.bodiesDir)
	}
	if tr.retention != 72*time.Hour {
		t.Errorf("retention = %v, want 72h", tr.retention)
	}
	if tr.interval != 6*time.Hour {
		t.Errorf("interval = %v, want 6h", tr.interval)
	}
}

func TestBodiesTrimmer_WithInterval(t *testing.T) {
	tr := NewBodiesTrimmer("/tmp/bodies", time.Hour).WithInterval(10 * time.Millisecond)
	if tr.interval != 10*time.Millisecond {
		t.Errorf("interval = %v, want 10ms", tr.interval)
	}
	tr2 := NewBodiesTrimmer("/tmp/bodies", time.Hour).WithInterval(-1)
	if tr2.interval != 6*time.Hour {
		t.Errorf("interval = %v, want default 6h", tr2.interval)
	}
}

func TestBodiesTrimmer_TrimOnce_RemovesExpiredSessions(t *testing.T) {
	bodiesDir := t.TempDir()
	retention := 2 * time.Hour

	old1 := writeSessionDir(t, bodiesDir, "tenant-a", "ab", "sess-1111", "turn_1.json.gz", "turn_2.json.gz") // 2 文件
	fresh := writeSessionDir(t, bodiesDir, "tenant-a", "ab", "sess-2222", "turn_1.json.gz")                  // 1 文件
	old2 := writeSessionDir(t, bodiesDir, "tenant-b", "cd", "sess-3333", "turn_1.json.gz", "turn_2.json.gz", "turn_3.json.gz")

	// 会话目录判断的是目录自身 mtime：先写完文件，再整体置为过去。
	for _, dir := range []string{old1, old2} {
		past := time.Now().Add(-3 * retention)
		if err := os.Chtimes(dir, past, past); err != nil {
			t.Fatalf("Chtimes(%s): %v", dir, err)
		}
	}

	tr := NewBodiesTrimmer(bodiesDir, retention)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	// 过期会话目录整目录删除、新鲜会话保留。
	for _, dir := range []string{old1, old2} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("expired session dir %s still exists (err=%v)", dir, err)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh session dir %s should survive: %v", fresh, err)
	}
	// 过期会话内部的 turn 文件一并消失。
	if _, err := os.Stat(filepath.Join(old1, "turn_1.json.gz")); !os.IsNotExist(err) {
		t.Errorf("turn file inside expired session should be removed (err=%v)", err)
	}
	// 统计正确：2 个会话，5 个 gz 文件（"gzip-body-"+文件名，每个 24B，共 120B）。
	if tr.lastDeletedSessions != 2 {
		t.Errorf("lastDeletedSessions = %d, want 2", tr.lastDeletedSessions)
	}
	if tr.lastFreedBytes != 120 {
		t.Errorf("lastFreedBytes = %d, want 120", tr.lastFreedBytes)
	}
}

func TestBodiesTrimmer_TrimOnce_CleansEmptyParents(t *testing.T) {
	bodiesDir := t.TempDir()
	retention := time.Hour

	// tenant-x/xx 下只有一个过期会话：清理后前缀目录与租户目录应一并变空被删。
	orphan := writeSessionDir(t, bodiesDir, "tenant-x", "xx", "sess-old", "turn_1.json.gz")
	past := time.Now().Add(-2 * retention)
	if err := os.Chtimes(orphan, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// tenant-y/yy 仍有新鲜会话：其父目录必须保留。
	kept := writeSessionDir(t, bodiesDir, "tenant-y", "yy", "sess-fresh", "turn_1.json.gz")

	tr := NewBodiesTrimmer(bodiesDir, retention)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Fatalf("TrimOnce: %v", err)
	}

	if _, err := os.Stat(filepath.Join(bodiesDir, "tenant-x")); !os.IsNotExist(err) {
		t.Errorf("empty tenant dir should be removed (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(bodiesDir, "tenant-y", "yy")); err != nil {
		t.Errorf("non-empty prefix dir should survive: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("fresh session dir should survive: %v", err)
	}
}

func TestBodiesTrimmer_TrimOnce_MissingDir(t *testing.T) {
	tr := NewBodiesTrimmer(filepath.Join(t.TempDir(), "not-exist"), time.Hour)
	if err := tr.TrimOnce(context.Background()); err != nil {
		t.Errorf("TrimOnce on missing dir = %v, want nil", err)
	}
}

func TestBodiesTrimmer_TrimOnce_CanceledCtx(t *testing.T) {
	bodiesDir := t.TempDir()
	writeSessionDir(t, bodiesDir, "tenant-a", "ab", "sess-1111", "turn_1.json.gz")

	tr := NewBodiesTrimmer(bodiesDir, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消
	if err := tr.TrimOnce(ctx); err == nil {
		t.Error("TrimOnce with canceled ctx should return error")
	}
}

func TestBodiesTrimmer_Start_TrimLoopAndGracefulExit(t *testing.T) {
	bodiesDir := t.TempDir()
	retention := time.Hour
	old := writeSessionDir(t, bodiesDir, "tenant-a", "ab", "sess-1111", "turn_1.json.gz")
	past := time.Now().Add(-2 * retention)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	tr := NewBodiesTrimmer(bodiesDir, retention).WithInterval(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()

	// 等待首轮清理把过期会话目录删掉。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start 首轮清理未在期限内删除过期会话目录")
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
}

func TestDirSize_SumsFileSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("0123456789"), 0o644); err != nil { // 10B
		t.Fatalf("WriteFile: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("01234567890123456789"), 0o644); err != nil { // 20B
		t.Fatalf("WriteFile: %v", err)
	}
	if got := dirSize(dir); got != 30 {
		t.Errorf("dirSize = %d, want 30", got)
	}
	if got := dirSize(filepath.Join(t.TempDir(), "missing")); got != 0 {
		t.Errorf("dirSize on missing dir = %d, want 0", got)
	}
}
