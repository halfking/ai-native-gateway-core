package audit

import (
	"sync"
	"time"
)

// BatchWriter 异步批量写入器
type BatchWriter struct {
	sink          Sink
	buffer        []*Event
	flushCh       chan struct{}
	flushSize     int
	flushInterval time.Duration
	mu            sync.Mutex
	done          chan struct{}
	closed        bool
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
	w := &BatchWriter{
		sink:          sink,
		buffer:        make([]*Event, 0, flushSize),
		flushCh:       make(chan struct{}, 1),
		flushSize:     flushSize,
		flushInterval: flushInterval,
		done:          make(chan struct{}),
	}
	go w.run()
	return w
}

// Append 添加事件（非阻塞，达到 flushSize 触发 flush）
func (w *BatchWriter) Append(event *Event) {
	w.mu.Lock()
	w.buffer = append(w.buffer, event)
	shouldFlush := len(w.buffer) >= w.flushSize
	w.mu.Unlock()

	if shouldFlush {
		select {
		case w.flushCh <- struct{}{}:
		default:
		}
	}
}

// Close 刷新剩余事件并退出
func (w *BatchWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()

	w.flush() // 最后一次 flush
	close(w.done)
	return w.sink.Close()
}

func (w *BatchWriter) run() {
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-w.flushCh:
			w.flush()
		case <-ticker.C:
			w.flush()
		}
	}
}

func (w *BatchWriter) flush() {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return
	}
	events := w.buffer
	w.buffer = make([]*Event, 0, w.flushSize)
	w.mu.Unlock()
	_ = w.sink.Write(events) // 错误吞掉（生产可加 retry/DLQ）
}

// BufferedCount 返回当前缓冲区中的事件数（测试用）
func (w *BatchWriter) BufferedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buffer)
}
