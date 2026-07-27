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
	stateMu  sync.RWMutex
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
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()
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
		q.stateMu.Lock()
		defer q.stateMu.Unlock()
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
	done       chan struct{}
	closed     atomic.Bool
	stateMu    sync.RWMutex
	closeOnce  sync.Once
	closeErr   error
	// lastDropWarn 上次队列满告警的 UnixNano，用于限流（见 noteDroppedEntry）
	lastDropWarn atomic.Int64
}

// dropWarnInterval 队列满告警的最小间隔
const dropWarnInterval = 10 * time.Second

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
		done:       make(chan struct{}),
	}

	// 启动后台写入协程
	go logger.flushWorker()

	return logger, nil
}

// LogClientRequest 异步记录客户端请求
func (l *AsyncRawDataLogger) LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string) {
	l.LogClientRequestWithEnvelope(requestID, protocol, body, headers, conversionStep, RawCorrelationEnvelope{})
}

// LogClientRequestWithEnvelope is the correlation-aware counterpart of
// LogClientRequest. Operators can attach session, task, provider, and
// trace identifiers so the audit log can be joined with request_logs
// without an external index.
func (l *AsyncRawDataLogger) LogClientRequestWithEnvelope(
	requestID, protocol string,
	body []byte,
	headers map[string]string,
	conversionStep string,
	env RawCorrelationEnvelope,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := l.makeEntry("client_request", requestID, protocol, body, headers, conversionStep, env)
	if !l.queue.Enqueue(entry) {
		// 2026-07-27: A dropped raw entry is no longer silent. Emit a
		// stub entry that records the queue overflow plus the
		// correlation envelope so the operator can still join with
		// request_logs and the anomaly endpoint can dedupe across
		// processes.
		overflow := RawDataEntry{
			Timestamp:        time.Now(),
			RequestID:        requestID,
			Direction:        "overflow",
			Protocol:         protocol,
			DataSize:         len(body),
			Headers:          headers,
			ConversionStep:   conversionStep,
			ClientRequestID:  env.ClientRequestID,
			GWSessionID:      env.GWSessionID,
			GWTaskID:         env.GWTaskID,
			ParentRequestID:  env.ParentRequestID,
			TenantID:         env.TenantID,
			ApplicationID:    env.ApplicationID,
			APIKeyID:         env.APIKeyID,
			ProviderID:       env.ProviderID,
			CredentialID:     env.CredentialID,
			AttemptNo:        env.AttemptNo,
			UpstreamEndpoint: env.UpstreamEndpoint,
			TraceID:          env.TraceID,
			SpanID:           env.SpanID,
			Error:            "raw_log_queue_full",
		}
		if !l.queue.Enqueue(overflow) {
			l.noteDroppedEntry(requestID, "client_request")
		}
	}
}

// makeEntry builds a RawDataEntry from the supplied envelope, computing
// the SHA-256 of the body so the raw log can be verified against the
// request_logs row and the anomaly endpoint.
func (l *AsyncRawDataLogger) makeEntry(
	direction, requestID, protocol string,
	body []byte,
	headers map[string]string,
	conversionStep string,
	env RawCorrelationEnvelope,
) RawDataEntry {
	entry := RawDataEntry{
		Timestamp:        time.Now(),
		RequestID:        requestID,
		Direction:        direction,
		Protocol:         protocol,
		DataSize:         len(body),
		RawData:          encodeRawData(body),
		RawDataEncoding:  "base64",
		Headers:          headers,
		ConversionStep:   conversionStep,
		ClientRequestID:  env.ClientRequestID,
		GWSessionID:      env.GWSessionID,
		GWTaskID:         env.GWTaskID,
		ParentRequestID:  env.ParentRequestID,
		TenantID:         env.TenantID,
		ApplicationID:    env.ApplicationID,
		APIKeyID:         env.APIKeyID,
		ProviderID:       env.ProviderID,
		CredentialID:     env.CredentialID,
		AttemptNo:        env.AttemptNo,
		UpstreamEndpoint: env.UpstreamEndpoint,
		TraceID:          env.TraceID,
		SpanID:           env.SpanID,
		ChunkIndex:       env.ChunkIndex,
	}
	if len(body) > 0 {
		entry.SHA256 = hashBytes(body)
	}
	return entry
}

// RawCorrelationEnvelope carries the request correlation fields used by
// the audit log writer. All fields are optional; missing values fall
// back to the legacy (request_id, direction) lookup. Producer sites
// (executor / streaming bridges) populate the envelope from the
// request context so every raw entry can be joined with request_logs.
type RawCorrelationEnvelope struct {
	ClientRequestID  string
	GWSessionID      string
	GWTaskID         string
	ParentRequestID  string
	TenantID         string
	ApplicationID    string
	APIKeyID         int
	ProviderID       int
	CredentialID     int
	AttemptNo        int
	ChunkIndex       int
	UpstreamEndpoint string
	TraceID          string
	SpanID           string
}

// noteDroppedEntry 记录一次入队失败。
// 丢弃发生在流式请求的 goroutine 上，每帧一条 slog.Warn 会把"尽力而为"的
// 日志变成热路径开销，因此按时间窗口聚合，只在窗口内首次丢弃时输出一次，
// 并带上累计丢弃总数。
func (l *AsyncRawDataLogger) noteDroppedEntry(requestID, direction string) {
	now := time.Now().UnixNano()
	last := l.lastDropWarn.Load()
	if now-last < int64(dropWarnInterval) {
		return
	}
	if !l.lastDropWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("async_raw_logger: queue full, dropping log entries",
		"request_id", requestID,
		"direction", direction,
		"dropped_total", l.queue.Stats().DropCount)
}

// LogUpstreamRequest 异步记录上游请求
func (l *AsyncRawDataLogger) LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string) {
	l.LogUpstreamRequestWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

// LogUpstreamRequestWithEnvelope is the correlation-aware counterpart of
// LogUpstreamRequest. Used by the streaming bridges to attribute each
// upstream request to a specific provider, credential, attempt, and
// chunk sequence.
func (l *AsyncRawDataLogger) LogUpstreamRequestWithEnvelope(
	requestID, protocol string,
	body []byte,
	conversionStep string,
	env RawCorrelationEnvelope,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := l.makeEntry("upstream_request", requestID, protocol, body, nil, conversionStep, env)
	if !l.queue.Enqueue(entry) {
		overflow := l.makeOverflowEntry("upstream_request", requestID, protocol, conversionStep, env)
		if !l.queue.Enqueue(overflow) {
			l.noteDroppedEntry(requestID, "upstream_request")
		}
	}
}

// LogUpstreamResponse 异步记录上游响应
func (l *AsyncRawDataLogger) LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string) {
	l.LogUpstreamResponseWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

// LogUpstreamResponseWithEnvelope is the correlation-aware counterpart
// of LogUpstreamResponse.
func (l *AsyncRawDataLogger) LogUpstreamResponseWithEnvelope(
	requestID, protocol string,
	body []byte,
	conversionStep string,
	env RawCorrelationEnvelope,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := l.makeEntry("upstream_response", requestID, protocol, body, nil, conversionStep, env)
	if !l.queue.Enqueue(entry) {
		overflow := l.makeOverflowEntry("upstream_response", requestID, protocol, conversionStep, env)
		if !l.queue.Enqueue(overflow) {
			l.noteDroppedEntry(requestID, "upstream_response")
		}
	}
}

// LogClientResponse 异步记录客户端响应
func (l *AsyncRawDataLogger) LogClientResponse(requestID, protocol string, body []byte, conversionStep string) {
	l.LogClientResponseWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

// LogClientResponseWithEnvelope is the correlation-aware counterpart of
// LogClientResponse. Used to capture the exact wire bytes the gateway
// sent to the client (non-streaming or stream-end), which previously
// were only reconstructed from chunks.
func (l *AsyncRawDataLogger) LogClientResponseWithEnvelope(
	requestID, protocol string,
	body []byte,
	conversionStep string,
	env RawCorrelationEnvelope,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := l.makeEntry("client_response", requestID, protocol, body, nil, conversionStep, env)
	if !l.queue.Enqueue(entry) {
		overflow := l.makeOverflowEntry("client_response", requestID, protocol, conversionStep, env)
		if !l.queue.Enqueue(overflow) {
			l.noteDroppedEntry(requestID, "client_response")
		}
	}
}

// LogConversionError 异步记录转换错误
func (l *AsyncRawDataLogger) LogConversionError(requestID, protocol, direction, step string, body []byte, err error) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return
	}

	entry := l.makeEntry(direction, requestID, protocol, body, nil, step, RawCorrelationEnvelope{})
	entry.Error = err.Error()
	if !l.queue.Enqueue(entry) {
		overflow := l.makeOverflowEntry(direction, requestID, protocol, step, RawCorrelationEnvelope{})
		overflow.Error = err.Error()
		if !l.queue.Enqueue(overflow) {
			l.noteDroppedEntry(requestID, "error")
		}
	}
}

func (l *AsyncRawDataLogger) makeOverflowEntry(direction, requestID, protocol, conversionStep string, env RawCorrelationEnvelope) RawDataEntry {
	return RawDataEntry{
		Timestamp:        time.Now(),
		RequestID:        requestID,
		Direction:        "overflow",
		Protocol:         protocol,
		DataSize:         0,
		ConversionStep:   conversionStep,
		ClientRequestID:  env.ClientRequestID,
		GWSessionID:      env.GWSessionID,
		GWTaskID:         env.GWTaskID,
		ParentRequestID:  env.ParentRequestID,
		TenantID:         env.TenantID,
		ApplicationID:    env.ApplicationID,
		APIKeyID:         env.APIKeyID,
		ProviderID:       env.ProviderID,
		CredentialID:     env.CredentialID,
		AttemptNo:        env.AttemptNo,
		UpstreamEndpoint: env.UpstreamEndpoint,
		TraceID:          env.TraceID,
		SpanID:           env.SpanID,
		Error:            "raw_log_queue_full:" + direction,
	}
}

// flushWorker 后台刷新协程
func (l *AsyncRawDataLogger) flushWorker() {
	defer close(l.done)
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

// flushBatch 批量刷新日志。
// 单次 tick 会持续排空队列，而非只取一个批次：此前每 100ms 最多写出
// batchSize(50) 条，全进程上限约 500 条/秒，而每个 SSE 帧就会产生一条记录，
// 少量并发流即可打满 10000 长度的队列并开始丢弃。
func (l *AsyncRawDataLogger) flushBatch() {
	for {
		entries := l.queue.TryDequeueBatch(l.batchSize)
		if len(entries) == 0 {
			return
		}
		l.baseLogger.writeEntries(entries)
		if len(entries) < l.batchSize {
			return
		}
	}
}

// Close 关闭异步日志记录器
func (l *AsyncRawDataLogger) Close() error {
	l.closeOnce.Do(func() {
		l.stateMu.Lock()
		l.closed.Store(true)
		l.stateMu.Unlock()
		l.cancel()
		<-l.done
		for l.queue.Size() > 0 {
			l.flushBatch()
		}
		if l.baseLogger != nil {
			l.closeErr = l.baseLogger.Close()
		}
	})
	return l.closeErr
}

// Stats 返回队列统计信息
func (l *AsyncRawDataLogger) Stats() QueueStats {
	return l.queue.Stats()
}

// CurrentLocation forwards to the underlying RawDataLogger and reports
// the path and byte offset of the next entry to be written. See
// RawDataLogger.CurrentLocation for semantics. Returns empty values
// when the underlying logger is disabled or closed.
//
// 2026-07-28: used by LockFreeAnomalyReporter to populate
// AnomalyReport.RawLogFile/RawLogOffset. The async wrapper does not
// snapshot the queue state; callers should expect the returned offset
// to be the position of the most recently *flushed* entry, not the most
// recently enqueued one. For most anomaly types this is acceptable
// because the relevant upstream_request / upstream_response entry has
// already been drained from the queue by the time the executor surfaces
// the anomaly.
func (l *AsyncRawDataLogger) CurrentLocation() (file string, offset int64) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return "", 0
	}
	return l.baseLogger.CurrentLocation()
}

// HasFile reports whether the underlying RawDataLogger has an open
// audit log file. See RawDataLogger.HasFile.
func (l *AsyncRawDataLogger) HasFile() bool {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed.Load() || l.baseLogger == nil || !l.baseLogger.enabled {
		return false
	}
	return l.baseLogger.HasFile()
}
