package main

import (
	"log/slog"
	"os"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// initRawDataLogger 初始化原始数据日志记录器
//
// 通过环境变量配置：
//   - LLM_GATEWAY_RAW_LOG_ENABLED: true/false (默认false，需显式启用)
//   - LLM_GATEWAY_RAW_LOG_DIR: 日志目录路径 (默认 ./logs/raw_data)
//   - LLM_GATEWAY_RAW_LOG_MAX_SIZE: 单文件最大字节数 (默认且上限 100MB)
//
// 2026-07-28: This helper is still consulted by the historical
// initEnhancedIRTransport path (now removed). The main wiring lives in
// cmd/gateway/main.go:948 onward and uses the default-on async logger.
func initRawDataLogger() *logging.RawDataLogger {
	enabledStr := os.Getenv("LLM_GATEWAY_RAW_LOG_ENABLED")
	enabled := enabledStr == "true"
	if !enabled {
		slog.Info("raw_data_logger: disabled (set LLM_GATEWAY_RAW_LOG_ENABLED=true to enable)")
		return nil
	}

	logDir := os.Getenv("LLM_GATEWAY_RAW_LOG_DIR")
	if logDir == "" {
		logDir = "./logs/raw_data"
	}

	maxSizeStr := os.Getenv("LLM_GATEWAY_RAW_LOG_MAX_SIZE")
	maxSize := int64(100 * 1024 * 1024) // 100MB default
	if maxSizeStr != "" {
		if parsed, err := strconv.ParseInt(maxSizeStr, 10, 64); err == nil {
			maxSize = parsed
		}
	}

	rawLogger, err := logging.NewRawDataLogger(logDir, maxSize, true)
	if err != nil {
		slog.Error("failed to initialize raw_data_logger", "err", err)
		return nil
	}

	slog.Info("raw_data_logger: initialized",
		"log_dir", logDir,
		"max_size_mb", maxSize/(1024*1024))

	return rawLogger
}

// initSemanticAnalyzer 初始化语义分析器
//
// 通过环境变量配置：
//   - LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED: true/false (默认true)
func initSemanticAnalyzer() *ir.SemanticAnalyzer {
	enabled := os.Getenv("LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED") != "false" // 默认启用

	if !enabled {
		slog.Info("semantic_analyzer: disabled by configuration")
		return ir.NewSemanticAnalyzer(false)
	}

	analyzer := ir.NewSemanticAnalyzer(true)
	slog.Info("semantic_analyzer: initialized")

	return analyzer
}

// 2026-07-28: removed initAnomalyReporter (mutex-based reporter is now
// deleted) and initEnhancedIRTransport (dead code: not referenced from
// main.go since the default-on async raw logger + LockFreeAnomalyReporter
// wiring lives inline in main.go).
//
// Environment variables retained:
//   - LLM_GATEWAY_RAW_LOG_DIR
//   - LLM_GATEWAY_RAW_LOG_MAX_SIZE
//   - LLM_GATEWAY_RAW_LOG_ENABLED
//   - LLM_GATEWAY_ANOMALY_REPORTER_ENABLED  → read directly in main.go
//   - LLM_GATEWAY_ANOMALY_ENDPOINT          → read directly in main.go
//   - LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED → handled by initSemanticAnalyzer
