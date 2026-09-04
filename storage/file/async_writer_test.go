package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestWriter 创建一个测试用写入器，并在测试结束后优雅关闭。
func newTestWriter(t *testing.T, workers int) *AsyncFileWriter {
	t.Helper()
	w := NewAsyncFileWriter(workers)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return w
}

// waitDone 带超时地等待任务结果，避免用例异常时挂死。
func waitDone(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("等待写入结果超时（%v）", timeout)
		return nil
	}
}

// TestNewAsyncFileWriterDefaults 验证 workers 参数防御与队列缓冲长度。
func TestNewAsyncFileWriterDefaults(t *testing.T) {
	for _, workers := range []int{0, -1, -100} {
		w := NewAsyncFileWriter(workers)
		if w.workers != defaultWorkers {
			t.Errorf("NewAsyncFileWriter(%d).workers = %d, want %d", workers, w.workers, defaultWorkers)
		}
		if cap(w.queue) != defaultQueueSize {
			t.Errorf("queue cap = %d, want %d", cap(w.queue), defaultQueueSize)
		}
		if err := w.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}

	w := NewAsyncFileWriter(7)
	if w.workers != 7 {
		t.Errorf("NewAsyncFileWriter(7).workers = %d, want 7", w.workers)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// TestWrite 表驱动验证同步写入：内容一致、目录自动创建、覆盖写、空数据、非法路径。
func TestWrite(t *testing.T) {
	tests := []struct {
		name    string
		relPath string
		data    []byte
		wantErr bool
	}{
		{name: "基本写入", relPath: "a.txt", data: []byte("hello gateway")},
		{name: "嵌套目录自动创建", relPath: "lvl1/lvl2/lvl3/deep.txt", data: []byte(`{"k":1}`)},
		{name: "覆盖写入", relPath: "overwrite.txt", data: []byte("second")},
		{name: "空数据", relPath: "empty.bin", data: nil},
		{name: "中文文件名", relPath: "目录/文件.txt", data: []byte("内容")},
		{name: "空路径报错", relPath: "", data: []byte("x"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			w := newTestWriter(t, 2)

			// 覆盖写入场景：先写一次旧内容
			if tt.name == "覆盖写入" {
				if err := w.Write(filepath.Join(dir, tt.relPath), []byte("first")); err != nil {
					t.Fatalf("预写入失败: %v", err)
				}
			}

			path := filepath.Join(dir, tt.relPath)
			err := w.Write(path, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Write(%q) 期望报错，实际 nil", path)
				}
				stats := w.Stats()
				if stats["failed_writes"] != 1 {
					t.Errorf("failed_writes = %d, want 1", stats["failed_writes"])
				}
				return
			}
			if err != nil {
				t.Fatalf("Write() error = %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读取文件失败: %v", err)
			}
			if string(got) != string(tt.data) {
				t.Errorf("文件内容 = %q, want %q", got, tt.data)
			}

			// 嵌套目录场景：确认父目录确实被自动创建
			if tt.name == "嵌套目录自动创建" {
				fi, err := os.Stat(filepath.Dir(path))
				if err != nil || !fi.IsDir() {
					t.Errorf("父目录未自动创建: %v", err)
				}
			}

			// 统计预期：覆盖写入场景包含一次预写入
			wantWrites, wantBytes := uint64(1), uint64(len(tt.data))
			if tt.name == "覆盖写入" {
				wantWrites, wantBytes = 2, wantBytes+uint64(len("first"))
			}
			stats := w.Stats()
			if stats["total_writes"] != wantWrites {
				t.Errorf("total_writes = %d, want %d", stats["total_writes"], wantWrites)
			}
			if stats["total_bytes"] != wantBytes {
				t.Errorf("total_bytes = %d, want %d", stats["total_bytes"], wantBytes)
			}
		})
	}
}

// TestWriteAsync 验证异步写入：成功路径收到 nil，错误路径收到 error。
func TestWriteAsync(t *testing.T) {
	t.Run("成功路径", func(t *testing.T) {
		dir := t.TempDir()
		w := newTestWriter(t, 2)

		path := filepath.Join(dir, "async", "ok.txt")
		done := w.WriteAsync(path, []byte("async-data"))
		if err := waitDone(t, done, 5*time.Second); err != nil {
			t.Fatalf("WriteAsync 返回错误: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取文件失败: %v", err)
		}
		if string(got) != "async-data" {
			t.Errorf("内容 = %q, want %q", got, "async-data")
		}
	})

	t.Run("错误路径_父目录是文件", func(t *testing.T) {
		dir := t.TempDir()
		w := newTestWriter(t, 2)

		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("准备 blocker 文件失败: %v", err)
		}
		badPath := filepath.Join(blocker, "sub", "f.txt") // 父组件是普通文件，MkdirAll 必失败
		done := w.WriteAsync(badPath, []byte("y"))
		if err := waitDone(t, done, 5*time.Second); err == nil {
			t.Fatal("期望收到 error，实际 nil")
		}
		if stats := w.Stats(); stats["failed_writes"] != 1 {
			t.Errorf("failed_writes = %d, want 1", stats["failed_writes"])
		}
	})
}

// TestWriteAsyncDoneChannelClosed 验证 Done 恰好写入一次并被关闭（无泄漏）。
func TestWriteAsyncDoneChannelClosed(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, 2)

	done := w.WriteAsync(filepath.Join(dir, "closed.txt"), []byte("x"))
	if err := waitDone(t, done, 5*time.Second); err != nil {
		t.Fatalf("WriteAsync 返回错误: %v", err)
	}
	// 通道应已被关闭：第二次读取立即返回 ok=false
	select {
	case _, ok := <-done:
		if ok {
			t.Error("Done 通道应已关闭，但收到了值")
		}
	case <-time.After(time.Second):
		t.Error("Done 通道未关闭，存在泄漏")
	}
}

// TestAtomicWriteNoTmpResidue 验证原子写入后目标路径不留 .tmp 残留。
func TestAtomicWriteNoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, 2)

	path := filepath.Join(dir, "atomic.txt")
	if err := w.Write(path, []byte("payload")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := os.Stat(path + tmpSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("存在 .tmp 残留: %v", err)
	}
}

// TestConcurrentWrites 并发压测：50 goroutine × 20 次写同一目录不同文件。
func TestConcurrentWrites(t *testing.T) {
	const (
		goroutines = 50
		perG       = 20
	)
	dir := t.TempDir()
	w := newTestWriter(t, 4)

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*perG)
	data := []byte("concurrent-payload")
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				path := filepath.Join(dir, fmt.Sprintf("g%02d-%02d.txt", g, i))
				if err := w.Write(path, data); err != nil {
					errCh <- fmt.Errorf("g%d i%d: %w", g, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("并发写入失败: %v", err)
	}

	// 抽查全部文件确实存在且内容正确
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perG; i++ {
			path := filepath.Join(dir, fmt.Sprintf("g%02d-%02d.txt", g, i))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("文件缺失 %s: %v", path, err)
				continue
			}
			if string(got) != string(data) {
				t.Errorf("文件 %s 内容不符", path)
			}
		}
	}

	stats := w.Stats()
	if stats["total_writes"] != goroutines*perG {
		t.Errorf("total_writes = %d, want %d", stats["total_writes"], goroutines*perG)
	}
	if stats["failed_writes"] != 0 {
		t.Errorf("failed_writes = %d, want 0", stats["failed_writes"])
	}
	if stats["total_bytes"] != uint64(goroutines*perG*len(data)) {
		t.Errorf("total_bytes = %d, want %d", stats["total_bytes"], goroutines*perG*len(data))
	}
}

// TestGracefulClose 优雅关闭：入队 100 个任务后立即 Close，所有任务都完成且文件存在。
func TestGracefulClose(t *testing.T) {
	dir := t.TempDir()
	w := NewAsyncFileWriter(4) // 不用 newTestWriter：本用例自行管理关闭

	const total = 100
	dones := make([]<-chan error, total)
	paths := make([]string, total)
	data := []byte("graceful-close-payload")
	for i := 0; i < total; i++ {
		paths[i] = filepath.Join(dir, fmt.Sprintf("task-%03d.txt", i))
		dones[i] = w.WriteAsync(paths[i], data) // 全部入队，不等待
	}
	// 立即关闭：worker 必须排空队列，所有 Done 恰好收到一次结果
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	for i, done := range dones {
		if err := waitDone(t, done, 5*time.Second); err != nil {
			t.Errorf("任务 %d 结果错误: %v", i, err)
			continue
		}
		got, err := os.ReadFile(paths[i])
		if err != nil {
			t.Errorf("任务 %d 文件缺失: %v", i, err)
			continue
		}
		if string(got) != string(data) {
			t.Errorf("任务 %d 内容不符", i)
		}
	}

	stats := w.Stats()
	if stats["total_writes"] != total {
		t.Errorf("total_writes = %d, want %d", stats["total_writes"], total)
	}
	if stats["failed_writes"] != 0 {
		t.Errorf("failed_writes = %d, want 0", stats["failed_writes"])
	}
}

// TestWriteAfterClose 关闭后再写入必须立即返回错误且不 panic。
func TestWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	w := NewAsyncFileWriter(2)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// 同步 Write：立即返回 ErrWriterClosed
	err := w.Write(filepath.Join(dir, "after-close.txt"), []byte("x"))
	if !errors.Is(err, ErrWriterClosed) {
		t.Errorf("Write after Close = %v, want ErrWriterClosed", err)
	}

	// 异步 WriteAsync：返回的通道携带 ErrWriterClosed 且已关闭
	done := w.WriteAsync(filepath.Join(dir, "after-close-async.txt"), []byte("x"))
	select {
	case err := <-done:
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("WriteAsync after Close = %v, want ErrWriterClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteAsync after Close 阻塞了")
	}

	// 关闭后不应产生任何文件
	if _, serr := os.Stat(filepath.Join(dir, "after-close.txt")); !errors.Is(serr, os.ErrNotExist) {
		t.Error("关闭后的写入不应落盘")
	}

	// 统计不应被关闭后的调用污染
	if stats := w.Stats(); stats["total_writes"] != 0 || stats["failed_writes"] != 0 {
		t.Errorf("关闭后统计被污染: %v", stats)
	}
}

// TestCloseIdempotent 验证 Close 幂等，可安全重复调用。
func TestCloseIdempotent(t *testing.T) {
	w := NewAsyncFileWriter(2)
	dir := t.TempDir()
	if err := w.Write(filepath.Join(dir, "pre.txt"), []byte("x")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Close(); err != nil {
			t.Errorf("第 %d 次 Close() error = %v", i+1, err)
		}
	}
}

// TestConcurrentCloseRace 并发调用 Close 与 Write，验证不 panic、无死锁。
func TestConcurrentCloseRace(t *testing.T) {
	dir := t.TempDir()
	w := NewAsyncFileWriter(4)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = w.Write(filepath.Join(dir, fmt.Sprintf("race-%d.txt", i)), []byte("x"))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // 让部分写入先入队
		_ = w.Close()
	}()
	wg.Wait()

	// 关闭后再写：只能得到 ErrWriterClosed，不得 panic
	if err := w.Write(filepath.Join(dir, "post.txt"), []byte("x")); !errors.Is(err, ErrWriterClosed) {
		t.Errorf("关闭后 Write = %v, want ErrWriterClosed", err)
	}
}
