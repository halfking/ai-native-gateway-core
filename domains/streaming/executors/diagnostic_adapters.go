package executors

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

type rawDataLogger interface {
	LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string)
	LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string)
	LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string)
	LogClientResponse(requestID, protocol string, body []byte, conversionStep string)
}

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

type anomalyReporter interface {
	ReportToolCallsMissing(ctx context.Context, requestID string, sourceProto, targetProto string, rawInput, rawOutput []byte, missingToolCalls string, confidence float64)
	ReportConversionError(ctx context.Context, requestID string, sourceProto, targetProto, step string, rawInput []byte, err error)
	ReportSemanticIncomplete(ctx context.Context, requestID string, protocol string, rawOutput []byte, reason string, indicators []string, confidence float64)
}

// AnomalyReporterAdapter adapts the queued anomaly reporter to the Executor
// interface.
type AnomalyReporterAdapter struct {
	reporter anomalyReporter
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

	switch anomalyType {
	case "tool_calls_missing":
		a.reporter.ReportToolCallsMissing(
			context.Background(), requestID, sourceProtocol, targetProtocol,
			rawInput, rawOutput,
			detailString(details, "missing_tool_calls", "upstream tool calls were not emitted"),
			confidence,
		)
	case "semantic_incomplete":
		a.reporter.ReportSemanticIncomplete(
			context.Background(), requestID, targetProtocol, rawOutput,
			detailString(details, "reason", "response appears incomplete"),
			detailStrings(details, "indicators"), confidence,
		)
	default:
		a.reporter.ReportConversionError(
			context.Background(), requestID, sourceProtocol, targetProtocol, conversionStep,
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
