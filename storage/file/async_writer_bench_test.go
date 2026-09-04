package file

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// benchPayload 构造约 512 字节的模拟会话记录负载。
func benchPayload() []byte {
	base := []byte(`{"session_id":"sess-bench-0000000000","model":"gpt-4o","messages":[{"role":"user","content":"benchmark payload for async file writer throughput measurement"}],"usage":{"prompt_tokens":128,"completion_tokens":64},"metadata":{"source":"bench","tenant":"default"}}`)
	// 补齐到至少 512 字节，模拟真实记录大小
	for len(base) < 512 {
		base = append(base, ' ')
	}
	return base
}

// benchWriteTo 为每次操作生成唯一目标路径，避免并发 rename 覆盖同一文件。
func benchWriteTo(dir string, counter *atomic.Int64) string {
	return filepath.Join(dir, fmt.Sprintf("bench-%d.json", counter.Add(1)))
}

// BenchmarkWrite 同步写入基准：4 workers，并行度 4。
// 运行示例：go test -bench=BenchmarkWrite -benchtime=1000x -run=XXX ./storage/file/
func BenchmarkWrite(b *testing.B) {
	w := NewAsyncFileWriter(4)
	defer func() { _ = w.Close() }()
	dir := b.TempDir()
	data := benchPayload()
	var counter atomic.Int64

	b.SetParallelism(4) // 并行度 4
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := w.Write(benchWriteTo(dir, &counter), data); err != nil {
				b.Error(err) // 从非测试协程报告失败，循环外统一终止
				return
			}
		}
	})
	b.StopTimer()
	if b.Failed() {
		b.Fatal("BenchmarkWrite 存在写入失败")
	}
	if stats := w.Stats(); stats["failed_writes"] != 0 {
		b.Fatalf("failed_writes = %d, want 0", stats["failed_writes"])
	}
}

// BenchmarkWriteAsync 异步写入基准：4 workers，并行度 4。
// 每次迭代提交任务后立即等待其 Done（允许流水线并行，通道缓冲为 1），
// 度量的是端到端提交-完成吞吐。
func BenchmarkWriteAsync(b *testing.B) {
	w := NewAsyncFileWriter(4)
	defer func() { _ = w.Close() }()
	dir := b.TempDir()
	data := benchPayload()
	var counter atomic.Int64

	b.SetParallelism(4) // 并行度 4
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			done := w.WriteAsync(benchWriteTo(dir, &counter), data)
			if err := <-done; err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.StopTimer()
	if b.Failed() {
		b.Fatal("BenchmarkWriteAsync 存在写入失败")
	}
	if stats := w.Stats(); stats["failed_writes"] != 0 {
		b.Fatalf("failed_writes = %d, want 0", stats["failed_writes"])
	}
}
