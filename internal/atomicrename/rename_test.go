package atomicrename

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestReplaceCoversExisting 验证已存在目标被完整覆盖（POSIX 原子替换语义的
// 基本面，两平台共用）。
func TestReplaceCoversExisting(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "final.txt")
	if err := os.WriteFile(final, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	tmp := filepath.Join(dir, ".final.txt-tmp")
	if err := os.WriteFile(tmp, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := Replace(tmp, final); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(got) != "new-content" {
		t.Fatalf("final = %q, want %q", got, "new-content")
	}
}

// TestReplaceConcurrentSameTarget 复现 storage/file B5 场景：多 goroutine
// 并发替换同一目标。Windows 上首个 MoveFileEx 瞬态 ACCESS_DENIED 必须经
// 重试收敛；POSIX 上天然原子。断言全部成功且终值是某次完整写入。
func TestReplaceConcurrentSameTarget(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "target.bin")

	const writers, perWrite = 8, 25
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = repeatByte(byte('a'+i), 4096)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWrite)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perWrite; j++ {
				tmp, err := os.CreateTemp(dir, ".target-*")
				if err != nil {
					errCh <- err
					return
				}
				if _, err := tmp.Write(payloads[i]); err != nil {
					_ = tmp.Close()
					errCh <- err
					return
				}
				if err := tmp.Close(); err != nil {
					errCh <- err
					return
				}
				if err := Replace(tmp.Name(), final); err != nil {
					_ = os.Remove(tmp.Name())
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发替换失败: %v", err)
	}

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	for _, p := range payloads {
		if string(got) == string(p) {
			return // 终值恰为某次完整写入
		}
	}
	t.Fatalf("终值非任何一次完整写入（疑似撕裂），len=%d", len(got))
}

func repeatByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestRemoveIdempotent 缺失路径归一为 nil；存在路径删除成功。
func TestRemoveIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Remove(filepath.Join(dir, "missing.txt")); err != nil {
		t.Fatalf("Remove missing: %v", err)
	}
	p := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := Remove(p); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file still exists after Remove")
	}
}
