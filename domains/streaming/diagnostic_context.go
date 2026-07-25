// Package streaming provides protocol conversion and streaming capabilities
// for the LLM Gateway.
//
// diagnostic_context.go - Diagnostic context for streaming functions
// Created: 2026-07-26 (Phase 2)
package streaming

import "github.com/kaixuan/llm-gateway-go/domains/streaming/executors"

// DiagnosticContext bundles optional diagnostic components for streaming functions.
// All fields are optional (nil-safe). When non-nil, streaming functions can log
// raw data, report anomalies, or run semantic analysis.
//
// Usage:
//
//	diagnostics := &DiagnosticContext{
//	    RawLogger: routingExec.RawDataLogger,
//	    Anomaly:   routingExec.AnomalyReporter,
//	    Semantic:  routingExec.SemanticAnalyzer,
//	}
//
//	outcome := StreamAnthropicSSEToOpenAI(w, resp, ..., diagnostics)
//
// Streaming functions check for nil before calling:
//
//	if diagnostics != nil && diagnostics.RawLogger != nil {
//	    diagnostics.RawLogger.LogResponse(requestID, "anthropic", body, true)
//	}
type DiagnosticContext struct {
	// RawLogger records raw request/response payloads to disk
	RawLogger executors.RawDataLogger

	// Anomaly reports protocol conversion anomalies to external endpoint
	Anomaly executors.AnomalyReporter

	// Semantic analyzes IR objects for tool calls and content loss
	Semantic executors.SemanticAnalyzer
}
