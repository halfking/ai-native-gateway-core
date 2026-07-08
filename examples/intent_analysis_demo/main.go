// Package main 演示如何集成多轮意图分析到现有系统
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/intentconfig"
)

func main() {
	// 1. 创建 logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// 2. 连接数据库
	ctx := context.Background()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// 3. 创建配置管理器
	configMgr := intentconfig.NewManager(pool, logger)
	if err := configMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start config manager: %v", err)
	}
	defer configMgr.Stop()

	// 4. 创建存储层
	evolutionStore := intentconfig.NewPGEvolutionStore(pool, logger)
	feedbackStore := intentconfig.NewPGFeedbackStore(pool, logger)

	// 5. 创建意图分析器
	analyzer := intentconfig.NewAnalyzer(configMgr, evolutionStore, logger)

	// 6. 模拟多轮对话分析
	sessionID := "session_demo_001"
	tenantID := "tenant_demo"

	// 第1轮：用户询问代码问题
	fmt.Println("\n=== 第1轮对话 ===")
	result1, err := analyzer.Analyze(ctx, intentconfig.AnalysisRequest{
		SessionID:     sessionID,
		RequestID:     "req_001",
		TenantID:      tenantID,
		UserContent:   "请帮我实现一个快速排序算法",
		ContextLength: 100,
		HasImages:     false,
		ToolCount:     0,
		StoreContent:  false,
	})
	if err != nil {
		log.Printf("Analysis failed: %v", err)
	} else {
		printAnalysisResult(result1)
	}

	// 第2轮：继续代码相关问题
	fmt.Println("\n=== 第2轮对话 ===")
	result2, err := analyzer.Analyze(ctx, intentconfig.AnalysisRequest{
		SessionID:     sessionID,
		RequestID:     "req_002",
		TenantID:      tenantID,
		UserContent:   "```python\ndef quicksort(arr):\n    if len(arr) <= 1:\n        return arr\n```\n这段代码有什么问题？",
		ContextLength: 200,
		HasImages:     false,
		ToolCount:     0,
		StoreContent:  false,
	})
	if err != nil {
		log.Printf("Analysis failed: %v", err)
	} else {
		printAnalysisResult(result2)
	}

	// 第3轮：意图切换到推理问题
	fmt.Println("\n=== 第3轮对话（意图切换）===")
	result3, err := analyzer.Analyze(ctx, intentconfig.AnalysisRequest{
		SessionID:     sessionID,
		RequestID:     "req_003",
		TenantID:      tenantID,
		UserContent:   "请证明快速排序的平均时间复杂度是O(n log n)",
		ContextLength: 300,
		HasImages:     false,
		ToolCount:     0,
		StoreContent:  false,
	})
	if err != nil {
		log.Printf("Analysis failed: %v", err)
	} else {
		printAnalysisResult(result3)
	}

	// 7. 获取会话摘要
	fmt.Println("\n=== 会话摘要 ===")
	summary, err := analyzer.GetSessionSummary(ctx, sessionID, tenantID)
	if err != nil {
		log.Printf("Get summary failed: %v", err)
	} else {
		printSessionSummary(summary)
	}

	// 8. 模拟保存用户反馈
	fmt.Println("\n=== 保存反馈 ===")
	feedback := &intentconfig.Feedback{
		SessionID:           sessionID,
		RequestID:           "req_001",
		TenantID:            tenantID,
		PredictedIntent:     string(result1.PrimaryIntent.Kind),
		PredictedConfidence: result1.PrimaryConfidence,
		UserAcceptedModel:   boolPtr(true),
		UserRetryCount:      0,
	}
	if err := feedbackStore.Save(ctx, feedback); err != nil {
		log.Printf("Save feedback failed: %v", err)
	} else {
		fmt.Printf("✓ 反馈已保存 (ID: %d)\n", feedback.ID)
	}

	// 9. 演示配置更新
	fmt.Println("\n=== 更新配置 ===")
	cfg := configMgr.GetConfig(tenantID)
	fmt.Printf("当前策略: %s\n", cfg.Strategy)
	fmt.Printf("漂移阈值: %.2f\n", cfg.DriftThreshold)
	fmt.Printf("记忆窗口: %d轮\n", cfg.MultiTurnMemory)

	// 修改配置（示例：降低漂移阈值以更敏感地检测意图变化）
	cfg.DriftThreshold = 0.2
	if err := configMgr.UpdateConfig(ctx, cfg); err != nil {
		log.Printf("Update config failed: %v", err)
	} else {
		fmt.Println("✓ 配置已更新（30秒内生效）")
	}

	fmt.Println("\n=== 集成演示完成 ===")
}

func printAnalysisResult(result *intentconfig.AnalysisResult) {
	fmt.Printf("轮次: %d\n", result.TurnNumber)
	fmt.Printf("主意图: %s (置信度: %.2f, 等级: %s)\n",
		result.PrimaryIntent.Kind,
		result.PrimaryConfidence,
		result.ConfidenceLevel)

	if len(result.Candidates) > 1 {
		fmt.Printf("其他候选:\n")
		for i, c := range result.Candidates[1:] {
			if i >= 3 {
				break // 只显示前3个候选
			}
			fmt.Printf("  - %s: %.2f\n", c.Kind, c.Confidence)
		}
	}

	fmt.Printf("意图漂移: %.3f\n", result.IntentDriftScore)
	if result.IsIntentChanged {
		fmt.Printf("意图切换: %s -> %s (%s)\n",
			result.PreviousIntent,
			result.PrimaryIntent.Kind,
			result.IntentShiftType)
	}
	fmt.Printf("意图稳定性: %.2f\n", result.IntentStability)
	fmt.Printf("分类耗时: %v\n", result.ClassificationLatency)
	fmt.Printf("推荐: %s\n", result.Recommendation)
}

func printSessionSummary(summary *intentconfig.SessionSummary) {
	fmt.Printf("会话ID: %s\n", summary.SessionID)
	fmt.Printf("总轮次: %d\n", summary.TotalTurns)
	fmt.Printf("主导意图: %s\n", summary.DominantIntent)
	fmt.Printf("平均置信度: %.2f\n", summary.AvgConfidence)
	fmt.Printf("稳定性: %.2f\n", summary.Stability)
	fmt.Printf("切换次数: %d\n", summary.SwitchCount)
	fmt.Printf("最新意图: %s\n", summary.LatestIntent)

	fmt.Println("意图分布:")
	for intent, count := range summary.IntentDistribution {
		fmt.Printf("  - %s: %d轮 (%.1f%%)\n",
			intent, count, float64(count)/float64(summary.TotalTurns)*100)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
