package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	}

	reporter.enabled.Store(enabled && endpoint != "")

	if reporter.enabled.Load() {
		// 启动后台发送协程
		go reporter.sendWorker()
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
		Timestamp:       time.Now(),
		RequestID:       requestID,
		AnomalyType:     "tool_calls_missing",
		SourceProtocol:  sourceProto,
		TargetProtocol:  targetProto,
		ConversionStep:  "parse_or_serialize",
		RawInputSample:  truncate(string(rawInput), 2000),
		RawOutputSample: truncate(string(rawOutput), 2000),
		Confidence:      confidence,
		Analysis: map[string]string{
			"suspected_cause":      "IR conversion dropped tool_calls field",
			"missing_tool_calls":   truncate(missingToolCalls, 1000),
			"recommended_action":   "Check IR ParseXXX/SerializeXXX functions for tool_calls handling",
			"upstream_has_tools":   "true",
			"downstream_has_tools": "false",
		},
	}

	if !r.queue.Enqueue(report) {
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
		RawInputSample: truncate(string(rawInput), 2000),
		ErrorMessage:   err.Error(),
		Confidence:     1.0,
		Analysis: map[string]string{
			"error_type":         fmt.Sprintf("%T", err),
			"suspected_cause":    "Malformed input or unsupported field",
			"recommended_action": "Review parse/serialize logic for this protocol combination",
		},
	}

	if !r.queue.Enqueue(report) {
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
		Timestamp:       time.Now(),
		RequestID:       requestID,
		AnomalyType:     "semantic_incomplete",
		TargetProtocol:  protocol,
		ConversionStep:  "post_serialize",
		RawOutputSample: truncate(string(rawOutput), 2000),
		Confidence:      confidence,
		Analysis: map[string]string{
			"reason":             reason,
			"indicators":         indicatorsStr,
			"suspected_cause":    "Response stopped prematurely or tool_calls were dropped",
			"recommended_action": "Compare raw upstream response with IR output to identify loss point",
		},
	}

	if !r.queue.Enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping report",
			"request_id", requestID,
			"anomaly_type", "semantic_incomplete")
	}
}

// sendWorker 后台发送协程
func (r *LockFreeAnomalyReporter) sendWorker() {
	ticker := time.NewTicker(r.flushDelay)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			// 关闭前发送剩余报告
			r.sendBatch()
			return
		case <-ticker.C:
			r.sendBatch()
		}
	}
}

// sendBatch 批量发送异常报告（失败时重新入队一次）
func (r *LockFreeAnomalyReporter) sendBatch() {
	batch := r.queue.TryDequeueBatch(r.batchSize)
	if len(batch) == 0 {
		return
	}

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
		for _, report := range batch {
			r.queue.Enqueue(report) // 重试一次
		}
		return
	}

	// 创建请求
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", r.endpoint, bytes.NewReader(payload))
	if err != nil {
		slog.Error("lockfree_anomaly_reporter: failed to create request, re-enqueuing", "err", err)
		r.reportsFailed.Add(uint64(len(batch)))
		for _, report := range batch {
			r.queue.Enqueue(report)
		}
		return
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
		for _, report := range batch {
			r.queue.Enqueue(report)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.reportsSent.Add(uint64(len(batch)))
		slog.Debug("lockfree_anomaly_reporter: sent batch successfully",
			"batch_size", len(batch),
			"status", resp.StatusCode)
	} else {
		r.reportsFailed.Add(uint64(len(batch)))
		slog.Warn("lockfree_anomaly_reporter: server returned non-2xx, re-enqueuing",
			"status", resp.StatusCode,
			"batch_size", len(batch))
		for _, report := range batch {
			r.queue.Enqueue(report)
		}
	}
}

// Flush 刷新队列中的所有报告
func (r *LockFreeAnomalyReporter) Flush(ctx context.Context) {
	if !r.enabled.Load() {
		return
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && r.queue.Size() > 0 {
		r.sendBatch()
		time.Sleep(100 * time.Millisecond)
	}
}

// Close 关闭报告器
func (r *LockFreeAnomalyReporter) Close() error {
	if !r.enabled.Load() {
		return nil
	}

	// 停止后台协程
	r.cancel()

	// 刷新剩余报告
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.Flush(ctx)

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
