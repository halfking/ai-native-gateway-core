package logging

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// LockFreeQueue 无锁队列，用于异步日志和异常报告
// 使用有界 channel 保证多生产者/多消费者的发布与回收顺序安全。
type LockFreeQueue[T any] struct {
	queue    chan T
	capacity uint64
	closed   atomic.Bool
	closeMu  sync.Once

	// 统计信息
	enqueueCount atomic.Uint64
	dequeueCount atomic.Uint64
	dropCount    atomic.Uint64
}

// NewLockFreeQueue 创建有界并发队列。
// capacity <= 0 时使用默认容量；保留此 API 名称以兼容现有调用方。
func NewLockFreeQueue[T any](capacity int) *LockFreeQueue[T] {
	if capacity <= 0 {
		capacity = 10000
	}
	return &LockFreeQueue[T]{
		queue:    make(chan T, capacity),
		capacity: uint64(capacity),
	}
}

// Enqueue 入队（非阻塞）。队列满或已关闭时丢弃并返回 false。
func (q *LockFreeQueue[T]) Enqueue(item T) bool {
	if q.closed.Load() {
		q.dropCount.Add(1)
		return false
	}
	select {
	case q.queue <- item:
		q.enqueueCount.Add(1)
		return true
	default:
		q.dropCount.Add(1)
		return false
	}
}

// Dequeue 出队（非阻塞）。
func (q *LockFreeQueue[T]) Dequeue() (T, bool) {
	var zero T
	select {
	case item, ok := <-q.queue:
		if !ok {
			return zero, false
		}
		q.dequeueCount.Add(1)
		return item, true
	default:
		return zero, false
	}
}

// TryDequeueBatch 批量出队（非阻塞）。
func (q *LockFreeQueue[T]) TryDequeueBatch(maxCount int) []T {
	if maxCount <= 0 {
		return nil
	}
	result := make([]T, 0, maxCount)
	for len(result) < maxCount {
		item, ok := q.Dequeue()
		if !ok {
			break
		}
		result = append(result, item)
	}
	return result
}

// Size 返回当前队列大小。
func (q *LockFreeQueue[T]) Size() int64 {
	return int64(len(q.queue))
}

// IsClosed 返回队列是否已关闭。
func (q *LockFreeQueue[T]) IsClosed() bool {
	return q.closed.Load()
}

// Close 关闭队列；关闭后仍允许消费者排空已有元素。
func (q *LockFreeQueue[T]) Close() {
	q.closeMu.Do(func() {
		q.closed.Store(true)
		close(q.queue)
	})
}

// Stats 返回队列统计信息。
func (q *LockFreeQueue[T]) Stats() QueueStats {
	return QueueStats{
		Size:         int64(len(q.queue)),
		Capacity:     int64(q.capacity),
		EnqueueCount: q.enqueueCount.Load(),
		DequeueCount: q.dequeueCount.Load(),
		DropCount:    q.dropCount.Load(),
	}
}

// QueueStats 队列统计信息
type QueueStats struct {
	Size         int64  // 当前大小
	Capacity     int64  // 容量
	EnqueueCount uint64 // 入队总数
	DequeueCount uint64 // 出队总数
	DropCount    uint64 // 丢弃总数
}

// AsyncRawDataLogger 异步原始数据日志记录器
// 使用无锁队列替代同步写入，提升性能
type AsyncRawDataLogger struct {
	baseLogger *RawDataLogger
	queue      *LockFreeQueue[RawDataEntry]
	ctx        context.Context
	cancel     context.CancelFunc
	batchSize  int
	flushDelay time.Duration
}

// NewAsyncRawDataLogger 创建异步日志记录器
func NewAsyncRawDataLogger(baseDir string, maxSize int64, enabled bool, queueSize int) (*AsyncRawDataLogger, error) {
	baseLogger, err := NewRawDataLogger(baseDir, maxSize, enabled)
	if err != nil {
		return nil, err
	}

	if queueSize <= 0 {
		queueSize = 10000
	}

	ctx, cancel := context.WithCancel(context.Background())

	logger := &AsyncRawDataLogger{
		baseLogger: baseLogger,
		queue:      NewLockFreeQueue[RawDataEntry](queueSize),
		ctx:        ctx,
		cancel:     cancel,
		batchSize:  50,
		flushDelay: 100 * time.Millisecond,
	}

	// 启动后台写入协程
	go logger.flushWorker()

	return logger, nil
}

// LogClientRequest 异步记录客户端请求
func (l *AsyncRawDataLogger) LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string) {
	if l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		Direction:      "client_request",
		Protocol:       protocol,
		DataSize:       len(body),
		RawData:        encodeRawData(body),
		RawDataEncoding: "base64",
		Headers:        headers,
		ConversionStep: conversionStep,
	}

	if !l.queue.Enqueue(entry) {
		slog.Warn("async_raw_logger: queue full, dropping log entry",
			"request_id", requestID,
			"direction", "client_request")
	}
}

// LogUpstreamRequest 异步记录上游请求
func (l *AsyncRawDataLogger) LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string) {
	if l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		Direction:      "upstream_request",
		Protocol:       protocol,
		DataSize:       len(body),
		RawData:        encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep: conversionStep,
	}

	if !l.queue.Enqueue(entry) {
		slog.Warn("async_raw_logger: queue full, dropping log entry",
			"request_id", requestID,
			"direction", "upstream_request")
	}
}

// LogUpstreamResponse 异步记录上游响应
func (l *AsyncRawDataLogger) LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string) {
	if l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		Direction:      "upstream_response",
		Protocol:       protocol,
		DataSize:       len(body),
		RawData:        encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep: conversionStep,
	}

	if !l.queue.Enqueue(entry) {
		slog.Warn("async_raw_logger: queue full, dropping log entry",
			"request_id", requestID,
			"direction", "upstream_response")
	}
}

// LogClientResponse 异步记录客户端响应
func (l *AsyncRawDataLogger) LogClientResponse(requestID, protocol string, body []byte, conversionStep string) {
	if l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		Direction:      "client_response",
		Protocol:       protocol,
		DataSize:       len(body),
		RawData:        encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep: conversionStep,
	}

	if !l.queue.Enqueue(entry) {
		slog.Warn("async_raw_logger: queue full, dropping log entry",
			"request_id", requestID,
			"direction", "client_response")
	}
}

// LogConversionError 异步记录转换错误
func (l *AsyncRawDataLogger) LogConversionError(requestID, protocol, direction, step string, body []byte, err error) {
	if l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		Direction:      direction,
		Protocol:       protocol,
		DataSize:       len(body),
		RawData:        encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep: step,
		Error:          err.Error(),
	}

	if !l.queue.Enqueue(entry) {
		slog.Warn("async_raw_logger: queue full, dropping error log",
			"request_id", requestID,
			"error", err)
	}
}

// flushWorker 后台刷新协程
func (l *AsyncRawDataLogger) flushWorker() {
	ticker := time.NewTicker(l.flushDelay)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctx.Done():
			// 关闭前刷新剩余日志
			l.flushBatch()
			return
		case <-ticker.C:
			l.flushBatch()
		}
	}
}

// flushBatch 批量刷新日志
func (l *AsyncRawDataLogger) flushBatch() {
	entries := l.queue.TryDequeueBatch(l.batchSize)
	if len(entries) == 0 {
		return
	}

	// 批量写入基础日志记录器
	for _, entry := range entries {
		l.baseLogger.writeEntry(entry)
	}
}

// Close 关闭异步日志记录器
func (l *AsyncRawDataLogger) Close() error {
	// 停止后台协程
	l.cancel()

	// 等待队列清空（最多等待5秒）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && l.queue.Size() > 0 {
		l.flushBatch()
		time.Sleep(10 * time.Millisecond)
	}

	// 关闭基础日志记录器
	if l.baseLogger != nil {
		return l.baseLogger.Close()
	}

	return nil
}

// Stats 返回队列统计信息
func (l *AsyncRawDataLogger) Stats() QueueStats {
	return l.queue.Stats()
}
