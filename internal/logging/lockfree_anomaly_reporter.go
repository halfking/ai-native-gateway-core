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

// AnomalyReport 异常报告
//
// 2026-07-27: removed RawInputSample/RawOutputSample so the external
// payload no longer contains the original request/response bytes. The
// audit endpoint can still join with the on-disk raw log via the
// correlation fields below (request_id, gw_session_id, trace_id, ...)
// plus the SHA-256 hash and size of each side.
//
// 2026-07-28: AnomalyReport moved here from the legacy mutex-based
// anomaly_reporter.go (now deleted). Producers (LockFreeAnomalyReporter
// + streaming diagnostic adapters) share this struct.
type AnomalyReport struct {
	Timestamp      time.Time         `json:"timestamp"`
	RequestID      string            `json:"request_id"`
	AnomalyType    string            `json:"anomaly_type"` // "tool_calls_missing", "conversion_error", "semantic_incomplete", "raw_log_overflow", "raw_log_close_drained"
	SourceProtocol string            `json:"source_protocol"`
	TargetProtocol string            `json:"target_protocol"`
	ConversionStep string            `json:"conversion_step"`
	RawInputHash   string            `json:"raw_input_hash,omitempty"`
	RawInputSize   int               `json:"raw_input_size,omitempty"`
	RawOutputHash  string            `json:"raw_output_hash,omitempty"`
	RawOutputSize  int               `json:"raw_output_size,omitempty"`
	RawLogFile     string            `json:"raw_log_file,omitempty"`
	RawLogOffset   int64             `json:"raw_log_offset,omitempty"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	Analysis       map[string]string `json:"analysis,omitempty"` // 初步判断
	Confidence     float64           `json:"confidence"`         // 置信度 (0.0-1.0)

	// 2026-07-27: correlation envelope. Producers fill these from the
	// request context so the external endpoint can pivot into
	// _request_logs and trace spans without an extra lookup.
	ClientRequestID string `json:"client_request_id,omitempty"`
	GWSessionID     string `json:"gw_session_id,omitempty"`
	GWTaskID        string `json:"gw_task_id,omitempty"`
	TenantID        string `json:"tenant_id,omitempty"`
	ProviderID      int    `json:"provider_id,omitempty"`
	CredentialID    int    `json:"credential_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
}

// Anomaly type constants (2026-07-28 §5.8). Defined as package-level
// constants so producers can switch on the typed value rather than
// a stringly-typed `anomaly_type` field.
const (
	AnomalyTypeToolCallsMissing   = "tool_calls_missing"
	AnomalyTypeConversionError    = "conversion_error"
	AnomalyTypeSemanticIncomplete = "semantic_incomplete"
	AnomalyTypeRawLogOverflow     = "raw_log_overflow"
	AnomalyTypeRawLogCloseDrained = "raw_log_close_drained"
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

	// rawLogLocator returns the current raw audit log file path and the
	// byte offset of the next write. Used to populate
	// AnomalyReport.RawLogFile/RawLogOffset so the external endpoint can
	// reference the on-disk audit log entry that was most recently
	// flushed before the anomaly was raised. nil disables file/offset
	// reporting (legacy behaviour).
	//
	// 2026-07-28: see design docs §5.7. The locator is invoked at
	// Report time, so it captures the "most recent flush position" — for
	// concurrent requests this may point to a different request's entry.
	// Callers that need strict per-request correlation must wire the
	// raw data logger to flush synchronously before reporting.
	rawLogLocator func() (file string, offset int64)

	// 统计信息
	reportsSent   atomic.Uint64
	reportsFailed atomic.Uint64

	// consecutive permanent endpoint rejections (404/405/410/401/...).
	// 2026-10-11: 154 生产事故 — endpoint 配置到一个本网关不存在的路由
	// （/api/diagnostics/anomalies，实际路由是 /api/admin/format-anomalies），
	// postBatch 对非 2xx 一律 requeue，sendWorker 每 5s 重发同一批 →
	// 24h 内 9341 次 404 循环。永久性 4xx 重试永远不可能成功，连续
	// permanentFailuresThreshold 次后自禁用报告器。
	permanentFailures atomic.Uint32

	// permanentlyDisabled records that this reporter shut ITSELF off after
	// permanentFailuresThreshold consecutive permanent endpoint rejections.
	//
	// 2026-10-11（12h 审计 A2）：自禁用本身没有留下任何可观测痕迹——enabled
	// 归零后 enqueue 直接 false、所有 Report* 静默返回，而 Stats() 的 5 个字段
	// 里没有任何一个能区分「刚自禁用」与「一直没启用」。于是运维看到的是
	// 一条读数正常的死量具。同批 c88bc79e3 在脚本侧立的规矩正是「量具不可用
	// 不得报 ok」，Go 侧留了同一个盲区。
	//
	// 单独一个 flag（而不是读 enabled）的原因：enabled=false 有三种成因
	// （构造时未启用、Close 已调用、自禁用），只有自禁用这一种需要人介入。
	// Close 也会置它，保证进程收尾时不会把「正常关闭」误报成故障。
	permanentlyDisabled atomic.Bool
}

// permanentFailuresThreshold is the number of consecutive permanent endpoint
// rejections after which the reporter disables itself.
const permanentFailuresThreshold = 3

// isPermanentAnomalyStatus reports whether an HTTP status makes retrying the
// same batch pointless. Auth failures are included: the reporter sends no
// credentials, so a 401/403 from the endpoint cannot self-heal.
func isPermanentAnomalyStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusGone,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// NewLockFreeAnomalyReporter 创建无锁异常报告器
func NewLockFreeAnomalyReporter(endpoint string, enabled bool, queueSize int) *LockFreeAnomalyReporter {
	return NewLockFreeAnomalyReporterWithRawLogLocator(endpoint, enabled, queueSize, nil)
}

// NewLockFreeAnomalyReporterWithRawLogLocator creates a lock-free
// anomaly reporter that, when reporting, stamps each AnomalyReport with
// the (file, offset) returned by locator. Pass nil to disable file
// stamping (legacy behaviour, equivalent to NewLockFreeAnomalyReporter).
//
// 2026-07-28: see design docs §5.7.
func NewLockFreeAnomalyReporterWithRawLogLocator(endpoint string, enabled bool, queueSize int, locator func() (string, int64)) *LockFreeAnomalyReporter {
	if queueSize <= 0 {
		queueSize = 1000
	}

	ctx, cancel := context.WithCancel(context.Background())

	reporter := &LockFreeAnomalyReporter{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue:         NewLockFreeQueue[AnomalyReport](queueSize),
		ctx:           ctx,
		cancel:        cancel,
		batchSize:     10,
		flushDelay:    5 * time.Second,
		done:          make(chan struct{}),
		rawLogLocator: locator,
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
	r.stampRawLogLocation(&report)

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
	r.stampRawLogLocation(&report)

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
	r.stampRawLogLocation(&report)

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

// stampRawLogLocation reads the configured rawLogLocator and writes the
// returned (file, offset) into the report. Safe to call on a nil
// receiver (no-op). The locator callback is invoked at most once per
// report and is permitted to panic-recover — see recover below.
//
// 2026-07-28: see design docs §5.7.
func (r *LockFreeAnomalyReporter) stampRawLogLocation(report *AnomalyReport) {
	if r == nil || report == nil || r.rawLogLocator == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("lockfree_anomaly_reporter: rawLogLocator panicked, leaving file/offset blank",
				"request_id", report.RequestID,
				"panic", rec)
		}
	}()
	file, offset := r.rawLogLocator()
	report.RawLogFile = file
	report.RawLogOffset = offset
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
		r.permanentFailures.Store(0)
		slog.Debug("lockfree_anomaly_reporter: sent batch successfully",
			"batch_size", len(batch),
			"status", resp.StatusCode)
		return true
	}
	r.reportsFailed.Add(uint64(len(batch)))
	if isPermanentAnomalyStatus(resp.StatusCode) {
		n := r.permanentFailures.Add(1)
		if n <= permanentFailuresThreshold {
			slog.Error("lockfree_anomaly_reporter: endpoint permanently rejected batch, dropping (retry can never succeed)",
				"status", resp.StatusCode,
				"endpoint", r.endpoint,
				"batch_size", len(batch),
				"consecutive_permanent_rejections", n)
		}
		if n >= permanentFailuresThreshold && r.enabled.CompareAndSwap(true, false) {
			// 与 enabled 同一临界区内翻转：先置 flag 再关闸，读 Stats 的
			// 人不会看到「已禁用但没标记」的中间态。
			r.permanentlyDisabled.Store(true)
			slog.Error("lockfree_anomaly_reporter: disabled after repeated permanent endpoint rejections",
				"status", resp.StatusCode,
				"endpoint", r.endpoint,
				"hint", "check LLM_GATEWAY_ANOMALY_ENDPOINT points to a route this gateway serves")
		}
		// 返回 true 让调用方丢弃 batch 而不是重新入队。
		return true
	}
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
		// 正常关闭不是故障：显式清掉自禁用标记，避免收尾读数把
		// 「按计划关停」误报成「端点永久拒绝导致停摆」。
		r.permanentlyDisabled.Store(false)
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
		// A2: 自禁用必须从读数上可区分——否则一条读数正常的死量具
		// 和一个健康的量具在监控里完全同形。
		PermanentlyDisabled: r.permanentlyDisabled.Load(),
		PermanentRejections: uint64(r.permanentFailures.Load()),
	}
}

// AnomalyReporterStats 异常报告统计信息
type AnomalyReporterStats struct {
	QueueSize     int64  `json:"queue_size"`     // 当前队列大小
	QueueCapacity int64  `json:"queue_capacity"` // 队列容量
	Enqueued      uint64 `json:"enqueued"`       // 入队总数
	Dropped       uint64 `json:"dropped"`        // 丢弃总数（队列满）
	Sent          uint64 `json:"sent"`           // 成功发送总数
	Failed        uint64 `json:"failed"`         // 发送失败总数

	// PermanentlyDisabled 报告器是否因端点永久性拒绝而自禁用。自禁用后
	// 不再接受任何上报（enqueue 直接返回 false），需要人改配置后重启——
	// 因此该位为 true 就是一个待处置的故障，不是一次瞬时抖动。
	PermanentlyDisabled bool `json:"permanently_disabled"`
	// PermanentRejections 连续永久性 4xx 计数（2xx 成功后归零）。
	// 与 PermanentlyDisabled 一起看可区分「正在逼近阈值」与「已停摆」。
	PermanentRejections uint64 `json:"permanent_rejections"`
}

// AnomalyReportEnvelope is the correlation context attached to every
// anomaly report. Producers (executor / streaming bridges) populate it
// from the request context so the external endpoint can join with
// _request_logs and the on-disk audit log without re-fetching the body.
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

// truncate clips a string to maxLen bytes (not runes) and appends a
// truncation marker. 2026-07-28: previously lived in the legacy mutex
// anomaly_reporter.go; moved here when that file was deleted.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... [truncated from %d bytes]", len(s))
}

// ReportRawLogOverflow queues an anomaly reporting that the async
// raw-data logger dropped entries because the queue was full. The
// envelope fields are best-effort; the key payload is the dropped
// count and the latest raw-log location so operators can find the
// gap on disk.
//
// 2026-07-28 §5.8: replaces the silent slog.Warn-only behaviour
// from the pre-fix AsyncRawDataLogger.noteDroppedEntry.
func (r *LockFreeAnomalyReporter) ReportRawLogOverflow(
	ctx context.Context,
	env RawCorrelationEnvelope,
	dropped uint64,
) {
	if r == nil || !r.enabled.Load() {
		return
	}
	report := AnomalyReport{
		Timestamp:      time.Now(),
		RequestID:      "raw_logger",
		AnomalyType:    AnomalyTypeRawLogOverflow,
		SourceProtocol: "raw_logger",
		TargetProtocol: "raw_logger",
		ConversionStep: "queue_overflow",
		ErrorMessage:   fmt.Sprintf("raw log queue overflow; dropped=%d", dropped),
		Confidence:     1.0,
		Analysis: map[string]string{
			"dropped_count": fmt.Sprintf("%d", dropped),
			"cause":         "async_raw_logger: queue full, dropping log entries",
		},
		ClientRequestID: env.ClientRequestID,
		GWSessionID:     env.GWSessionID,
		GWTaskID:        env.GWTaskID,
		TenantID:        env.TenantID,
		ProviderID:      env.ProviderID,
		CredentialID:    env.CredentialID,
		TraceID:         env.TraceID,
	}
	r.stampRawLogLocation(&report)
	if !r.enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping raw_log_overflow report",
			"dropped_count", dropped)
	}
}

// ReportRawLogCloseDrained queues an anomaly when Close() exits with
// items still in the queue. `remaining` is the number of items that
// could not be drained before the flush deadline (5*flushDelay).
//
// 2026-07-28 §5.8: paired with the close_drained stub entry written
// to the audit log so operators can correlate the anomaly with the
// on-disk "shutdown" row.
func (r *LockFreeAnomalyReporter) ReportRawLogCloseDrained(
	ctx context.Context,
	env RawCorrelationEnvelope,
	remaining uint64,
) {
	if r == nil || !r.enabled.Load() {
		return
	}
	report := AnomalyReport{
		Timestamp:      time.Now(),
		RequestID:      "raw_logger",
		AnomalyType:    AnomalyTypeRawLogCloseDrained,
		SourceProtocol: "raw_logger",
		TargetProtocol: "raw_logger",
		ConversionStep: "shutdown",
		ErrorMessage:   fmt.Sprintf("raw logger close drained with remaining=%d", remaining),
		Confidence:     1.0,
		Analysis: map[string]string{
			"remaining_count": fmt.Sprintf("%d", remaining),
			"cause":           "AsyncRawDataLogger.Close exited before draining the queue",
		},
		GWSessionID: env.GWSessionID,
	}
	r.stampRawLogLocation(&report)
	if !r.enqueue(report) {
		slog.Warn("lockfree_anomaly_reporter: queue full, dropping raw_log_close_drained report",
			"remaining", remaining)
	}
}
