package audit

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const defaultPendingCapacity = 1000

// BatchWriter 异步批量写入器。
// Failed writes stay in the bounded pending queue and are retried by the next
// flush. When the queue is full, the oldest events are evicted so the newest
// audit events remain available for a later retry/Close error inspection.
type BatchWriter struct {
	sink          Sink
	buffer        []*Event
	flushCh       chan struct{}
	flushSize     int
	flushInterval time.Duration
	maxPending    int
	mu            sync.Mutex
	done          chan struct{}
	workerDone    chan struct{}
	closed        bool
	closeDone     chan struct{}
	closeErr      error
}

// NewBatchWriter 创建批量写入器。
// flushSize: 缓冲区满 N 个事件触发 flush。
// flushInterval: 定时 flush 间隔。
func NewBatchWriter(sink Sink, flushSize int, flushInterval time.Duration) *BatchWriter {
	if flushSize <= 0 {
		flushSize = 100
	}
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}
	maxPending := defaultPendingCapacity
	if flushSize > maxPending {
		maxPending = flushSize
	}
	w := &BatchWriter{
		sink:          sink,
		buffer:        make([]*Event, 0, flushSize),
		flushCh:       make(chan struct{}, 1),
		flushSize:     flushSize,
		flushInterval: flushInterval,
		maxPending:    maxPending,
		done:          make(chan struct{}),
		workerDone:    make(chan struct{}),
		closeDone:     make(chan struct{}),
	}
	go w.run()
	return w
}

// Append 添加事件（非阻塞，达到 flushSize 触发 flush）。
func (w *BatchWriter) Append(event *Event) {
	if event == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.buffer = append(w.buffer, event)
	w.trimPendingLocked()
	shouldFlush := len(w.buffer) >= w.flushSize
	w.mu.Unlock()

	if shouldFlush {
		select {
		case w.flushCh <- struct{}{}:
		default:
		}
	}
}

// Close 刷新剩余事件并退出。
func (w *BatchWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		closeDone := w.closeDone
		w.mu.Unlock()
		<-closeDone
		return w.closeErr
	}
	w.closed = true
	close(w.done)
	w.mu.Unlock()

	<-w.workerDone
	writeErr := w.flush()
	closeErr := w.sink.Close()
	w.mu.Lock()
	w.closeErr = errors.Join(writeErr, closeErr, w.pendingErrorLocked())
	close(w.closeDone)
	result := w.closeErr
	w.mu.Unlock()
	return result
}

func (w *BatchWriter) run() {
	defer close(w.workerDone)
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-w.flushCh:
			_ = w.flush()
		case <-ticker.C:
			_ = w.flush()
		}
	}
}

func (w *BatchWriter) flush() error {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return nil
	}
	events := w.buffer
	w.buffer = make([]*Event, 0, w.flushSize)
	w.mu.Unlock()

	if err := w.sink.Write(events); err != nil {
		w.mu.Lock()
		w.buffer = append(events, w.buffer...)
		w.trimPendingLocked()
		w.mu.Unlock()
		slog.Warn("audit batch write failed; events queued for retry", "error", err, "pending", w.BufferedCount())
		return fmt.Errorf("audit batch write: %w", err)
	}
	return nil
}

func (w *BatchWriter) trimPendingLocked() {
	if len(w.buffer) <= w.maxPending {
		return
	}
	dropped := len(w.buffer) - w.maxPending
	copy(w.buffer, w.buffer[dropped:])
	w.buffer = w.buffer[:w.maxPending]
}

func (w *BatchWriter) pendingErrorLocked() error {
	if len(w.buffer) == 0 {
		return nil
	}
	return fmt.Errorf("audit writer closed with %d pending events", len(w.buffer))
}

// BufferedCount 返回当前缓冲区中的事件数（测试用）。
func (w *BatchWriter) BufferedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buffer)
}
