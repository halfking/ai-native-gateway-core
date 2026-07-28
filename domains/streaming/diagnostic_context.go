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

	// Audit (2026-07-28 §5.4) is the per-request correlation handle
	// that streaming bridges attach to every raw log emission and
	// anomaly report. nil disables the per-frame envelope; the legacy
	// `request_id` / `direction` lookup still works in that case.
	Audit *executors.AuditContext

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

// auditFromDiagnostics returns the per-attempt AuditContext from the
// diagnostic, falling back to a freshly-allocated context with just
// the request_id + protocol when diagnostics or its Audit field is
// nil. The fallback keeps the legacy (request_id, protocol)
// envelope-blind path working for callers that haven't been
// migrated yet.
func auditFromDiagnostics(diagnostics *DiagnosticContext, requestID, protocol string) *executors.AuditContext {
	if diagnostics != nil && diagnostics.Audit != nil {
		return diagnostics.Audit
	}
	return &executors.AuditContext{RequestID: requestID, Protocol: protocol}
}

// logRawUpstreamFrame emits one upstream SSE frame to the raw log.
// The audit context (2026-07-28 §5.4) drives the correlation envelope
// so every frame carries gw_session_id, trace_id, etc. When audit is
// nil the function falls back to the legacy (request_id, protocol)
// envelope-blind path.
func logRawUpstreamFrame(diagnostics *DiagnosticContext, audit *executors.AuditContext, frame []byte) {
	if diagnostics == nil || diagnostics.RawLogger == nil || len(frame) == 0 {
		return
	}
	requestID := audit.GetRequestID()
	protocol := audit.GetProtocol()
	defer recoverDiagnostic(requestID, "log_raw_upstream_frame")
	if audit != nil {
		audit.ChunkIndex.Add(1)
	}
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
	// OpenAI / OpenAI-compatible (including n>1 with multiple choices)
	if choices, ok := raw["choices"].([]any); ok {
		for _, choice := range choices {
			c, ok := choice.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := c["delta"].(map[string]any); ok {
				if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) > 0 {
					return true
				}
				if _, ok := delta["function_call"].(map[string]any); ok {
					return true
				}
			}
			if msg, ok := c["message"].(map[string]any); ok {
				if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
					return true
				}
				if _, ok := msg["function_call"].(map[string]any); ok {
					return true
				}
			}
		}
	}
	// Anthropic: top-level content_block or message_start carrying message.content[]
	if cb, ok := raw["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
		return true
	}
	if content, ok := raw["content"].([]any); ok {
		for _, block := range content {
			if value, ok := block.(map[string]any); ok && value["type"] == "tool_use" {
				return true
			}
		}
	}
	if msg, ok := raw["message"].(map[string]any); ok {
		if content, ok := msg["content"].([]any); ok {
			for _, block := range content {
				if value, ok := block.(map[string]any); ok && value["type"] == "tool_use" {
					return true
				}
			}
		}
	}
	// Gemini: candidates[].content.parts[].functionCall
	if candidates, ok := raw["candidates"].([]any); ok {
		for _, c := range candidates {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			content, ok := cm["content"].(map[string]any)
			if !ok {
				continue
			}
			parts, ok := content["parts"].([]any)
			if !ok {
				continue
			}
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := pm["functionCall"].(map[string]any); ok {
					return true
				}
			}
		}
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
