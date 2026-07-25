package main

import (
	"log/slog"
	"os"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// initRawDataLogger 初始化原始数据日志记录器
//
// 通过环境变量配置：
//   - LLM_GATEWAY_RAW_LOG_ENABLED: true/false (默认false，需显式启用)
//   - LLM_GATEWAY_RAW_LOG_DIR: 日志目录路径 (默认 ./logs/raw_data)
//   - LLM_GATEWAY_RAW_LOG_MAX_SIZE: 单文件最大字节数 (默认且上限 200MB)
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
	maxSize := int64(200 * 1024 * 1024) // 200MB default
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

// initAnomalyReporter 初始化异常报告器
//
// 通过环境变量配置：
//   - LLM_GATEWAY_ANOMALY_REPORTER_ENABLED: true/false (默认true)
//   - LLM_GATEWAY_ANOMALY_ENDPOINT: 异常接收端点 URL
func initAnomalyReporter() *logging.AnomalyReporter {
	enabled := os.Getenv("LLM_GATEWAY_ANOMALY_REPORTER_ENABLED") != "false" // 默认启用
	endpoint := os.Getenv("LLM_GATEWAY_ANOMALY_ENDPOINT")

	if endpoint == "" {
		endpoint = "https://llmgo.kxpms.cn/format-anomalies"
	}

	if !enabled {
		slog.Info("anomaly_reporter: disabled by configuration")
		return logging.NewAnomalyReporter("", false)
	}

	reporter := logging.NewAnomalyReporter(endpoint, true)
	slog.Info("anomaly_reporter: initialized", "endpoint", endpoint)

	return reporter
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

// initEnhancedIRTransport 初始化增强的IR转换器（带日志和分析）
//
// 在主程序中调用此函数替代 transformation.NewIRTransport()
func initEnhancedIRTransport() *transformation.IRTransport {
	rawLogger := initRawDataLogger()
	anomalyReporter := initAnomalyReporter()
	semanticAnalyzer := initSemanticAnalyzer()

	transport := transformation.NewIRTransportWithLoggers(
		rawLogger,
		anomalyReporter,
		semanticAnalyzer,
	)

	slog.Info("enhanced_ir_transport: initialized with comprehensive logging and analysis")

	return transport
}

// 示例：在 main.go 中的使用方式
//
// 原代码：
//   irTransport := transformation.NewIRTransport()
//
// 新代码：
//   irTransport := initEnhancedIRTransport()
//
// 环境变量配置示例：
//   export LLM_GATEWAY_RAW_LOG_ENABLED=true
//   export LLM_GATEWAY_RAW_LOG_DIR=/var/log/llm-gateway/raw_data
//   export LLM_GATEWAY_RAW_LOG_MAX_SIZE=209715200  # 200MB
//   export LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true
//   export LLM_GATEWAY_ANOMALY_ENDPOINT=https://llmgo.kxpms.cn/format-anomalies
//   export LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED=true
