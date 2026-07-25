package executors

// diagnostic_adapters.go 提供诊断组件的适配器实现，
// 将 internal/logging 和 internal/ir 中的具体类型适配到
// Executor 期望的接口。
//
// 2026-07-26: 诊断功能集成到 Executor（USRM v2 架构）
// Phase 1: 仅实现原始数据日志功能，异常报告和语义分析作为占位符保留。

import (
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// RawDataLoggerAdapter 将 logging.RawDataLogger 适配为 RawDataLogger 接口
type RawDataLoggerAdapter struct {
	logger *logging.RawDataLogger
}

func NewRawDataLoggerAdapter(logger *logging.RawDataLogger) *RawDataLoggerAdapter {
	return &RawDataLoggerAdapter{logger: logger}
}

func (a *RawDataLoggerAdapter) LogRequest(requestID string, protocol string, body []byte) error {
	if a.logger == nil {
		return nil
	}
	// 使用 LogClientRequest 记录客户端请求
	a.logger.LogClientRequest(requestID, protocol, body, nil, "executor")
	return nil
}

func (a *RawDataLoggerAdapter) LogResponse(requestID string, protocol string, body []byte, isStream bool) error {
	if a.logger == nil {
		return nil
	}
	// 使用 LogClientResponse 记录响应
	convStep := "executor"
	if isStream {
		convStep = "executor_stream"
	}
	a.logger.LogClientResponse(requestID, protocol, body, convStep)
	return nil
}

// AnomalyReporterAdapter 将 logging.AnomalyReporter 适配为 AnomalyReporter 接口
// Phase 1: 占位符实现，暂不报告异常（需要更复杂的上下文信息）
type AnomalyReporterAdapter struct {
	reporter *logging.AnomalyReporter
}

func NewAnomalyReporterAdapter(reporter *logging.AnomalyReporter) *AnomalyReporterAdapter {
	return &AnomalyReporterAdapter{reporter: reporter}
}

func (a *AnomalyReporterAdapter) ReportAnomaly(requestID string, anomalyType string, details map[string]interface{}) error {
	if a.reporter == nil {
		return nil
	}
	
	// Phase 1: 简化实现，仅记录日志
	// 完整的异常报告需要在流式处理函数中直接调用 AnomalyReporter 的具体方法
	// 因为它们需要 context.Context 和完整的原始数据
	
	return nil
}

// SemanticAnalyzerAdapter 将 ir.SemanticAnalyzer 适配为 SemanticAnalyzer 接口
// Phase 1: 占位符实现，暂不执行语义分析
type SemanticAnalyzerAdapter struct {
	analyzer *ir.SemanticAnalyzer
}

func NewSemanticAnalyzerAdapter(analyzer *ir.SemanticAnalyzer) *SemanticAnalyzerAdapter {
	return &SemanticAnalyzerAdapter{analyzer: analyzer}
}

func (a *SemanticAnalyzerAdapter) AnalyzeRequest(requestID string, irReq interface{}) error {
	if a.analyzer == nil {
		return nil
	}
	
	// Phase 1: 占位符，语义分析需要在实际转换点集成
	return nil
}

func (a *SemanticAnalyzerAdapter) AnalyzeResponse(requestID string, irResp interface{}) error {
	if a.analyzer == nil {
		return nil
	}
	
	// Phase 1: 占位符，语义分析需要在实际转换点集成
	return nil
}


