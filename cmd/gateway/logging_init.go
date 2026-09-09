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
// 现状（2026-09-10）：main 装配已改用默认开启的 async raw logger
// （cmd/gateway/main.go），本函数当前无调用方，仅为保留上述环境变量契约；
// 连同 logging.RawDataLogger 的下线属破坏性清理，另行处理。
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

// 环境变量契约（历史实现已下线，变量读取点如下）：
//   - LLM_GATEWAY_RAW_LOG_DIR / _MAX_SIZE / _ENABLED → initRawDataLogger
//   - LLM_GATEWAY_ANOMALY_REPORTER_ENABLED  → read directly in main.go
//   - LLM_GATEWAY_ANOMALY_ENDPOINT          → read directly in main.go
//   - LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED → handled by initSemanticAnalyzer
