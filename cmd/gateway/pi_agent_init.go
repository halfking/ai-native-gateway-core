package main

// PI 智能体接线（2026-11-17 审计轮）。
//
// ★ 背景：PI 智能体（domains/promptinjection 的 error_collector / error_analyzer
//	/ llm_client_gateway）此前**整条链从未接线** ——
//	`grep -rn "NewErrorCollector\|NewErrorAnalyzer" --include=*.go` 在
//	本包外**零命中** ⇒
//	  · 错误收集器从未被构造 ⇒ 检测失败只落一条 slog.Warn；
//	  · 分析器从未被构造 ⇒ 队列里永远没有东西；
//	  · worker 从未启动 ⇒ 即使有队列也不会被处理；
//	  · 管理 API 从未注册 ⇒ 即使有结果也没有出口。
//	⇒ 「错误信息的收集与分析」这条职责链**产出为零**。
//
// ★ 本文件把这条链接上，且**默认关闭**（显式 opt-in）：
//
//	LLMGatewayPIErrorCollectorEnabled=true   开启错误采集（默认 false）
//	LLMGatewayPIErrorAnalysisEnabled=true    开启 LLM 分析 + 后台 worker（默认 false）
//	LLMGatewayPIErrorAnalysisModel=<model>  分析模型（默认取分析端点的默认模型）
//	LLMGatewayPIErrorAnalysisInterval=5m     后台轮询间隔（默认 5m）
//	LLMGatewayPIErrorAnalysisBatchSize=10    单批条数（默认 10）
//	LLMGatewayPIErrorAnalysisMaxDepth=1000   队列深度上限（默认 1000，超出则丢弃新错误）
//
//	★ 分析端点复用本仓**既有正典**约定（cmd/gateway/main_pipeline.go 的
//	  LLM_GATEWAY_ANALYSIS_BASE_URL / LLM_GATEWAY_ANALYSIS_API_KEY +
//	  validateAnalysisEndpoint）⇒
//	  · 不另起一套配置口径；
//	  · 端点合法性由 validateAnalysisEndpoint 把关（禁 userinfo/query/fragment、
//	    强制 https、禁解析到私网/回环，除非显式允许本地）；
//	  · 分析请求**不经过网关自己的请求链路** ⇒ 结构性地消除
//	    「判官载荷被自己的防护拦截 / 误报被收集 → 再分析 → 自放大循环」
//	    这条递归面（2026-11-17 审计轮 P1）。

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// piAgentConfig 是 PI 智能体的运行配置（全部来自环境变量）。
type piAgentConfig struct {
	collectorEnabled bool
	analysisEnabled  bool
	model            string
	interval         time.Duration
	batchSize        int
	maxDepth         int
	// timeoutSec 是单次 LLM 分析调用的超时（秒）。
	//
	// ★ 2026-11-18 根修（245 生产实证）：defaultAnalyzerTimeout 硬编码
	// 60s，而 glm-5.3-flash 这类**推理模型**对分析级大提示词（max_tokens
	// 4000）实测 59s 贴线——生产提示词更大即超时 ⇒ 每条错误重试 3 次
	// 全灭。默认放宽到 150s，可经 LLMGatewayPIErrorAnalysisTimeoutSec 调。
	timeoutSec int
}

// ★ 2026-11-17 审计轮：进程内持有 PI 智能体组件，供管理 API 注册时取用。
//
//	（管理 API 的注册点在 main.go 里比 PI 检测器的构造点**更早**
//	—— admin.NewPromptInjectionHandler 在 3576 行，检测器在 7690 行——
//	所以只能靠进程内变量把两者接起来。）
var (
	piAgentCollector *promptinjection.ErrorCollector
	piAgentAnalyzer  *promptinjection.ErrorAnalyzer

	// piDetectorCoreGlobal 是**在役**的 PI 检测器（v2 pipeline 段构造）。
	//
	//	★ 2026-11-18 PI 分析管理面优化轮：管理面「应用修复」改了
	//	  prompt_injection_rules 之后要立即刷新 detector 的内存规则缓存
	//	  （RefreshRules）⇒ 注册点在 main.go、构造点在 v2 pipeline 段，
	//	  经此变量传递（与 piErrorCollectorGlobal 同款形态）。
	//	  未开 v2 pipeline（默认）时恒 nil ⇒ 刷新回调如实报「未接线」。
	piDetectorCoreGlobal *promptinjection.Detector
)

// setPIAgentComponents 记录 PI 智能体组件（供管理 API 使用）。
func setPIAgentComponents(c *promptinjection.ErrorCollector, a *promptinjection.ErrorAnalyzer) {
	piAgentCollector = c
	piAgentAnalyzer = a
}

// loadPIAgentConfig 从环境变量装配配置。
//
// ★ 默认**全部关闭**：PI 检测链本身在 LLM_GATEWAY_USE_V2_PIPELINE（默认关）
//
//	之内，而错误采集会把**攻击者原文**落库 ⇒ 默认开启会改变数据面行为。
func loadPIAgentConfig() piAgentConfig {
	cfg := piAgentConfig{
		collectorEnabled: envBool("LLMGatewayPIErrorCollectorEnabled", false),
		analysisEnabled:  envBool("LLMGatewayPIErrorAnalysisEnabled", false),
		model:            strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisModel")),
		interval:         5 * time.Minute,
		batchSize:        10,
		maxDepth:         1000,
		timeoutSec:       150,
	}
	if v := strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisInterval")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.interval = d
		} else {
			slog.Warn("pi_agent: invalid interval, using default", "value", v, "default", cfg.interval)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisBatchSize")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			cfg.batchSize = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisMaxDepth")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.maxDepth = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisTimeoutSec")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 && n <= 600 {
			cfg.timeoutSec = n
		} else {
			slog.Warn("pi_agent: invalid analysis timeout (want 5..600s), using default", "value", v, "default_sec", cfg.timeoutSec)
		}
	}
	return cfg
}

// initPIAgent 构造 PI 智能体的三个组件。
//
// ★ 2026-11-17 审计轮（与并行列车的 Phase 1 装配合并后的最终形态）：
//
//	① **采集器始终存在**（哪怕采集功能是关的）⇒
//	   管理面（队列/统计）**始终可读**，运维看到的���「计数为 0」
//	   而不是「404 / 端点不存在」——
//	   「没开采集」与「采集了但队列是空的」在运行期必须可区分。
//	   ★ 关着的时候 `Collect` 是**空操作**（`enabled=false`）⇒
//	     **攻击者原文不会被落库**（这是默认关的真���含义）。
//	② **分析器默认 nil** ⇒ 手动触发端点返回 503（而不是 panic）。
//	③ **分析端点必须指向外部**（复用既有正典
//	   LLM_GATEWAY_ANALYSIS_BASE_URL/API_KEY + validateAnalysisEndpoint）
//	   ⇒ 结构性地消除「误报被收集 → 再分析 → 再误报」的自放大递归
//	   （并行列车 Phase 1 把端点写死成 http://localhost:8080 回环，
//	     那正是这条递归面）。
//
// 返回值可能部分为 nil：
//   - collector 为 nil ⇒ 没有 DB（整个链不可用）。
//   - analyzer 为 nil ⇒ 未开启分析（错误仍然入队，可在管理面查看）。
//   - worker 为 nil ⇒ 同上。
func initPIAgent(
	ctx context.Context,
	sqlDB *sql.DB,
	cfg piAgentConfig,
) (*promptinjection.ErrorCollector, *promptinjection.ErrorAnalyzer, *promptinjection.ErrorAnalysisWorker) {
	if sqlDB == nil {
		return nil, nil, nil
	}

	// ★ 采集器**始终**构造：enabled=false 时它是个**惰性壳**
	// （Collect 空操作、不落库），但统计面与队列面可用。
	collector := promptinjection.NewErrorCollector(sqlDB, cfg.collectorEnabled || cfg.analysisEnabled)
	// ★ 队列深度闸：真正落到实现（此前配置项形同虚设）。
	collector.SetMaxDepth(cfg.maxDepth)
	collector.Start(ctx)

	if !cfg.analysisEnabled {
		slog.Info("pi_agent: error collector constructed (collection disabled by configuration)",
			"collecting", cfg.collectorEnabled, "max_depth", cfg.maxDepth)
		return collector, nil, nil
	}

	// 分析端点：复用既有正典约定 + validateAnalysisEndpoint 把关。
	baseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_BASE_URL"))
	}
	apiKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_API_KEY"))
	}
	allowInsecureLocal := os.Getenv("LLM_GATEWAY_ANALYSIS_ALLOW_INSECURE_LOCAL") == "true"

	if baseURL == "" || apiKey == "" {
		// ★ 不静默：采集仍然工作（有队列、有管理面），只是不做 LLM 分析。
		slog.Warn("pi_agent: error analysis disabled, analysis endpoint is not configured " +
			"(LLM_GATEWAY_ANALYSIS_BASE_URL / LLM_GATEWAY_ANALYSIS_API_KEY); " +
			"errors are still collected and visible on the admin API")
		return collector, nil, nil
	}
	validatedURL, err := validateAnalysisEndpoint(baseURL, allowInsecureLocal)
	if err != nil {
		slog.Warn("pi_agent: error analysis disabled, analysis endpoint rejected", "error", err)
		return collector, nil, nil
	}

	model := cfg.model
	if model == "" {
		model = strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisModel"))
	}
	if model == "" {
		// 端点通常提供默认模型；留空时由服务端决定。
		model = "gpt-4o-mini"
	}

	llmClient := promptinjection.NewGatewayLLMClient(model, "/chat/completions", apiKey, validatedURL)
	// ★ 2026-11-18 根修：分析超时落到 client（此前 60s 硬编码，推理模型
	// 大提示词必超时——245 实测 glm-5.3-flash 59s 贴线、生产即灭）。
	llmClient.SetTimeout(time.Duration(cfg.timeoutSec) * time.Second)
	analyzer := promptinjection.NewErrorAnalyzer(sqlDB, collector, llmClient, model, true)
	// ★ 单批条数：真正落到实现（此前配置项形同虚设）。
	analyzer.SetBatchSize(cfg.batchSize)
	worker := promptinjection.NewErrorAnalysisWorker(analyzer, cfg.interval)
	worker.Start(ctx)

	// F6 修复：定期清理旧的错误分析记录（90 天保留期）。
	//
	//	问题：cleanup_old_error_analysis SQL 函数已定义（977 迁移），但全仓零调用方
	//	⇒ 队列与结果表（含攻击者原文 + LLM 响应）无界增长。
	//	修复：每 24 小时调用一次清理函数，删除超过 90 天的记录。
	bg.Go("pi_agent.cleanup", func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("pi_agent: cleanup task stopping")
				return
			case <-ticker.C:
				var deletedQueue, deletedResults int64
				if err := sqlDB.QueryRowContext(ctx,
					`SELECT * FROM cleanup_old_error_analysis($1)`, 90).Scan(&deletedQueue, &deletedResults); err != nil {
					slog.Warn("pi_agent: cleanup failed", "error", err)
					continue
				}
				if deletedQueue > 0 || deletedResults > 0 {
					slog.Info("pi_agent: cleanup completed",
						"deleted_queue", deletedQueue,
						"deleted_results", deletedResults)
				}
			}
		}
	})

	slog.Info("pi_agent: error analysis enabled",
		"model", model,
		"interval", cfg.interval,
		"batch_size", cfg.batchSize,
		"base_url", validatedURL)

	return collector, analyzer, worker
}
