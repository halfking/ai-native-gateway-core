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
	degradation   DegradationConfig
	failures      []time.Time
	degraded      bool
	writeSuccess  uint64
	writeFailures uint64
	queueFull     uint64
	failureEvents []time.Time
	successEvents []time.Time
}

// DegradationConfig controls optional audit degradation notifications.
type DegradationConfig struct {
	FailureWindow time.Duration
	FailureRate   float64
	OnAlert       func(Alert)
}

type Alert struct {
	Kind        string
	FailureRate float64
	QueueSize   int
}

type BatchWriterStats struct {
	Writes      uint64
	Failures    uint64
	QueueFull   uint64
	FailureRate float64
}

type BatchWriterOption func(*BatchWriter)

func WithDegradationConfig(cfg DegradationConfig) BatchWriterOption {
	return func(w *BatchWriter) {
		if cfg.FailureWindow <= 0 {
			cfg.FailureWindow = 5 * time.Minute
		}
		if cfg.FailureRate <= 0 {
			cfg.FailureRate = .05
		}
		w.degradation = cfg
	}
}

// NewBatchWriter 创建批量写入器。
// flushSize: 缓冲区满 N 个事件触发 flush。
// flushInterval: 定时 flush 间隔。
func NewBatchWriter(sink Sink, flushSize int, flushInterval time.Duration, opts ...BatchWriterOption) *BatchWriter {
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
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
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
	before := len(w.buffer)
	w.trimPendingLocked()
	var alert *Alert
	if before > w.maxPending {
		w.queueFull++
		if w.degradation.OnAlert != nil {
			alert = &Alert{Kind: "audit_queue_full", QueueSize: len(w.buffer)}
		}
	}
	shouldFlush := len(w.buffer) >= w.flushSize
	w.mu.Unlock()
	if alert != nil {
		w.degradation.OnAlert(*alert)
	}

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
		w.writeFailures++
		now := time.Now()
		w.failureEvents = append(w.failureEvents, now)
		w.trimFailureEventsLocked(now)
		w.buffer = append(events, w.buffer...)
		w.trimPendingLocked()
		alert := w.degradationAlertLocked()
		w.mu.Unlock()
		if alert != nil && w.degradation.OnAlert != nil {
			w.degradation.OnAlert(*alert)
		}
		slog.Warn("audit batch write failed; events queued for retry", "error", err, "pending", w.BufferedCount())
		return fmt.Errorf("audit batch write: %w", err)
	}
	w.mu.Lock()
	w.writeSuccess++
	now := time.Now()
	w.successEvents = append(w.successEvents, now)
	w.trimFailureEventsLocked(now)
	w.mu.Unlock()
	return nil
}

func (w *BatchWriter) trimFailureEventsLocked(now time.Time) {
	window := w.degradation.FailureWindow
	if window <= 0 {
		window = 5 * time.Minute
	}
	cutoff := now.Add(-window)
	trim := func(events []time.Time) []time.Time {
		i := 0
		for i < len(events) && events[i].Before(cutoff) {
			i++
		}
		return events[i:]
	}
	w.failureEvents = trim(w.failureEvents)
	w.successEvents = trim(w.successEvents)
}

func (w *BatchWriter) degradationAlertLocked() *Alert {
	if w.degradation.OnAlert == nil {
		return nil
	}
	if len(w.buffer) >= w.maxPending {
		return &Alert{Kind: "audit_queue_full", QueueSize: len(w.buffer)}
	}
	total := len(w.failureEvents) + len(w.successEvents)
	if total == 0 {
		return nil
	}
	rate := float64(len(w.failureEvents)) / float64(total)
	threshold := w.degradation.FailureRate
	if threshold <= 0 {
		threshold = .05
	}
	if rate > threshold && !w.degraded {
		w.degraded = true
		return &Alert{Kind: "audit_write_degraded", FailureRate: rate, QueueSize: len(w.buffer)}
	}
	if rate <= threshold {
		w.degraded = false
	}
	return nil
}

func (w *BatchWriter) Stats() BatchWriterStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.trimFailureEventsLocked(now)
	total := len(w.failureEvents) + len(w.successEvents)
	rate := float64(0)
	if total > 0 {
		rate = float64(len(w.failureEvents)) / float64(total)
	}
	return BatchWriterStats{Writes: w.writeSuccess, Failures: w.writeFailures, QueueFull: w.queueFull, FailureRate: rate}
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
