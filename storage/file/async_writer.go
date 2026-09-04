// Package file 提供基于文件系统的异步存储能力。
//
// AsyncFileWriter 通过固定数量的 worker 协程将写入任务并发落盘，
// 采用"临时文件 + rename"的原子写入策略，避免读者读到半截数据。
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultWorkers   = 4                // workers 参数非法（<=0）时的默认并发数
	defaultQueueSize = 1000             // 任务队列缓冲长度
	closeTimeout     = 30 * time.Second // Close 等待 worker 排空队列的超时上限
	tmpSuffix        = ".tmp"           // 原子写入使用的临时文件后缀
	dirPerm          = 0o755            // 自动创建父目录的权限
	filePerm         = 0o644            // 落盘文件的权限
)

// ErrWriterClosed 表示写入器已关闭，不再接受新的写入任务。
var ErrWriterClosed = errors.New("async file writer: closed")

// WriteTask 表示一次文件写入任务。
type WriteTask struct {
	Path string     // 目标文件路径
	Data []byte     // 待写入数据
	Done chan error // 结果通道：缓冲为 1，worker 写入结果后立即 close，避免泄漏
}

// AsyncFileWriter 异步文件写入器。
//
// 优雅关闭语义：Close 会先关闭入队闸门（不再接受新任务），随后 worker
// 持续消费队列直到排空，保证每一个已入队任务的 Done 都恰好被写入一次
// 并关闭，调用方永远不会因为关闭而永久阻塞。
type AsyncFileWriter struct {
	queue   chan *WriteTask // 任务队列（带缓冲）
	workers int             // worker 协程数量
	wg      sync.WaitGroup  // 等待所有 worker 退出

	ctx    context.Context    // 生命周期上下文（关闭信号，用于入队快速失败）
	cancel context.CancelFunc // 取消 ctx

	// enqMu 保护"入队发送"与"close(queue)"之间的竞态：
	// 持有该锁期间完成的入队发送必然发生在 close(queue) 之前，
	// 从而杜绝向已关闭 channel 发送导致的 panic。
	enqMu sync.Mutex
	// accepting 标识写入器是否仍接受新任务，Close 后置为 false。
	accepting atomic.Bool
	// closeOnce 保证关闭逻辑只执行一次（Close 幂等）。
	closeOnce sync.Once

	mu           sync.Mutex // 保护以下统计字段
	totalWrites  uint64     // 成功写入次数
	totalBytes   uint64     // 成功写入的字节总数
	failedWrites uint64     // 失败写入次数
}

// NewAsyncFileWriter 创建异步文件写入器并启动 worker 协程。
// workers <= 0 时使用默认值 4；任务队列缓冲固定为 1000。
func NewAsyncFileWriter(workers int) *AsyncFileWriter {
	if workers <= 0 {
		workers = defaultWorkers
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &AsyncFileWriter{
		queue:   make(chan *WriteTask, defaultQueueSize),
		workers: workers,
		ctx:     ctx,
		cancel:  cancel,
	}
	w.accepting.Store(true)
	for i := 0; i < workers; i++ {
		w.wg.Add(1)
		go w.worker()
	}
	return w
}

// Write 入队一个写入任务并阻塞等待其完成，返回写入结果。
// 写入器已关闭时立即返回 ErrWriterClosed。
func (w *AsyncFileWriter) Write(path string, data []byte) error {
	done, err := w.enqueue(path, data)
	if err != nil {
		return err
	}
	return <-done
}

// WriteAsync 入队一个写入任务并立即返回其 Done 通道（只读视图）。
// 调用方从通道中读取一次即得写入结果；通道会被 worker 关闭，可安全 range。
// 写入器已关闭时返回一个已携带 ErrWriterClosed 并关闭的通道，不会阻塞。
func (w *AsyncFileWriter) WriteAsync(path string, data []byte) <-chan error {
	done, err := w.enqueue(path, data)
	if err != nil {
		ch := make(chan error, 1)
		ch <- err
		close(ch)
		return ch
	}
	return done
}

// enqueue 将任务放入队列，返回任务的结果通道。
// 通过 enqMu + accepting 双重保护，保证不会向已关闭的 queue 发送。
func (w *AsyncFileWriter) enqueue(path string, data []byte) (chan error, error) {
	// 快速失败路径：已关闭时无需竞争锁
	select {
	case <-w.ctx.Done():
		return nil, ErrWriterClosed
	default:
	}

	w.enqMu.Lock()
	defer w.enqMu.Unlock()
	if !w.accepting.Load() {
		return nil, ErrWriterClosed
	}
	task := &WriteTask{
		Path: path,
		Data: data,
		Done: make(chan error, 1), // 缓冲为 1，worker 写入后 close，不会阻塞也不会泄漏
	}
	// 队列满时阻塞等待 worker 消费（Close 会先拿锁再关队列，因此此处发送安全）
	w.queue <- task
	return task.Done, nil
}

// Stats 返回写入统计快照。
// total_writes：成功写入次数；total_bytes：成功写入字节总数；failed_writes：失败次数。
func (w *AsyncFileWriter) Stats() map[string]uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]uint64{
		"total_writes":  w.totalWrites,
		"total_bytes":   w.totalBytes,
		"failed_writes": w.failedWrites,
	}
}

// Close 优雅关闭写入器：
//  1. 关闭入队闸门，此后 Write/WriteAsync 立即返回 ErrWriterClosed，绝不 panic；
//  2. 关闭队列，worker 继续排空已入队任务（所有 Done 恰好被写入一次并 close）；
//  3. 等待全部 worker 退出，超时（30s）则返回错误。
//
// Close 可安全重复调用（幂等）。
func (w *AsyncFileWriter) Close() error {
	w.closeOnce.Do(func() {
		// 必须先拒绝新任务，再关闭队列；两步都在锁内完成以消除竞态
		w.enqMu.Lock()
		w.accepting.Store(false)
		close(w.queue) // worker for-range 消费完剩余任务后自然退出
		w.enqMu.Unlock()
		w.cancel() // 触发入队快速失败路径
	})

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(closeTimeout):
		return fmt.Errorf("async file writer: close timed out after %v, workers still draining", closeTimeout)
	}
}

// worker 持续消费队列直到队列被关闭且排空，保证优雅关闭不丢任务。
func (w *AsyncFileWriter) worker() {
	defer w.wg.Done()
	for task := range w.queue {
		w.execute(task)
	}
}

// execute 执行单个写入任务，并保证 Done 恰好被写入一次且随后关闭
// （即使 writeAtomic 意外 panic 也不会让调用方永久阻塞）。
func (w *AsyncFileWriter) execute(task *WriteTask) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("async file writer: task panic: %v", r)
		}
		w.record(task.Data, err)
		task.Done <- err // 缓冲为 1，必不阻塞
		close(task.Done) // 关闭以通知"结果已产生"，避免接收方泄漏
	}()
	err = w.writeAtomic(task.Path, task.Data)
}

// writeAtomic 原子写入：先确保父目录存在，再写同目录唯一临时文件，
// 最后 rename 保证原子性；任一步失败都会清理临时文件。
func (w *AsyncFileWriter) writeAtomic(path string, data []byte) error {
	if path == "" {
		return errors.New("async file writer: path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("async file writer: mkdir %s: %w", dir, err)
	}
	base := filepath.Base(path)
	tmpFile, err := os.CreateTemp(dir, "."+base+"-*"+tmpSuffix)
	if err != nil {
		return fmt.Errorf("async file writer: create tmp in %s: %w", dir, err)
	}
	tmp := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(filePerm); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("async file writer: chmod tmp %s: %w", tmp, err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("async file writer: write tmp %s: %w", tmp, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("async file writer: close tmp %s: %w", tmp, err)
	}
	// 同目录 rename，保证原子替换；唯一临时文件允许同一目标并发写入而不互相覆盖。
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("async file writer: rename %s -> %s: %w", tmp, path, err)
	}
	cleanup = false
	return nil
}

// record 更新写入统计。
func (w *AsyncFileWriter) record(data []byte, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.failedWrites++
		return
	}
	w.totalWrites++
	w.totalBytes += uint64(len(data))
}
