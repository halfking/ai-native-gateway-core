package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelquality"
)

func main() {
	// 命令行参数
	mode := flag.String("mode", "test", "运行模式: test(单次测试) | monitor(持续监控) | history(历史分析) | changes(变化检测) | simulate-drop(模拟质量下降)")
	provider := flag.String("provider", "openai", "供应商名称")
	model := flag.String("model", "gpt-4", "模型名称")
	benchmark := flag.String("benchmark", "lite", "测试套件: lite(快速) | full(完整)")
	dataDir := flag.String("data-dir", "./data/model-quality", "数据存储目录")
	interval := flag.Duration("interval", 24*time.Hour, "监控间隔(仅monitor模式)")
	days := flag.Int("days", 30, "历史分析天数(仅history模式)")
	
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 处理中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n收到中断信号，正在退出...")
		cancel()
	}()

	// 初始化组件
	storage, err := modelquality.NewFileStorage(*dataDir)
	if err != nil {
		fmt.Printf("初始化存储失败: %v\n", err)
		os.Exit(1)
	}

	alertLogFile := filepath.Join(*dataDir, "alerts.log")
	alerter, err := modelquality.NewLogAlerter(alertLogFile)
	if err != nil {
		fmt.Printf("初始化告警器失败: %v\n", err)
		os.Exit(1)
	}

	// 使用模拟调用器(演示)
	invoker := modelquality.NewMockModelInvoker()
	executor := modelquality.NewBenchmarkExecutor(invoker, 30*time.Second)

	switch *mode {
	case "test":
		// 单次测试模式
		runSingleTest(ctx, executor, storage, *provider, *model, *benchmark)
		
	case "monitor":
		// 持续监控模式
		runMonitor(ctx, executor, storage, alerter, *interval)
		
	case "simulate-drop":
		// 模拟质量下降场景
		simulateQualityDrop(ctx, executor, storage, alerter, invoker)
		
	case "history":
		// 历史分析模式
		runHistoryAnalysis(ctx, storage, *provider, *model, *days)
		
	case "changes":
		// 变化检测模式
		runChangeDetection(ctx, storage)
		
	default:
		fmt.Printf("未知模式: %s\n", *mode)
		flag.Usage()
		os.Exit(1)
	}
}

// runSingleTest 运行单次测试
func runSingleTest(ctx context.Context, executor modelquality.BenchmarkExecutor, storage modelquality.MonitorStorage, provider string, modelName string, benchmarkType string) {
	fmt.Printf("=== LLM模型质量检测 ===\n")
	fmt.Printf("供应商: %s\n", provider)
	fmt.Printf("模型: %s\n", modelName)
	fmt.Printf("测试类型: %s\n\n", benchmarkType)

	// 选择测试套件
	var suite *modelquality.BenchmarkSuite
	if benchmarkType == "lite" {
		suite = modelquality.GetMMLULiteSuite()
	} else {
		suite = modelquality.GetMMLUFullSuite()
	}

	fmt.Printf("开始测试，共 %d 道题目...\n", len(suite.Questions))

	// 执行测试
	report, err := executor.Execute(ctx, modelName, provider, suite)
	if err != nil {
		fmt.Printf("测试失败: %v\n", err)
		os.Exit(1)
	}

	// 保存报告
	if err := storage.SaveReport(ctx, report); err != nil {
		fmt.Printf("保存报告失败: %v\n", err)
	}

	// 计算评分
	calculator := &modelquality.ScoreCalculator{}
	score := calculator.CalculateScore(report)

	// 保存评分
	if err := storage.SaveScore(ctx, score); err != nil {
		fmt.Printf("保存评分失败: %v\n", err)
	}

	// 输出结果
	fmt.Printf("\n=== 测试完成 ===\n")
	fmt.Printf("总题数: %d\n", report.TotalQuestions)
	fmt.Printf("正确数: %d\n", report.CorrectCount)
	fmt.Printf("错误数: %d\n", report.ErrorCount)
	fmt.Printf("准确率: %.2f%%\n", report.Accuracy)
	fmt.Printf("平均延迟: %.0fms\n", report.AvgLatency)
	fmt.Printf("总Token: %d\n", report.TotalTokens)
	fmt.Printf("耗时: %s\n", report.Duration)
	fmt.Printf("\n综合评分: %.2f\n", score.OverallScore)
	fmt.Printf("等级: %s\n", score.Grade)
	fmt.Printf("稳定性: %.2f%%\n", score.Stability)
	fmt.Printf("P95延迟: %.0fms\n", score.Latency)

	// 分学科得分
	if len(report.SubjectScores) > 0 {
		fmt.Printf("\n分学科得分:\n")
		for subject, scoreVal := range report.SubjectScores {
			fmt.Printf("  %s: %.2f%%\n", subject, scoreVal)
		}
	}

	fmt.Printf("\n报告ID: %s\n", report.ID)
}

// runMonitor 运行持续监控
func runMonitor(ctx context.Context, executor modelquality.BenchmarkExecutor, storage modelquality.MonitorStorage, alerter modelquality.Alerter, interval time.Duration) {
	fmt.Printf("=== LLM模型质量持续监控 ===\n")
	fmt.Printf("监控间隔: %s\n\n", interval)

	// 配置监控目标 - 使用默认的特色模型列表
	targetModels := modelquality.GetDefaultMonitorModels()
	
	config := &modelquality.MonitorConfig{
		EnableScheduled:      true,
		ScheduleInterval:     interval,
		UseLiteBenchmark:     true,
		EnableAnomalyTrigger: true,
		ErrorRateThreshold:   0.3,
		LatencyThreshold:     5000,
		AlertOnQualityDrop:   true,
		QualityDropThreshold: 5.0, // 准确率下降5%触发告警
		TargetModels:         targetModels,
	}
	
	fmt.Printf("监控模型列表:\n")
	for i, model := range targetModels {
		fmt.Printf("  %d. %s (%s:%s)\n", i+1, model.Alias, model.Provider, model.ModelName)
	}
	fmt.Printf("\n")

	monitor := modelquality.NewQualityMonitor(config, executor, storage, alerter)

	// 启动监控
	if err := monitor.Start(ctx); err != nil {
		fmt.Printf("启动监控失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("监控已启动，按 Ctrl+C 停止...")

	// 等待中断
	<-ctx.Done()
	monitor.Stop()
	fmt.Println("监控已停止")
}

// simulateQualityDrop 模拟质量下降场景
func simulateQualityDrop(ctx context.Context, executor modelquality.BenchmarkExecutor, storage modelquality.MonitorStorage, alerter modelquality.Alerter, invoker *modelquality.MockModelInvoker) {
	fmt.Printf("=== 模拟质量下降场景 ===\n\n")

	provider := "domestic_a"
	modelName := "chatglm-4"

	// 第一次测试(正常质量)
	fmt.Println("阶段1: 初始质量测试...")
	suite := modelquality.GetMMLULiteSuite()
	report1, _ := executor.Execute(ctx, modelName, provider, suite)
	storage.SaveReport(ctx, report1)
	
	calculator := &modelquality.ScoreCalculator{}
	score1 := calculator.CalculateScore(report1)
	storage.SaveScore(ctx, score1)
	
	fmt.Printf("初始准确率: %.2f%%, 评分: %.2f (%s)\n\n", score1.Accuracy, score1.OverallScore, score1.Grade)

	// 模拟质量下降
	fmt.Println("阶段2: 模拟供应商模型降级...")
	invoker.SimulateQualityDrop(provider, 0.15, 1000) // 准确率下降15%，延迟增加1000ms
	time.Sleep(2 * time.Second)

	// 第二次测试(质量下降后)
	fmt.Println("阶段3: 质量下降后测试...")
	report2, _ := executor.Execute(ctx, modelName, provider, suite)
	storage.SaveReport(ctx, report2)
	
	score2 := calculator.CalculateScore(report2)
	storage.SaveScore(ctx, score2)
	
	fmt.Printf("下降后准确率: %.2f%%, 评分: %.2f (%s)\n\n", score2.Accuracy, score2.OverallScore, score2.Grade)

	// 触发告警
	accuracyDrop := score1.Accuracy - score2.Accuracy
	if accuracyDrop > 5.0 {
		alertMsg := fmt.Sprintf(
			"检测到模型质量显著下降!\n"+
			"供应商: %s\n"+
			"模型: %s\n"+
			"准确率: %.2f%% -> %.2f%% (下降 %.2f%%)\n"+
			"综合评分: %.2f (%s) -> %.2f (%s)\n"+
			"延迟: %.0fms -> %.0fms\n"+
			"建议: 立即检查供应商是否更换了底层模型或进行了降级",
			provider, modelName,
			score1.Accuracy, score2.Accuracy, accuracyDrop,
			score1.OverallScore, score1.Grade, score2.OverallScore, score2.Grade,
			score1.Latency, score2.Latency,
		)
		
		alerter.Alert(ctx, "critical", "模型质量渗水告警", alertMsg)
	}

	fmt.Println("\n模拟完成，请查看告警日志")
}

// runHistoryAnalysis 运行历史分析
func runHistoryAnalysis(ctx context.Context, storage modelquality.MonitorStorage, provider string, modelName string, days int) {
	fmt.Printf("=== 模型质量历史分析 ===\n")
	fmt.Printf("模型: %s:%s\n", provider, modelName)
	fmt.Printf("分析周期: 最近%d天\n\n", days)

	analyzer := modelquality.NewHistoryAnalyzer(storage)

	// 获取完整历史
	history, err := analyzer.GetModelHistory(ctx, provider, modelName)
	if err != nil {
		fmt.Printf("获取历史失败: %v\n", err)
		os.Exit(1)
	}

	// 输出基本信息
	fmt.Printf("历史记录统计:\n")
	fmt.Printf("  总测试次数: %d\n", history.TotalRecords)
	fmt.Printf("  首次测试: %s\n", history.FirstTestDate.Format("2006-01-02 15:04:05"))
	fmt.Printf("  最近测试: %s\n", history.LastTestDate.Format("2006-01-02 15:04:05"))
	fmt.Printf("\n")

	// 输出评分信息
	fmt.Printf("评分统计:\n")
	fmt.Printf("  当前评分: %.2f (%s)\n", history.CurrentScore.OverallScore, history.CurrentScore.Grade)
	fmt.Printf("  平均评分: %.2f\n", history.AverageScore)
	fmt.Printf("  最高评分: %.2f (%s) - %s\n", 
		history.BestScore.OverallScore, history.BestScore.Grade, 
		history.BestScore.Timestamp.Format("2006-01-02"))
	fmt.Printf("  最低评分: %.2f (%s) - %s\n", 
		history.WorstScore.OverallScore, history.WorstScore.Grade, 
		history.WorstScore.Timestamp.Format("2006-01-02"))
	fmt.Printf("\n")

	// 输出趋势分析
	if history.Trend != nil {
		fmt.Printf("趋势分析 (%s):\n", history.Trend.Period)
		fmt.Printf("  趋势方向: %s\n", history.Trend.Direction)
		fmt.Printf("  平均分: %.2f\n", history.Trend.AverageScore)
		fmt.Printf("  分数范围: %.2f - %.2f (差%.2f)\n", 
			history.Trend.MinScore, history.Trend.MaxScore, history.Trend.ScoreRange)
		fmt.Printf("  波动性: %.2f (标准差)\n", history.Trend.Volatility)
		if history.Trend.IsVolatile {
			fmt.Printf("  ⚠️ 波动剧烈，质量不稳定\n")
		} else {
			fmt.Printf("  ✓ 波动正常，质量稳定\n")
		}
		fmt.Printf("\n")
	}

	// 输出最近变化
	if len(history.RecentChanges) > 0 {
		fmt.Printf("最近检测到的变化:\n")
		for _, change := range history.RecentChanges {
			fmt.Printf("  类型: %s (%s)\n", change.ChangeType, change.Severity)
			fmt.Printf("  准确率变化: %+.2f%%\n", change.AccuracyChange)
			fmt.Printf("  综合评分变化: %+.2f\n", change.OverallChange)
		}
	} else {
		fmt.Printf("✓ 未检测到显著变化，质量稳定\n")
	}
}

// runChangeDetection 运行变化检测
func runChangeDetection(ctx context.Context, storage modelquality.MonitorStorage) {
	fmt.Printf("=== 全量模型变化检测 ===\n\n")

	analyzer := modelquality.NewHistoryAnalyzer(storage)

	// 获取所有需要监控的模型
	models := modelquality.GetDefaultMonitorModels()
	fmt.Printf("检测 %d 个模型的质量变化...\n\n", len(models))

	// 生成变化报告
	report, err := analyzer.GenerateChangeReport(ctx, models)
	if err != nil {
		fmt.Printf("生成报告失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(report)
	fmt.Println()

	// 获取所有模型历史摘要
	histories, err := analyzer.GetAllModelsHistory(ctx, models)
	if err != nil {
		fmt.Printf("获取历史摘要失败: %v\n", err)
		os.Exit(1)
	}

	if len(histories) > 0 {
		fmt.Printf("\n=== 所有模型质量概览 ===\n\n")
		fmt.Printf("%-20s %-25s %8s %8s %10s %12s\n", 
			"供应商", "模型", "当前评分", "等级", "测试次数", "趋势")
		fmt.Printf("%s\n", string(make([]byte, 90)))

		for _, h := range histories {
			trendIcon := "→"
			if h.Trend != nil {
				switch h.Trend.Direction {
				case "up":
					trendIcon = "↑"
				case "down":
					trendIcon = "↓"
				}
			}

			fmt.Printf("%-20s %-25s %8.2f %8s %10d %12s\n",
				h.Provider, h.ModelName,
				h.CurrentScore.OverallScore, h.CurrentScore.Grade,
				h.TotalRecords, trendIcon)
		}
	}
}
