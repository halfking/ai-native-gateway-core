package streaming

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// DiagnosticContext bundles optional diagnostic components for stream bridges.
// All components are best-effort and must not affect client-visible behavior.
// A fresh value is built per request in cmd/gateway/main.go, so the embedded
// counter is per-request state.
type DiagnosticContext struct {
	RawLogger executors.RawDataLogger
	Anomaly   executors.AnomalyReporter
	Semantic  executors.SemanticAnalyzer

	// conversionReports caps conversion_error anomalies per request. An
	// upstream emitting a shape the IR parser rejects produces one report
	// per frame otherwise, which saturates the reporter queue and evicts
	// unrelated anomalies for the whole process.
	conversionReports atomic.Int32
}

// maxConversionReportsPerRequest bounds conversion_error anomalies for a single
// request. The first few carry the same diagnostic signal as the whole stream.
const maxConversionReportsPerRequest = 3

// recoverDiagnostic keeps a misbehaving diagnostic component from reaching the
// request path. Errors are already logged-and-ignored at every call site; a
// panic used to escape instead, and the blast radius differed per bridge:
// StreamChatWithPendingCaptureAndDiagnostics registers its report defer before
// its recover defer, so the panic unwound into the HTTP handler and skipped
// both the audit emit and pc.finalize; the other four bridges caught it but
// then marked a fully delivered stream as Interrupted, which the executors turn
// into a failover plus a circuit-breaker penalty on a healthy credential.
func recoverDiagnostic(requestID, site string) {
	if r := recover(); r != nil {
		slog.Error("stream diagnostics: component panicked",
			"request_id", requestID, "site", site, "panic", r)
	}
}

func logRawUpstreamFrame(diagnostics *DiagnosticContext, requestID, protocol string, frame []byte) {
	if diagnostics == nil || diagnostics.RawLogger == nil || len(frame) == 0 {
		return
	}
	defer recoverDiagnostic(requestID, "log_raw_upstream_frame")
	if err := diagnostics.RawLogger.LogResponse(requestID, protocol, frame, true); err != nil {
		slog.Warn("stream diagnostics: raw response logging failed", "request_id", requestID, "error", err)
	}
}

func reportConversionAnomaly(
	diagnostics *DiagnosticContext,
	requestID, sourceProtocol, targetProtocol, step string,
	rawInput []byte,
	err error,
	details map[string]interface{},
) {
	if diagnostics == nil || diagnostics.Anomaly == nil || err == nil {
		return
	}
	if diagnostics.conversionReports.Add(1) > maxConversionReportsPerRequest {
		return
	}
	defer recoverDiagnostic(requestID, "report_conversion_anomaly")
	if details == nil {
		details = make(map[string]interface{}, 5)
	}
	details["source_protocol"] = sourceProtocol
	details["target_protocol"] = targetProtocol
	details["conversion_step"] = step
	details["raw_input"] = rawInput
	details["error"] = err.Error()
	if reportErr := diagnostics.Anomaly.ReportAnomaly(requestID, "conversion_error", details); reportErr != nil {
		slog.Warn("stream diagnostics: anomaly reporting failed", "request_id", requestID, "error", reportErr)
	}
}

const (
	maxDiagnosticTextBytes     = 64 * 1024
	maxDiagnosticEvidenceBytes = 8 * 1024
)

type streamDiagnosticCollector struct {
	text                 strings.Builder
	emittedToolCallCount int
	finishReason         string
	rawToolCalls         []string
	textTruncated        bool
}

func (c *streamDiagnosticCollector) observeRaw(frame []byte) {
	if len(c.rawToolCalls) >= 5 || !rawStreamFrameHasToolCall(frame) {
		return
	}
	if len(frame) > maxDiagnosticEvidenceBytes {
		frame = frame[:maxDiagnosticEvidenceBytes]
	}
	c.rawToolCalls = append(c.rawToolCalls, string(frame))
}

func rawStreamFrameHasToolCall(frame []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(frame, &raw); err != nil {
		return false
	}
	if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
					return true
				}
			}
			if message, ok := choice["message"].(map[string]any); ok {
				if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
					return true
				}
			}
		}
	}
	if content, ok := raw["content"].([]any); ok {
		for _, block := range content {
			if value, ok := block.(map[string]any); ok && value["type"] == "tool_use" {
				return true
			}
		}
	}
	if contentBlock, ok := raw["content_block"].(map[string]any); ok && contentBlock["type"] == "tool_use" {
		return true
	}
	return false
}

func (c *streamDiagnosticCollector) observeChunk(chunk *ir.StreamChunk) {
	if chunk == nil {
		return
	}
	if chunk.FinishReason != "" {
		c.finishReason = chunk.FinishReason
	}
	if chunk.Delta == nil {
		return
	}
	if !c.textTruncated && chunk.Delta.Content != "" {
		remaining := maxDiagnosticTextBytes - c.text.Len()
		if remaining <= 0 {
			c.textTruncated = true
		} else if len(chunk.Delta.Content) > remaining {
			c.text.WriteString(chunk.Delta.Content[:remaining])
			c.textTruncated = true
		} else {
			c.text.WriteString(chunk.Delta.Content)
		}
	}
}

func (c *streamDiagnosticCollector) observeEmittedLine(line string) {
	chunk, err := ir.ParseOpenAIStreamChunk(line)
	if err == nil {
		c.observeEmittedChunk(chunk)
	}
}

func (c *streamDiagnosticCollector) observeEmittedChunk(chunk *ir.StreamChunk) {
	if chunk == nil || chunk.Delta == nil {
		return
	}
	c.emittedToolCallCount += len(chunk.Delta.ToolCalls)
}

func (c *streamDiagnosticCollector) report(
	diagnostics *DiagnosticContext,
	requestID, sourceProtocol, targetProtocol string,
	streamInterrupted bool,
) {
	if diagnostics == nil {
		return
	}
	defer recoverDiagnostic(requestID, "collector_report")

	rawEvidence := strings.Join(c.rawToolCalls, "\n")
	clientOutput := []byte(c.text.String())
	// An interrupted stream (chunk timeout, upstream read error, client
	// cancel) can bank tool-call evidence from a raw frame and then die
	// before the bridge converts it. Nothing was dropped by the gateway in
	// that case, so reporting it as tool_calls_missing with confidence 1.0
	// would bury genuine conversion losses under upstream flakiness.
	if !streamInterrupted && rawEvidence != "" && c.emittedToolCallCount == 0 && diagnostics.Anomaly != nil {
		details := map[string]interface{}{
			"source_protocol":    sourceProtocol,
			"target_protocol":    targetProtocol,
			"conversion_step":    "stream_parse_or_serialize",
			"raw_input":          []byte(rawEvidence),
			"raw_output":         clientOutput,
			"missing_tool_calls": rawEvidence,
			"confidence":         1.0,
		}
		if err := diagnostics.Anomaly.ReportAnomaly(requestID, "tool_calls_missing", details); err != nil {
			slog.Warn("stream diagnostics: tool-call anomaly reporting failed", "request_id", requestID, "error", err)
		}
	}

	if diagnostics.Semantic == nil || diagnostics.Anomaly == nil || c.textTruncated {
		return
	}
	// emittedToolCallCount counts delta fragments, not distinct tool calls: a
	// single call with a large argument payload can contribute tens of
	// thousands. The analyzer only tests len(ToolCalls) > 0, so allocate a
	// presence marker rather than one empty struct per fragment.
	toolCallMarkers := 0
	if c.emittedToolCallCount > 0 {
		toolCallMarkers = 1
	}
	response := &ir.InternalResponse{
		SourceProtocol: sourceProtocol,
		FinishReason:   c.finishReason,
		ToolCalls:      make([]ir.ResponseToolCall, toolCallMarkers),
	}
	if c.text.Len() > 0 {
		response.Content = []ir.ResponseContentBlock{{Type: "text", Text: c.text.String()}}
	}
	analysis, err := diagnostics.Semantic.AnalyzeResponse(requestID, response)
	if err != nil {
		slog.Warn("stream diagnostics: semantic analysis failed", "request_id", requestID, "error", err)
		return
	}
	if analysis == nil || !analysis.IsIncomplete || !analysis.SuspectedMissingTools {
		return
	}
	details := map[string]interface{}{
		"source_protocol": sourceProtocol,
		"target_protocol": targetProtocol,
		"conversion_step": "stream_post_serialize",
		"raw_output":      clientOutput,
		"reason":          analysis.Reason,
		"indicators":      analysis.Indicators,
		"confidence":      analysis.Confidence,
	}
	if err := diagnostics.Anomaly.ReportAnomaly(requestID, "semantic_incomplete", details); err != nil {
		slog.Warn("stream diagnostics: semantic anomaly reporting failed", "request_id", requestID, "error", err)
	}
}
