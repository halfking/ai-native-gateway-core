package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BenchmarkConfig 配置
type BenchmarkConfig struct {
	DataSource     string
	SampleSize     int
	Strategies     []string
	EvaluatorModel string
	Output         string
	DryRun         bool
	Verbose        bool
}

// SessionSample 会话采样
type SessionSample struct {
	SessionID       string          `json:"session_id"`
	Messages        []Message       `json:"messages"`
	EstimatedTokens int             `json:"estimated_tokens"`
	ToolCallCount   int             `json:"tool_call_count"`
	Features        SessionFeatures `json:"features,omitempty"`
}

// Message 消息结构
type Message struct {
	Role      string      `json:"role"`
	Content   interface{} `json:"content"`
	ToolCalls interface{} `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// SessionFeatures 会话特征
type SessionFeatures struct {
	MessageCount     int     `json:"message_count"`
	EstimatedTokens  int     `json:"estimated_tokens"`
	HasToolCalls     bool    `json:"has_tool_calls"`
	ToolCallCount    int     `json:"tool_call_count"`
	ToolCallRatio    float64 `json:"tool_call_ratio"`
	AvgMessageLength int     `json:"avg_message_length"`
	HasCodeBlocks    bool    `json:"has_code_blocks"`
	CodeBlockRatio   float64 `json:"code_block_ratio"`
}

// StrategyResult 单个策略的结果
type StrategyResult struct {
	AvgCompressionRatio float64            `json:"avg_compression_ratio"`
	AvgSavingsPercent   float64            `json:"avg_savings_percent"`
	AvgInformationLoss  float64            `json:"avg_information_loss"`
	AvgFidelityScore    float64            `json:"avg_fidelity_score"`
	P50LatencyMs        int64              `json:"p50_latency_ms"`
	P95LatencyMs        int64              `json:"p95_latency_ms"`
	P99LatencyMs        int64              `json:"p99_latency_ms"`
	SuccessCount        int                `json:"success_count"`
	FailureCount        int                `json:"failure_count"`
	Distribution        map[string]int     `json:"distribution"`
	ByType              map[string]*Metrics `json:"by_type"`
}

// Metrics 单次压缩的度量
type Metrics struct {
	OriginalTokens      int     `json:"original_tokens"`
	CompressedTokens    int     `json:"compressed_tokens"`
	CompressionRatio    float64 `json:"compression_ratio"`
	SavingsPercent      float64 `json:"savings_percent"`
	DurationMs          int64   `json:"duration_ms"`
	InformationLossRate float64 `json:"information_loss_rate"`
	FidelityScore       float64 `json:"fidelity_score"`
}

// BenchmarkOutput 输出结果
type BenchmarkOutput struct {
	Timestamp   string                    `json:"timestamp"`
	SampleSize  int                       `json:"sample_size"`
	Strategies  []string                  `json:"strategies"`
	Results     map[string]*StrategyResult `json:"results"`
	SampleStats *SampleStats               `json:"sample_stats"`
}

// SampleStats 采样统计
type SampleStats struct {
	TotalSessions   int            `json:"total_sessions"`
	Stratification  map[string]int `json:"stratification"`
	AvgTokens       float64        `json:"avg_tokens"`
	AvgMessageCount float64        `json:"avg_message_count"`
	ToolCallRatio   float64        `json:"tool_call_ratio"`
}

func main() {
	config := parseFlags()
	
	if config.Verbose {
		log.SetFlags(log.Ltime | log.Lmicroseconds)
	} else {
		log.SetFlags(0)
	}
	
	log.Printf("INFO 开始压缩算法 benchmark...")
	
	// 1. 加载数据
	samples, err := loadSamples(config)
	if err != nil {
		log.Fatalf("ERROR 加载数据失败: %v", err)
	}
	log.Printf("INFO 成功加载 %d 个会话样本", len(samples))
	
	// 2. 计算采样统计
	stats := calculateSampleStats(samples)
	printSampleStats(stats)
	
	if config.DryRun {
		log.Printf("INFO Dry-run 模式，跳过压缩执行")
		return
	}
	
	// 3. 执行 benchmark
	results := make(map[string]*StrategyResult)
	for _, strategy := range config.Strategies {
		log.Printf("INFO 开始测试策略: %s", strategy)
		result, err := benchmarkStrategy(context.Background(), strategy, samples, config)
		if err != nil {
			log.Printf("WARN 策略 %s 测试失败: %v", strategy, err)
			continue
		}
		results[strategy] = result
		printStrategyResult(strategy, result)
	}
	
	// 4. 保存结果
	output := &BenchmarkOutput{
		Timestamp:   time.Now().Format(time.RFC3339),
		SampleSize:  len(samples),
		Strategies:  config.Strategies,
		Results:     results,
		SampleStats: stats,
	}
	
	if err := saveOutput(output, config.Output); err != nil {
		log.Fatalf("ERROR 保存结果失败: %v", err)
	}
	
	log.Printf("SUCCESS Benchmark 完成")
}

func parseFlags() *BenchmarkConfig {
	config := &BenchmarkConfig{}
	
	var strategiesStr string
	
	flag.StringVar(&config.DataSource, "data-source", "", "245 日志数据路径 (必需)")
	flag.IntVar(&config.SampleSize, "sample-size", 1000, "采样大小")
	flag.StringVar(&strategiesStr, "strategies", "intelligent", "逗号分隔的策略列表")
	flag.StringVar(&config.EvaluatorModel, "evaluator", "gpt-4o-mini", "LLM 评估器模型")
	flag.StringVar(&config.Output, "output", "compression-results.json", "输出文件路径")
	flag.BoolVar(&config.DryRun, "dry-run", false, "仅显示采样分布")
	flag.BoolVar(&config.Verbose, "verbose", false, "详细输出")
	
	flag.Parse()
	
	if config.DataSource == "" {
		fmt.Fprintf(os.Stderr, "错误: --data-source 是必需参数\n\n")
		flag.Usage()
		os.Exit(1)
	}
	
	config.Strategies = strings.Split(strategiesStr, ",")
	for i := range config.Strategies {
		config.Strategies[i] = strings.TrimSpace(config.Strategies[i])
	}
	
	return config
}

func loadSamples(config *BenchmarkConfig) ([]*SessionSample, error) {
	// TODO: 实现真实的日志解析逻辑
	// 当前返回模拟数据用于演示
	
	log.Printf("INFO 从 %s 加载数据...", config.DataSource)
	
	// 检查数据源是否存在
	if _, err := os.Stat(config.DataSource); os.IsNotExist(err) {
		return nil, fmt.Errorf("数据源不存在: %s", config.DataSource)
	}
	
	// 模拟数据（实际应从文件读取 JSONL）
	samples := make([]*SessionSample, 0, config.SampleSize)
	
	// 生成分层采样
	// 短文本会话 (30%)
	for i := 0; i < config.SampleSize*3/10 && i < config.SampleSize; i++ {
		samples = append(samples, &SessionSample{
			SessionID:       fmt.Sprintf("short-%d", i),
			EstimatedTokens: 5000 + i*100,
			Messages:        generateMockMessages(10, 0),
			ToolCallCount:   0,
		})
	}
	
	// 工具密集型会话 (40%)
	for i := 0; i < config.SampleSize*4/10 && len(samples) < config.SampleSize; i++ {
		samples = append(samples, &SessionSample{
			SessionID:       fmt.Sprintf("tool-heavy-%d", i),
			EstimatedTokens: 30000 + i*200,
			Messages:        generateMockMessages(30, 15),
			ToolCallCount:   15,
		})
	}
	
	// 长上下文会话 (30%)
	for i := 0; len(samples) < config.SampleSize; i++ {
		samples = append(samples, &SessionSample{
			SessionID:       fmt.Sprintf("long-context-%d", i),
			EstimatedTokens: 80000 + i*500,
			Messages:        generateMockMessages(100, 5),
			ToolCallCount:   5,
		})
	}
	
	// 提取特征
	for _, sample := range samples {
		sample.Features = extractFeatures(sample)
	}
	
	return samples, nil
}

func generateMockMessages(count, toolCalls int) []Message {
	messages := make([]Message, 0, count)
	
	// system message
	messages = append(messages, Message{
		Role:    "system",
		Content: "You are a helpful assistant.",
	})
	
	// 交替的 user/assistant 消息
	toolCallsAdded := 0
	for i := 1; i < count; i++ {
		if i%2 == 1 {
			messages = append(messages, Message{
				Role:    "user",
				Content: fmt.Sprintf("User message %d", i),
			})
		} else {
			msg := Message{
				Role:    "assistant",
				Content: fmt.Sprintf("Assistant response %d", i),
			}
			
			// 添加工具调用
			if toolCallsAdded < toolCalls && i > 5 {
				msg.ToolCalls = []map[string]interface{}{
					{
						"id":   fmt.Sprintf("call_%d", toolCallsAdded),
						"type": "function",
						"function": map[string]string{
							"name":      "read_file",
							"arguments": `{"path": "/tmp/test.txt"}`,
						},
					},
				}
				toolCallsAdded++
			}
			
			messages = append(messages, msg)
		}
	}
	
	return messages
}

func extractFeatures(sample *SessionSample) SessionFeatures {
	features := SessionFeatures{
		MessageCount:    len(sample.Messages),
		EstimatedTokens: sample.EstimatedTokens,
		ToolCallCount:   sample.ToolCallCount,
	}
	
	if features.MessageCount > 0 {
		features.ToolCallRatio = float64(features.ToolCallCount) / float64(features.MessageCount)
		features.HasToolCalls = features.ToolCallCount > 0
		
		totalLen := 0
		codeBlocks := 0
		for _, msg := range sample.Messages {
			if content, ok := msg.Content.(string); ok {
				totalLen += len(content)
				if strings.Contains(content, "```") {
					codeBlocks++
				}
			}
		}
		features.AvgMessageLength = totalLen / features.MessageCount
		features.HasCodeBlocks = codeBlocks > 0
		features.CodeBlockRatio = float64(codeBlocks) / float64(features.MessageCount)
	}
	
	return features
}

func calculateSampleStats(samples []*SessionSample) *SampleStats {
	stats := &SampleStats{
		TotalSessions:  len(samples),
		Stratification: make(map[string]int),
	}
	
	var totalTokens, totalMessages, totalToolCalls int
	
	for _, sample := range samples {
		totalTokens += sample.EstimatedTokens
		totalMessages += len(sample.Messages)
		totalToolCalls += sample.ToolCallCount
		
		// 分类
		if sample.ToolCallCount == 0 && sample.EstimatedTokens < 20000 {
			stats.Stratification["short-text"]++
		} else if sample.ToolCallCount > 10 {
			stats.Stratification["tool-heavy"]++
		} else if sample.EstimatedTokens > 50000 {
			stats.Stratification["long-context"]++
		} else {
			stats.Stratification["mixed"]++
		}
	}
	
	if len(samples) > 0 {
		stats.AvgTokens = float64(totalTokens) / float64(len(samples))
		stats.AvgMessageCount = float64(totalMessages) / float64(len(samples))
		stats.ToolCallRatio = float64(totalToolCalls) / float64(totalMessages)
	}
	
	return stats
}

func printSampleStats(stats *SampleStats) {
	log.Printf("INFO ===============================================")
	log.Printf("INFO 采样统计")
	log.Printf("INFO ===============================================")
	log.Printf("INFO 总会话数:       %d", stats.TotalSessions)
	log.Printf("INFO 平均 token 数:  %.0f", stats.AvgTokens)
	log.Printf("INFO 平均消息数:     %.0f", stats.AvgMessageCount)
	log.Printf("INFO 工具调用占比:   %.2f%%", stats.ToolCallRatio*100)
	log.Printf("INFO ")
	log.Printf("INFO 分层分布:")
	for stratum, count := range stats.Stratification {
		pct := float64(count) / float64(stats.TotalSessions) * 100
		log.Printf("INFO   %-15s: %4d (%.1f%%)", stratum, count, pct)
	}
	log.Printf("INFO ===============================================")
}

func benchmarkStrategy(ctx context.Context, strategy string, samples []*SessionSample, config *BenchmarkConfig) (*StrategyResult, error) {
	result := &StrategyResult{
		Distribution: make(map[string]int),
		ByType:       make(map[string]*Metrics),
	}
	
	latencies := make([]int64, 0, len(samples))
	var totalRatio, totalSavings, totalLoss, totalFidelity float64
	
	for i, sample := range samples {
		if config.Verbose && i%100 == 0 {
			log.Printf("INFO   进度: %d/%d", i, len(samples))
		}
		
		// TODO: 实际调用压缩策略
		// 当前返回模拟结果
		metrics := simulateCompression(strategy, sample)
		
		if metrics != nil {
			result.SuccessCount++
			totalRatio += metrics.CompressionRatio
			totalSavings += metrics.SavingsPercent
			totalLoss += metrics.InformationLossRate
			totalFidelity += metrics.FidelityScore
			latencies = append(latencies, metrics.DurationMs)
			
			// 分布统计
			ratioKey := fmt.Sprintf("%.1f-%.1f", 
				float64(int(metrics.CompressionRatio)), 
				float64(int(metrics.CompressionRatio)+1))
			result.Distribution[ratioKey]++
		} else {
			result.FailureCount++
		}
	}
	
	// 计算平均值
	if result.SuccessCount > 0 {
		result.AvgCompressionRatio = totalRatio / float64(result.SuccessCount)
		result.AvgSavingsPercent = totalSavings / float64(result.SuccessCount)
		result.AvgInformationLoss = totalLoss / float64(result.SuccessCount)
		result.AvgFidelityScore = totalFidelity / float64(result.SuccessCount)
	}
	
	// 计算分位数
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		result.P50LatencyMs = latencies[len(latencies)*50/100]
		result.P95LatencyMs = latencies[len(latencies)*95/100]
		result.P99LatencyMs = latencies[len(latencies)*99/100]
	}
	
	return result, nil
}

func simulateCompression(strategy string, sample *SessionSample) *Metrics {
	// 模拟压缩（实际应调用真实的压缩策略）
	
	var compressionRatio, durationMs float64
	
	switch strategy {
	case "intelligent":
		compressionRatio = 2.5 + float64(sample.ToolCallCount)*0.05
		durationMs = 2000 + float64(sample.EstimatedTokens)*0.01
	case "tool-focused":
		compressionRatio = 2.8 + float64(sample.ToolCallCount)*0.1
		durationMs = 500 + float64(sample.EstimatedTokens)*0.005
	case "rule-based":
		compressionRatio = 2.0
		durationMs = 100 + float64(sample.EstimatedTokens)*0.001
	case "hybrid":
		compressionRatio = 3.0 + float64(sample.ToolCallCount)*0.08
		durationMs = 1500 + float64(sample.EstimatedTokens)*0.008
	default:
		return nil
	}
	
	compressedTokens := int(float64(sample.EstimatedTokens) / compressionRatio)
	savingsPercent := (1 - 1/compressionRatio) * 100
	
	// 模拟信息丢失率（基于压缩比）
	lossRate := 0.05 + (compressionRatio-2.0)*0.03
	if lossRate < 0 {
		lossRate = 0
	}
	if lossRate > 0.3 {
		lossRate = 0.3
	}
	
	fidelityScore := 1.0 - lossRate
	
	return &Metrics{
		OriginalTokens:      sample.EstimatedTokens,
		CompressedTokens:    compressedTokens,
		CompressionRatio:    compressionRatio,
		SavingsPercent:      savingsPercent,
		DurationMs:          int64(durationMs),
		InformationLossRate: lossRate,
		FidelityScore:       fidelityScore,
	}
}

func printStrategyResult(strategy string, result *StrategyResult) {
	log.Printf("INFO ")
	log.Printf("INFO [%s] 结果:", strategy)
	log.Printf("INFO   平均压缩比:     %.2fx", result.AvgCompressionRatio)
	log.Printf("INFO   平均节省率:     %.1f%%", result.AvgSavingsPercent)
	log.Printf("INFO   平均信息丢失:   %.1f%%", result.AvgInformationLoss*100)
	log.Printf("INFO   平均保真度:     %.2f", result.AvgFidelityScore)
	log.Printf("INFO   P50 延迟:       %dms", result.P50LatencyMs)
	log.Printf("INFO   P95 延迟:       %dms", result.P95LatencyMs)
	log.Printf("INFO   P99 延迟:       %dms", result.P99LatencyMs)
	log.Printf("INFO   成功/失败:      %d/%d", result.SuccessCount, result.FailureCount)
}

func saveOutput(output *BenchmarkOutput, path string) error {
	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	
	// 写入 JSON
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	
	return nil
}
