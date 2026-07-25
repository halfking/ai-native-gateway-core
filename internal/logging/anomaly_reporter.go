package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// AnomalyReporter 向外部服务报告格式异常
type AnomalyReporter struct {
	endpoint   string
	httpClient *http.Client
	mu         sync.Mutex
	queue      []AnomalyReport
	maxQueue   int
	enabled    bool
}

// AnomalyReport 异常报告
type AnomalyReport struct {
	Timestamp       time.Time         `json:"timestamp"`
	RequestID       string            `json:"request_id"`
	AnomalyType     string            `json:"anomaly_type"` // "tool_calls_missing", "conversion_error", "semantic_incomplete"
	SourceProtocol  string            `json:"source_protocol"`
	TargetProtocol  string            `json:"target_protocol"`
	ConversionStep  string            `json:"conversion_step"`
	RawInputSample  string            `json:"raw_input_sample"`  // 截断的原始输入
	RawOutputSample string            `json:"raw_output_sample"` // 截断的原始输出
	ErrorMessage    string            `json:"error_message,omitempty"`
	Analysis        map[string]string `json:"analysis,omitempty"` // 初步判断
	Confidence      float64           `json:"confidence"`         // 置信度 (0.0-1.0)
}

// NewAnomalyReporter 创建异常报告器
//
// endpoint: 格式异常接收端点 URL (例如: https://llmgo.kxpms.cn/format-anomalies)
// enabled: 是否启用报告（可通过环境变量控制）
func NewAnomalyReporter(endpoint string, enabled bool) *AnomalyReporter {
	return &AnomalyReporter{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue:    make([]AnomalyReport, 0),
		maxQueue: 100,
		enabled:  enabled && endpoint != "",
	}
}

// ReportToolCallsMissing 报告工具调用丢失异常
func (r *AnomalyReporter) ReportToolCallsMissing(
	ctx context.Context,
	requestID string,
	sourceProto, targetProto string,
	rawInput, rawOutput []byte,
	missingToolCalls string,
	confidence float64,
) {
	if !r.enabled {
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

	r.enqueueAndSend(ctx, report)
}

// ReportConversionError 报告转换错误异常
func (r *AnomalyReporter) ReportConversionError(
	ctx context.Context,
	requestID string,
	sourceProto, targetProto, step string,
	rawInput []byte,
	err error,
) {
	if !r.enabled {
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
		Confidence:     1.0, // 转换错误是确定的
		Analysis: map[string]string{
			"error_type":         fmt.Sprintf("%T", err),
			"suspected_cause":    "Malformed input or unsupported field",
			"recommended_action": "Review parse/serialize logic for this protocol combination",
		},
	}

	r.enqueueAndSend(ctx, report)
}

// ReportSemanticIncomplete 报告语义不完整异常
func (r *AnomalyReporter) ReportSemanticIncomplete(
	ctx context.Context,
	requestID string,
	protocol string,
	rawOutput []byte,
	reason string,
	indicators []string,
	confidence float64,
) {
	if !r.enabled {
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

	r.enqueueAndSend(ctx, report)
}

// enqueueAndSend 入队并尝试发送
func (r *AnomalyReporter) enqueueAndSend(ctx context.Context, report AnomalyReport) {
	r.mu.Lock()
	r.queue = append(r.queue, report)
	if len(r.queue) > r.maxQueue {
		r.queue = r.queue[1:] // 移除最旧的
	}
	r.mu.Unlock()

	// 异步发送（不阻塞主流程）
	go r.sendBatch(ctx)
}

// sendBatch 批量发送异常报告
func (r *AnomalyReporter) sendBatch(ctx context.Context) {
	r.mu.Lock()
	if len(r.queue) == 0 {
		r.mu.Unlock()
		return
	}

	// 取出最多10条
	batchSize := 10
	if len(r.queue) < batchSize {
		batchSize = len(r.queue)
	}

	batch := make([]AnomalyReport, batchSize)
	copy(batch, r.queue[:batchSize])
	r.queue = r.queue[batchSize:]
	r.mu.Unlock()

	// 序列化为JSON
	payload, err := json.Marshal(map[string]interface{}{
		"anomalies":  batch,
		"batch_size": len(batch),
		"source":     "llm-gateway-go",
	})
	if err != nil {
		slog.Error("anomaly_reporter: failed to marshal batch", "err", err)
		return
	}

	// 发送HTTP POST
	req, err := http.NewRequestWithContext(ctx, "POST", r.endpoint, bytes.NewReader(payload))
	if err != nil {
		slog.Error("anomaly_reporter: failed to create request", "err", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llm-gateway-go/anomaly-reporter")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		slog.Warn("anomaly_reporter: failed to send batch", "err", err, "batch_size", len(batch))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("anomaly_reporter: sent batch successfully",
			"batch_size", len(batch),
			"status", resp.StatusCode)
	} else {
		slog.Warn("anomaly_reporter: server returned non-2xx status",
			"status", resp.StatusCode,
			"batch_size", len(batch))
	}
}

// Flush 刷新队列中的所有报告
func (r *AnomalyReporter) Flush(ctx context.Context) {
	if !r.enabled {
		return
	}

	for {
		r.mu.Lock()
		queueLen := len(r.queue)
		r.mu.Unlock()

		if queueLen == 0 {
			break
		}

		r.sendBatch(ctx)
		time.Sleep(100 * time.Millisecond)
	}
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... [truncated from %d bytes]", len(s))
}
