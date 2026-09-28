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
	"sync/atomic"
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

// TestHotZoneTrimmerApplyReloadValues 验证 applyReload 的取值语义：
// 非正 retention/maxBytes = 维持现值；enabled 透传。
func TestHotZoneTrimmerApplyReloadValues(t *testing.T) {
	tr := NewHotZoneTrimmer(t.TempDir(), time.Hour, 1024)

	tr.WithReload(func() (time.Duration, int64, bool) { return 2 * time.Hour, 2048, false })
	if tr.applyReload() {
		t.Fatal("applyReload should report enabled=false")
	}
	if got := time.Duration(tr.retention.Load()); got != 2*time.Hour {
		t.Errorf("retention = %v, want 2h", got)
	}
	if got := tr.maxBytes.Load(); got != 2048 {
		t.Errorf("maxBytes = %d, want 2048", got)
	}

	// 非正值维持现值（接线层语义：settings 读到非法/零值时不覆盖）
	tr.WithReload(func() (time.Duration, int64, bool) { return 0, -1, true })
	if !tr.applyReload() {
		t.Fatal("applyReload should report enabled=true")
	}
	if got := time.Duration(tr.retention.Load()); got != 2*time.Hour {
		t.Errorf("retention = %v, want unchanged 2h", got)
	}
	if got := tr.maxBytes.Load(); got != 2048 {
		t.Errorf("maxBytes = %d, want unchanged 2048", got)
	}
}

// TestHotZoneTrimmerReloadHookShrinksRetention H4 热重载接线验收：WithReload
// 钩子在 Start 的每轮 TrimOnce 前生效——retention 从 24h 收紧到 30min 后，
// 原本保留的文件在下一个 tick 被淘汰（≤1 轮询周期生效）。
func TestHotZoneTrimmerReloadHookShrinksRetention(t *testing.T) {
	dir := newHotZoneDir(t)
	path := filepath.Join(dir, "cache", "x.json")
	touchFile(t, path, []byte("aaaa"), time.Now().Add(-time.Hour)) // 对 30min 已过期

	retention := 24 * time.Hour
	tr := NewHotZoneTrimmer(dir, retention, 1<<30)
	var calls atomic.Int64
	tr.WithReload(func() (time.Duration, int64, bool) {
		calls.Add(1)
		return 30 * time.Minute, 0, true
	}).WithInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.Start(ctx)
	}()

	// 首轮 applyReload 在 Start 内同步执行：retention 已收紧 → x.json 应被删
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("x.json should be evicted after reload tightened retention")
	}
	if calls.Load() == 0 {
		t.Errorf("reload hook was never called")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not exit after ctx cancel")
	}
}

// TestHotZoneTrimmerReloadDisabledSkipsRound 热重载 enabled=false：本轮（含
// 首轮）跳过清理，目录内容原样保留；恢复 true 后恢复清理。
func TestHotZoneTrimmerReloadDisabledSkipsRound(t *testing.T) {
	dir := newHotZoneDir(t)
	path := filepath.Join(dir, "cache", "x.json")
	touchFile(t, path, []byte("aaaa"), time.Now().Add(-time.Hour)) // 过期，但应被跳过保护

	// 禁用窗口内跑过多个 tick，过期文件必须原样保留；原子开关供闭包跨
	// goroutine 读取（-race 干净）
	var enabledFlag atomic.Bool
	enabledFlag.Store(false)
	tr := NewHotZoneTrimmer(dir, time.Hour, 1<<30)
	tr.WithReload(func() (time.Duration, int64, bool) { return 0, 0, enabledFlag.Load() }).
		WithInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.Start(ctx)
	}()

	// 禁用窗口内跑过多个 tick，过期文件必须原样保留
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("disabled round must not delete files: %v", err)
	}

	// 恢复启用 → 下一个 tick 删除
	enabledFlag.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("x.json should be evicted after re-enable")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not exit after ctx cancel")
	}
}
