package executors

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

type rawDataLogger interface {
	LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string)
	LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string)
	LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string)
	LogClientResponse(requestID, protocol string, body []byte, conversionStep string)
}

// envelopeAwareRawDataLogger is the optional interface implemented by
// loggers that can attach the full correlation envelope. Producers
// type-assert the adapter's logger against this interface; legacy
// loggers fall through to the non-envelope methods.
type envelopeAwareRawDataLogger interface {
	LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope)
	LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
	LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
	LogClientResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
}

// RawCorrelationEnvelope is re-exported here so callers in this
// package don't need to import the logging package directly. The
// concrete type is the same struct defined in internal/logging.
type RawCorrelationEnvelope = logging.RawCorrelationEnvelope

// RawDataLoggerAdapter adapts a raw data logger to the Executor diagnostic
// interface. The wrapped logger may write asynchronously, so diagnostics never
// delay the request path.
type RawDataLoggerAdapter struct {
	logger rawDataLogger
}

// NewRawDataLoggerAdapter creates an adapter for a raw data logger.
func NewRawDataLoggerAdapter(logger rawDataLogger) *RawDataLoggerAdapter {
	return &RawDataLoggerAdapter{logger: logger}
}

func (a *RawDataLoggerAdapter) LogRequest(requestID string, protocol string, body []byte) error {
	if a.logger == nil {
		return nil
	}
	a.logger.LogClientRequest(requestID, protocol, body, nil, "pre_conversion")
	return nil
}

func (a *RawDataLoggerAdapter) LogResponse(requestID string, protocol string, body []byte, isStream bool) error {
	if a.logger == nil {
		return nil
	}
	conversionStep := "pre_conversion"
	if isStream {
		conversionStep = "pre_conversion_stream_frame"
	}
	a.logger.LogUpstreamResponse(requestID, protocol, body, conversionStep)
	return nil
}

// LogUpstreamRequest records the exact payload sent to the provider.
func (a *RawDataLoggerAdapter) LogUpstreamRequest(requestID string, protocol string, body []byte) error {
	if a.logger == nil {
		return nil
	}
	a.logger.LogUpstreamRequest(requestID, protocol, body, "post_conversion")
	return nil
}

// LogClientResponse records the final payload returned to the client.
func (a *RawDataLoggerAdapter) LogClientResponse(requestID string, protocol string, body []byte) error {
	if a.logger == nil {
		return nil
	}
	a.logger.LogClientResponse(requestID, protocol, body, "post_conversion")
	return nil
}

// LogClientRequestWithEnvelope forwards the envelope to loggers that
// accept it. Legacy loggers silently fall back to the basic
// LogClientRequest path.
func (a *RawDataLoggerAdapter) LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope) {
	if a.logger == nil {
		return
	}
	if aware, ok := a.logger.(envelopeAwareRawDataLogger); ok {
		aware.LogClientRequestWithEnvelope(requestID, protocol, body, headers, conversionStep, env)
		return
	}
	a.logger.LogClientRequest(requestID, protocol, body, headers, conversionStep)
}

// LogUpstreamRequestWithEnvelope forwards the envelope to loggers that
// accept it. Legacy loggers silently fall back to the basic
// LogUpstreamRequest path.
func (a *RawDataLoggerAdapter) LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	if a.logger == nil {
		return
	}
	if aware, ok := a.logger.(envelopeAwareRawDataLogger); ok {
		aware.LogUpstreamRequestWithEnvelope(requestID, protocol, body, conversionStep, env)
		return
	}
	a.logger.LogUpstreamRequest(requestID, protocol, body, conversionStep)
}

// LogUpstreamResponseWithEnvelope forwards the envelope to loggers
// that accept it. Legacy loggers silently fall back to the basic
// LogUpstreamResponse path.
func (a *RawDataLoggerAdapter) LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	if a.logger == nil {
		return
	}
	if aware, ok := a.logger.(envelopeAwareRawDataLogger); ok {
		aware.LogUpstreamResponseWithEnvelope(requestID, protocol, body, conversionStep, env)
		return
	}
	a.logger.LogUpstreamResponse(requestID, protocol, body, conversionStep)
}

// LogClientResponseWithEnvelope forwards the envelope to loggers
// that accept it. Legacy loggers silently fall back to the basic
// LogClientResponse path.
func (a *RawDataLoggerAdapter) LogClientResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	if a.logger == nil {
		return
	}
	if aware, ok := a.logger.(envelopeAwareRawDataLogger); ok {
		aware.LogClientResponseWithEnvelope(requestID, protocol, body, conversionStep, env)
		return
	}
	a.logger.LogClientResponse(requestID, protocol, body, conversionStep)
}

type anomalyReporter interface {
	ReportToolCallsMissing(ctx context.Context, requestID string, sourceProto, targetProto string, rawInput, rawOutput []byte, missingToolCalls string, confidence float64)
	ReportConversionError(ctx context.Context, requestID string, sourceProto, targetProto, step string, rawInput []byte, err error)
	ReportSemanticIncomplete(ctx context.Context, requestID string, protocol string, rawOutput []byte, reason string, indicators []string, confidence float64)
}

// AnomalyReporterAdapter adapts the queued anomaly reporter to the Executor
// interface.
type AnomalyReporterAdapter struct {
	reporter anomalyReporter
	// parentCtx (2026-07-28 §5.7) is the context used when callers
	// invoke ReportAnomaly without an envelope. nil falls back to
	// context.Background() at call time.
	parentCtx context.Context
}

// SetParentContext overrides the context passed to the underlying
// reporter when callers invoke ReportAnomaly without supplying one.
func (a *AnomalyReporterAdapter) SetParentContext(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.parentCtx = ctx
}

// NewAnomalyReporterAdapter creates an adapter for an anomaly reporter.
func NewAnomalyReporterAdapter(reporter anomalyReporter) *AnomalyReporterAdapter {
	return &AnomalyReporterAdapter{reporter: reporter}
}

func (a *AnomalyReporterAdapter) ReportAnomaly(requestID string, anomalyType string, details map[string]interface{}) error {
	if a.reporter == nil {
		return nil
	}

	sourceProtocol := detailString(details, "source_protocol", "unknown")
	targetProtocol := detailString(details, "target_protocol", "unknown")
	conversionStep := detailString(details, "conversion_step", "stream_conversion")
	rawInput := detailBytes(details, "raw_input")
	rawOutput := detailBytes(details, "raw_output")
	confidence := detailFloat(details, "confidence", 1)

	// 2026-07-28 §5.7: the anomaly reporter's `ctx` is what carries
	// the audit envelope (via logging.WithAnomalyEnvelope). We
	// historically passed context.Background() here, which produced
	// empty correlation context on every report. Until callers are
	// migrated to ReportAnomalyFromContext, use the parent context
	// from the adapter if one was configured via SetParentContext;
	// fall back to context.Background() otherwise.
	ctx := a.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}

	switch anomalyType {
	case "tool_calls_missing":
		a.reporter.ReportToolCallsMissing(
			ctx, requestID, sourceProtocol, targetProtocol,
			rawInput, rawOutput,
			detailString(details, "missing_tool_calls", "upstream tool calls were not emitted"),
			confidence,
		)
	case "semantic_incomplete":
		a.reporter.ReportSemanticIncomplete(
			ctx, requestID, targetProtocol, rawOutput,
			detailString(details, "reason", "response appears incomplete"),
			detailStrings(details, "indicators"), confidence,
		)
	default:
		a.reporter.ReportConversionError(
			ctx, requestID, sourceProtocol, targetProtocol, conversionStep,
			rawInput, fmt.Errorf("%s", detailString(details, "error", anomalyType)),
		)
	}
	return nil
}

// AnomalyReporterAdapterWithAudit (2026-07-28 §5.7) enriches
// AnomalyReporterAdapter with an AuditContext. ReportAnomalyFromContext
// dispatches with the audit envelope attached to ctx, so the
// downstream LockFreeAnomalyReporter fills client_request_id /
// gw_session_id / provider_id / credential_id / trace_id on the
// AnomalyReport. SetRawLookup additionally populates file/offset
// from the per-request frame index.
type AnomalyReporterAdapterWithAudit struct {
	*AnomalyReporterAdapter
	parentCtx context.Context
	rawLookup func(requestID, direction string) (file string, offset int64, ok bool)
}

// NewAnomalyReporterAdapterWithAudit builds a wrapper that defaults
// parentCtx to context.Background(). Callers that have a request
// context (e.g. the handler's request context) can SetParentContext
// before invoking ReportAnomalyFromContext.
func NewAnomalyReporterAdapterWithAudit(reporter anomalyReporter) *AnomalyReporterAdapterWithAudit {
	return &AnomalyReporterAdapterWithAudit{
		AnomalyReporterAdapter: &AnomalyReporterAdapter{reporter: reporter},
		parentCtx:              context.Background(),
	}
}

// SetParentContext overrides the parent context used when no
// per-report ctx is supplied. The AuditContext's envelope is
// layered on top of this context.
func (a *AnomalyReporterAdapterWithAudit) SetParentContext(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.parentCtx = ctx
}

// SetRawLookup wires a per-request raw-frame index. When set, the
// adapter populates file/offset on the underlying reporter's
// anomaly report via context.WithValue (the LockFreeAnomalyReporter
// reads it during ReportXxx). For now the lookup is plumbed via
// the existing rawLogLocator on the reporter itself — adapter-side
// overrides are a future enhancement.
func (a *AnomalyReporterAdapterWithAudit) SetRawLookup(fn func(string, string) (string, int64, bool)) {
	if a == nil {
		return
	}
	a.rawLookup = fn
}

// ReportAnomalyFromContext dispatches to the underlying reporter
// with the audit envelope's correlation context attached.
func (a *AnomalyReporterAdapterWithAudit) ReportAnomalyFromContext(
	auditCtx *AuditContext, requestID, anomalyType string, details map[string]interface{},
) error {
	if a == nil || a.reporter == nil {
		return nil
	}
	ctx := a.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if auditCtx != nil {
		env := auditCtx.AnomalyReportEnvelope()
		ctx = logging.WithAnomalyEnvelope(ctx, env)
	}
	sourceProtocol := detailString(details, "source_protocol", "unknown")
	targetProtocol := detailString(details, "target_protocol", "unknown")
	conversionStep := detailString(details, "conversion_step", "stream_conversion")
	rawInput := detailBytes(details, "raw_input")
	rawOutput := detailBytes(details, "raw_output")
	confidence := detailFloat(details, "confidence", 1)

	switch anomalyType {
	case "tool_calls_missing":
		a.reporter.ReportToolCallsMissing(
			ctx, requestID, sourceProtocol, targetProtocol,
			rawInput, rawOutput,
			detailString(details, "missing_tool_calls", "upstream tool calls were not emitted"),
			confidence,
		)
	case "semantic_incomplete":
		a.reporter.ReportSemanticIncomplete(
			ctx, requestID, targetProtocol, rawOutput,
			detailString(details, "reason", "response appears incomplete"),
			detailStrings(details, "indicators"), confidence,
		)
	default:
		a.reporter.ReportConversionError(
			ctx, requestID, sourceProtocol, targetProtocol, conversionStep,
			rawInput, fmt.Errorf("%s", detailString(details, "error", anomalyType)),
		)
	}
	return nil
}

func detailString(details map[string]interface{}, key, fallback string) string {
	if value, ok := details[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func detailBytes(details map[string]interface{}, key string) []byte {
	switch value := details[key].(type) {
	case []byte:
		return append([]byte(nil), value...)
	case string:
		return []byte(value)
	default:
		return nil
	}
}

func detailFloat(details map[string]interface{}, key string, fallback float64) float64 {
	if value, ok := details[key].(float64); ok {
		return value
	}
	return fallback
}

func detailStrings(details map[string]interface{}, key string) []string {
	if value, ok := details[key].([]string); ok {
		return append([]string(nil), value...)
	}
	return nil
}

// SemanticAnalyzerAdapter adapts the IR semantic analyzer to Executor.
type SemanticAnalyzerAdapter struct {
	analyzer *ir.SemanticAnalyzer
}

// NewSemanticAnalyzerAdapter creates an adapter for the IR semantic analyzer.
func NewSemanticAnalyzerAdapter(analyzer *ir.SemanticAnalyzer) *SemanticAnalyzerAdapter {
	return &SemanticAnalyzerAdapter{analyzer: analyzer}
}

func (a *SemanticAnalyzerAdapter) AnalyzeRequest(requestID string, irReq interface{}) error {
	return nil
}

func (a *SemanticAnalyzerAdapter) AnalyzeResponse(requestID string, irResp interface{}) (*SemanticAnalysisResult, error) {
	if a.analyzer == nil {
		return nil, nil
	}

	response, ok := irResp.(*ir.InternalResponse)
	if !ok || response == nil {
		return nil, nil
	}
	analysis := a.analyzer.AnalyzeResponse(response)
	return &SemanticAnalysisResult{
		IsIncomplete:          analysis.IsIncomplete,
		Reason:                analysis.Reason,
		Confidence:            analysis.Confidence,
		SuspectedMissingTools: analysis.SuspectedMissingTools,
		Indicators:            append([]string(nil), analysis.Indicators...),
	}, nil
}
