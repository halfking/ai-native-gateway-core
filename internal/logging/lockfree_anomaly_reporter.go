package logging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// LockFreeAnomalyReporter 无锁异常报告器
// 使用无锁队列替代互斥锁，提升高并发性能
type LockFreeAnomalyReporter struct {
	endpoint   string
	httpClient *http.Client
	queue      *LockFreeQueue[AnomalyReport]
	enabled    atomic.Bool
	ctx        context.Context
	cancel     context.CancelFunc
	batchSize  int
	flushDelay time.Duration
	closeOnce  sync.Once
	submitMu   sync.RWMutex
	done       chan struct{}
	batchMu    sync.Mutex
	flushMu    sync.Mutex
	closing    atomic.Bool

	// 统计信息
	reportsSent   atomic.Uint64
	reportsFailed atomic.Uint64
}

// NewLockFreeAnomalyReporter 创建无锁异常报告器
func NewLockFreeAnomalyReporter(endpoint string, enabled bool, queueSize int) *LockFreeAnomalyReporter {
	if queueSize <= 0 {
		queueSize = 1000
	}

	ctx, cancel := context.WithCancel(context.Background())

	reporter := &LockFreeAnomalyReporter{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue:      NewLockFreeQueue[AnomalyReport](queueSize),
		ctx:        ctx,
		cancel:     cancel,
		batchSize:  10,
		flushDelay: 5 * time.Second,
		done:       make(chan struct{}),
	}

	reporter.enabled.Store(enabled && endpoint != "")

	if reporter.enabled.Load() {
		go reporter.sendWorker()
	} else {
		close(reporter.done)
	}

	return reporter
}

// ReportToolCallsMissing 报告工具调用丢失异常
func (r *LockFreeAnomalyReporter) ReportToolCallsMissing(
	ctx context.Context,
	requestID string,
	sourceProto, targetProto string,
	rawInput, rawOutput []byte,
	missingToolCalls string,
	confidence float64,
) {
	if !r.enabled.Load() {
		return
	}

	report := AnomalyReport{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		AnomalyType:    "tool_calls_missing",
		SourceProtocol: sourceProto,
		TargetProtocol: targetProto,
		ConversionStep: "parse_or_serialize",
		RawInputHash:   hashString(string(rawInput)),
		RawInputSize:   len(rawInput),
		RawOutputHash:  hashString(string(rawOutput)),
		RawOutputSize:  len(rawOutput),
		Confidence:     confidence,
		Analysis: map[string]string{
			"suspected_cause":      "IR conversion dropped tool_calls field",
			"missing_tool_calls":   truncate(missingToolCalls, 1000),
			"recommended_action":   "Check IR ParseXXX/SerializeXXX functions for tool_calls handling",
			"upstream_has_tools":   "true",
			"downstream_has_tools": "false",
		},
	}
	envelope := envelopeFromContext(ctx)
	if envelope != nil {
		report.GWSessionID = envelope.GWSessionID
		report.ClientRequestID = envelope.ClientRequestID
		report.ProviderID = envelope.ProviderID
		report.CredentialID = envelope.CredentialID
		report.TraceID = envelope.TraceID
	}

	if !r.enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping report",
			"request_id", requestID,
			"anomaly_type", "tool_calls_missing")
	}
}

// ReportConversionError 报告转换错误异常
func (r *LockFreeAnomalyReporter) ReportConversionError(
	ctx context.Context,
	requestID string,
	sourceProto, targetProto, step string,
	rawInput []byte,
	err error,
) {
	if !r.enabled.Load() {
		return
	}

	report := AnomalyReport{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		AnomalyType:    "conversion_error",
		SourceProtocol: sourceProto,
		TargetProtocol: targetProto,
		ConversionStep: step,
		RawInputHash:   hashString(string(rawInput)),
		RawInputSize:   len(rawInput),
		ErrorMessage:   err.Error(),
		Confidence:     1.0,
		Analysis: map[string]string{
			"error_type":         fmt.Sprintf("%T", err),
			"suspected_cause":    "Malformed input or unsupported field",
			"recommended_action": "Review parse/serialize logic for this protocol combination",
		},
	}
	envelope := envelopeFromContext(ctx)
	if envelope != nil {
		report.GWSessionID = envelope.GWSessionID
		report.ClientRequestID = envelope.ClientRequestID
		report.ProviderID = envelope.ProviderID
		report.CredentialID = envelope.CredentialID
		report.TraceID = envelope.TraceID
	}

	if !r.enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping report",
			"request_id", requestID,
			"anomaly_type", "conversion_error")
	}
}

// ReportSemanticIncomplete 报告语义不完整异常
func (r *LockFreeAnomalyReporter) ReportSemanticIncomplete(
	ctx context.Context,
	requestID string,
	protocol string,
	rawOutput []byte,
	reason string,
	indicators []string,
	confidence float64,
) {
	if !r.enabled.Load() {
		return
	}

	indicatorsStr := ""
	if len(indicators) > 0 {
		indicatorsStr = fmt.Sprintf("%v", indicators)
	}

	report := AnomalyReport{
		Timestamp:      time.Now(),
		RequestID:      requestID,
		AnomalyType:    "semantic_incomplete",
		TargetProtocol: protocol,
		ConversionStep: "post_serialize",
		RawOutputHash:  hashString(string(rawOutput)),
		RawOutputSize:  len(rawOutput),
		Confidence:     confidence,
		Analysis: map[string]string{
			"reason":             reason,
			"indicators":         indicatorsStr,
			"suspected_cause":    "Response stopped prematurely or tool_calls were dropped",
			"recommended_action": "Compare raw upstream response with IR output to identify loss point",
		},
	}
	envelope := envelopeFromContext(ctx)
	if envelope != nil {
		report.GWSessionID = envelope.GWSessionID
		report.ClientRequestID = envelope.ClientRequestID
		report.ProviderID = envelope.ProviderID
		report.CredentialID = envelope.CredentialID
		report.TraceID = envelope.TraceID
	}

	if !r.enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping report",
			"request_id", requestID,
			"anomaly_type", "semantic_incomplete")
	}
}

func (r *LockFreeAnomalyReporter) enqueue(report AnomalyReport) bool {
	r.submitMu.RLock()
	defer r.submitMu.RUnlock()
	if !r.enabled.Load() {
		return false
	}
	return r.queue.Enqueue(report)
}

// sendWorker 后台发送协程
func (r *LockFreeAnomalyReporter) sendWorker() {
	defer close(r.done)
	ticker := time.NewTicker(r.flushDelay)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.sendBatch()
		}
	}
}

// sendBatch 批量发送异常报告（失败时重新入队一次）
func (r *LockFreeAnomalyReporter) sendBatch() bool {
	return r.sendBatchContext(context.Background())
}

func (r *LockFreeAnomalyReporter) sendBatchContext(ctx context.Context) bool {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	// 只在出队/重新入队时持有 batchMu,HTTP POST 不在锁内执行,
	// 避免卡住的异常端点阻塞 Close()/并发 flush。
	r.batchMu.Lock()
	batch := r.queue.TryDequeueBatch(r.batchSize)
	r.batchMu.Unlock()
	if len(batch) == 0 {
		return false
	}
	ok := r.postBatch(ctx, batch)
	if !ok {
		// 失败:重新入队 (需要锁)。
		r.batchMu.Lock()
		r.requeueBatch(batch)
		r.batchMu.Unlock()
	}
	return ok
}

// postBatch 序列化并发送一个已出队的 batch。
// 调用方负责出队/重新入队 (这些需要 batchMu);此函数本身不触碰队列,
// 也不持有 batchMu,因此 HTTP POST 不会阻塞 Close()/并发 flush。
// 返回 false 表示发送失败,调用方应把 batch 重新入队。
func (r *LockFreeAnomalyReporter) postBatch(parent context.Context, batch []AnomalyReport) bool {
	// 序列化为JSON
	payload, err := json.Marshal(map[string]interface{}{
		"anomalies":  batch,
		"batch_size": len(batch),
		"source":     "llm-gateway-go",
		"timestamp":  time.Now().Unix(),
	})
	if err != nil {
		slog.Error("lockfree_anomaly_reporter: failed to marshal batch, re-enqueuing", "err", err)
		r.reportsFailed.Add(uint64(len(batch)))
		return false
	}

	// 创建请求
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", r.endpoint, bytes.NewReader(payload))
	if err != nil {
		slog.Error("lockfree_anomaly_reporter: failed to create request, re-enqueuing", "err", err)
		r.reportsFailed.Add(uint64(len(batch)))
		return false
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llm-gateway-go/lockfree-anomaly-reporter")

	// 发送HTTP POST
	resp, err := r.httpClient.Do(req)
	if err != nil {
		slog.Warn("lockfree_anomaly_reporter: failed to send batch, re-enqueuing",
			"err", err,
			"batch_size", len(batch))
		r.reportsFailed.Add(uint64(len(batch)))
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.reportsSent.Add(uint64(len(batch)))
		slog.Debug("lockfree_anomaly_reporter: sent batch successfully",
			"batch_size", len(batch),
			"status", resp.StatusCode)
		return true
	}
	r.reportsFailed.Add(uint64(len(batch)))
	slog.Warn("lockfree_anomaly_reporter: server returned non-2xx, re-enqueuing",
		"status", resp.StatusCode,
		"batch_size", len(batch))
	return false
}

func (r *LockFreeAnomalyReporter) requeueBatch(batch []AnomalyReport) {
	for _, report := range batch {
		if !r.queue.Enqueue(report) {
			break
		}
	}
}

func (r *LockFreeAnomalyReporter) Flush(ctx context.Context) {
	if !r.enabled.Load() {
		return
	}

	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for r.queue.Size() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		default:
		}
		if r.sendBatchContext(ctx) {
			continue
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-deadline.C:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Close 关闭报告器
func (r *LockFreeAnomalyReporter) Close() error {
	r.closeOnce.Do(func() {
		r.closing.Store(true)
		r.submitMu.Lock()
		r.enabled.Store(false)
		r.submitMu.Unlock()
		r.cancel()
		<-r.done

		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.flushMu.Lock()
		defer r.flushMu.Unlock()
	flush:
		for attempts := 0; r.queue.Size() > 0 && attempts < 3; attempts++ {
			// 出队/重新入队在锁内,HTTP POST 不在锁内。
			r.batchMu.Lock()
			batch := r.queue.TryDequeueBatch(r.batchSize)
			r.batchMu.Unlock()
			if len(batch) == 0 {
				break flush
			}
			if r.postBatch(closeCtx, batch) {
				continue
			}
			// 失败:重新入队。
			r.batchMu.Lock()
			r.requeueBatch(batch)
			r.batchMu.Unlock()
			select {
			case <-closeCtx.Done():
				break flush
			case <-time.After(100 * time.Millisecond):
			}
		}
		r.queue.Close()
	})
	return nil
}

// Stats 返回统计信息
func (r *LockFreeAnomalyReporter) Stats() AnomalyReporterStats {
	queueStats := r.queue.Stats()
	return AnomalyReporterStats{
		QueueSize:     queueStats.Size,
		QueueCapacity: queueStats.Capacity,
		Enqueued:      queueStats.EnqueueCount,
		Dropped:       queueStats.DropCount,
		Sent:          r.reportsSent.Load(),
		Failed:        r.reportsFailed.Load(),
	}
}

// AnomalyReporterStats 异常报告器统计信息
type AnomalyReporterStats struct {
	QueueSize     int64  // 当前队列大小
	QueueCapacity int64  // 队列容量
	Enqueued      uint64 // 入队总数
	Dropped       uint64 // 丢弃总数（队列满）
	Sent          uint64 // 成功发送总数
	Failed        uint64 // 发送失败总数
}

// AnomalyReportEnvelope is the correlation context attached to every
// anomaly report. Producers (executor / streaming bridges) populate it
// from the request context so the external endpoint can join with
// request_logs and the on-disk audit log without re-fetching the body.
type AnomalyReportEnvelope struct {
	ClientRequestID string
	GWSessionID     string
	ProviderID      int
	CredentialID    int
	TraceID         string
}

type anomalyContextKey struct{}

// WithAnomalyEnvelope stores an envelope on the request context. The
// anomaly reporter pulls the envelope out at report time and includes
// its fields in the external payload.
func WithAnomalyEnvelope(ctx context.Context, env AnomalyReportEnvelope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, anomalyContextKey{}, env)
}

func envelopeFromContext(ctx context.Context) *AnomalyReportEnvelope {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(anomalyContextKey{}).(AnomalyReportEnvelope); ok {
		return &v
	}
	return nil
}

// hashString returns the lowercase hex SHA-256 of the input. Used by
// the anomaly reporter to anchor each anomaly against the upstream
// payload without sending the bytes themselves.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
