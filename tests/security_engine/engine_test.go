package security_engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"
	"github.com/kaixuan/llm-gateway-go/settings"
	_ "github.com/lib/pq"
)

// TestResult 测试结果
type TestResult struct {
	TestCaseID       int
	Category         string
	Content          string
	ExpectedIntent   string
	ActualIntent     string
	ExpectedThreats  []string
	ActualThreats    []string
	ExpectedAllow    bool
	ActualAllow      bool
	ExpectedSeverity int
	ActualSeverity   int
	Passed           bool
	ErrorMsg         string
	ExecutionTimeMs  int64
}

// PerformanceMetrics 性能指标
type PerformanceMetrics struct {
	TotalTests       int
	AvgLatencyMs     float64
	MinLatencyMs     int64
	MaxLatencyMs     int64
	P50LatencyMs     int64
	P95LatencyMs     int64
	P99LatencyMs     int64
	ThroughputPerSec float64
}

// AccuracyMetrics 准确度指标
type AccuracyMetrics struct {
	TotalTests     int
	PassedTests    int
	FailedTests    int
	Accuracy       float64
	FalsePositives int     // 误报：安全内容被拦截
	FalseNegatives int     // 漏报：危险内容被放行
	TruePositives  int     // 正确拦截
	TrueNegatives  int     // 正确放行
	Precision      float64 // 精确率 = TP / (TP + FP)
	Recall         float64 // 召回率 = TP / (TP + FN)
	F1Score        float64 // F1 = 2 * (Precision * Recall) / (Precision + Recall)
}

// TestSecurityEngine_Batch 批量测试安全检测引擎
func TestSecurityEngine_Batch(t *testing.T) {
	// 注意：当前版本暂时不写数据库，所有结果保存在内存中
	// 如需持久化，取消下面的注释
	// db := setupDatabase(t)
	// defer db.Close()
	// createAuditTable(t, db)

	// 加载测试数据
	testCases := GetTestDataset()
	t.Logf("加载了 %d 个测试用例", len(testCases))

	// 初始化安全检测引擎
	hook := security.NewSecurityHook(settings.Global)
	config := hook.GetConfig()
	t.Logf("安全引擎配置: mode=%s, intentThresh=%.2f, severityThresh=%d",
		config.Mode, config.IntentConfidenceThresh, config.SeverityThreshold)

	// 执行批量测试
	results := []TestResult{}
	startTime := time.Now()

	for _, tc := range testCases {
		result := runSecurityTest(t, hook, tc)
		results = append(results, result)

		// 保存到审计表（如果数据库可用）
		// saveTestResult(t, db, result)
	}

	totalTime := time.Since(startTime)

	// 性能分析
	perfMetrics := analyzePerformance(results, totalTime)
	printPerformanceMetrics(t, perfMetrics)

	// 准确度分析
	accMetrics := analyzeAccuracy(results)
	printAccuracyMetrics(t, accMetrics)

	// 保存分析报告（如果数据库可用）
	// saveAnalysisReport(t, nil, perfMetrics, accMetrics, results)

	// 生成优化建议
	recommendations := generateRecommendations(perfMetrics, accMetrics, results)
	printRecommendations(t, recommendations)

	// 保存测试结果到JSON文件
	saveResultsToJSON(t, results, perfMetrics, accMetrics, recommendations)
}

// runSecurityTest 运行单个安全测试
func runSecurityTest(t *testing.T, hook *security.SecurityHook, tc SecurityTestCase) TestResult {
	startTime := time.Now()

	ctx := context.Background()
	env := domain.NewRequestEnvelope(ctx, nil)
	env.Metadata = map[string]any{
		"user_content": tc.Content,
	}

	// 执行安全检查
	err := hook.Execute(ctx, env)
	executionTime := time.Since(startTime).Milliseconds()

	// 解析结果
	result := TestResult{
		TestCaseID:       tc.ID,
		Category:         tc.Category,
		Content:          tc.Content,
		ExpectedIntent:   tc.ExpectedIntent,
		ExpectedThreats:  tc.ExpectedThreats,
		ExpectedAllow:    tc.ExpectedAllow,
		ExpectedSeverity: tc.Severity,
		ExecutionTimeMs:  executionTime,
	}

	// 提取实际结果
	if verdict, ok := env.Metadata["security_verdict"].(*security.Verdict); ok {
		result.ActualAllow = verdict.Allow

		if verdict.Intent != nil {
			result.ActualIntent = verdict.Intent.Type
		}

		// 提取威胁类型
		actualThreats := []string{}
		maxSeverity := 0
		for _, threat := range verdict.Threats {
			actualThreats = append(actualThreats, threat.Type)
			if threat.Severity > maxSeverity {
				maxSeverity = threat.Severity
			}
		}
		result.ActualThreats = actualThreats
		result.ActualSeverity = maxSeverity
	} else {
		result.ActualAllow = (err == nil)
	}

	// 判断是否通过
	result.Passed = (result.ActualAllow == result.ExpectedAllow)
	if !result.Passed {
		if result.ExpectedAllow && !result.ActualAllow {
			result.ErrorMsg = "误报: 安全内容被拦截"
		} else {
			result.ErrorMsg = "漏报: 危险内容被放行"
		}
	}

	return result
}

// setupDatabase 设置数据库连接
func setupDatabase(t *testing.T) *sql.DB {
	// 使用 docker exec 方式连接，不需要密码
	// 或者直接跳过数据库连接，使用内存结果
	t.Skip("暂时跳过数据库连接，直接使用内存结果")
	return nil
}

// createAuditTable 创建审计表
func createAuditTable(t *testing.T, db *sql.DB) {
	schema := `
	CREATE TABLE IF NOT EXISTS security_engine_test_results (
		id SERIAL PRIMARY KEY,
		test_case_id INT NOT NULL,
		category TEXT NOT NULL,
		content TEXT NOT NULL,
		expected_intent TEXT,
		actual_intent TEXT,
		expected_threats TEXT[],
		actual_threats TEXT[],
		expected_allow BOOLEAN,
		actual_allow BOOLEAN,
		expected_severity INT,
		actual_severity INT,
		passed BOOLEAN,
		error_msg TEXT,
		execution_time_ms BIGINT,
		tested_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS security_engine_analysis (
		id SERIAL PRIMARY KEY,
		test_run_id TEXT NOT NULL,
		total_tests INT,
		passed_tests INT,
		failed_tests INT,
		accuracy FLOAT,
		false_positives INT,
		false_negatives INT,
		true_positives INT,
		true_negatives INT,
		precision_rate FLOAT,
		recall_rate FLOAT,
		f1_score FLOAT,
		avg_latency_ms FLOAT,
		p95_latency_ms BIGINT,
		p99_latency_ms BIGINT,
		recommendations JSONB,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);
	`

	_, err := db.Exec(schema)
	if err != nil {
		t.Fatalf("创建审计表失败: %v", err)
	}

	t.Log("✓ 审计表创建成功")
}

// saveTestResult 保存测试结果到数据库
func saveTestResult(t *testing.T, db *sql.DB, result TestResult) {
	query := `
		INSERT INTO security_engine_test_results 
		(test_case_id, category, content, expected_intent, actual_intent, 
		 expected_threats, actual_threats, expected_allow, actual_allow,
		 expected_severity, actual_severity, passed, error_msg, execution_time_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`

	_, err := db.Exec(query,
		result.TestCaseID, result.Category, result.Content,
		result.ExpectedIntent, result.ActualIntent,
		result.ExpectedThreats, result.ActualThreats,
		result.ExpectedAllow, result.ActualAllow,
		result.ExpectedSeverity, result.ActualSeverity,
		result.Passed, result.ErrorMsg, result.ExecutionTimeMs)

	if err != nil {
		t.Logf("保存测试结果失败: %v", err)
	}
}

// analyzePerformance 性能分析
func analyzePerformance(results []TestResult, totalTime time.Duration) PerformanceMetrics {
	if len(results) == 0 {
		return PerformanceMetrics{}
	}

	latencies := []int64{}
	var sum int64
	min := results[0].ExecutionTimeMs
	max := results[0].ExecutionTimeMs

	for _, r := range results {
		latencies = append(latencies, r.ExecutionTimeMs)
		sum += r.ExecutionTimeMs
		if r.ExecutionTimeMs < min {
			min = r.ExecutionTimeMs
		}
		if r.ExecutionTimeMs > max {
			max = r.ExecutionTimeMs
		}
	}

	// 计算百分位数（简单排序）
	sorted := make([]int64, len(latencies))
	copy(sorted, latencies)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	p50 := sorted[len(sorted)*50/100]
	p95 := sorted[len(sorted)*95/100]
	p99 := sorted[len(sorted)*99/100]

	avgLatency := float64(sum) / float64(len(results))
	throughput := float64(len(results)) / totalTime.Seconds()

	return PerformanceMetrics{
		TotalTests:       len(results),
		AvgLatencyMs:     avgLatency,
		MinLatencyMs:     min,
		MaxLatencyMs:     max,
		P50LatencyMs:     p50,
		P95LatencyMs:     p95,
		P99LatencyMs:     p99,
		ThroughputPerSec: throughput,
	}
}

// analyzeAccuracy 准确度分析
func analyzeAccuracy(results []TestResult) AccuracyMetrics {
	metrics := AccuracyMetrics{
		TotalTests: len(results),
	}

	for _, r := range results {
		if r.Passed {
			metrics.PassedTests++
		} else {
			metrics.FailedTests++
		}

		// 计算混淆矩阵
		if r.ExpectedAllow && !r.ActualAllow {
			// 误报：安全内容被拦截
			metrics.FalsePositives++
		} else if !r.ExpectedAllow && r.ActualAllow {
			// 漏报：危险内容被放行
			metrics.FalseNegatives++
		} else if !r.ExpectedAllow && !r.ActualAllow {
			// 正确拦截
			metrics.TruePositives++
		} else if r.ExpectedAllow && r.ActualAllow {
			// 正确放行
			metrics.TrueNegatives++
		}
	}

	metrics.Accuracy = float64(metrics.PassedTests) / float64(metrics.TotalTests)

	// 计算精确率和召回率
	if (metrics.TruePositives + metrics.FalsePositives) > 0 {
		metrics.Precision = float64(metrics.TruePositives) / float64(metrics.TruePositives+metrics.FalsePositives)
	}

	if (metrics.TruePositives + metrics.FalseNegatives) > 0 {
		metrics.Recall = float64(metrics.TruePositives) / float64(metrics.TruePositives+metrics.FalseNegatives)
	}

	// 计算 F1 分数
	if (metrics.Precision + metrics.Recall) > 0 {
		metrics.F1Score = 2 * (metrics.Precision * metrics.Recall) / (metrics.Precision + metrics.Recall)
	}

	return metrics
}

// printPerformanceMetrics 打印性能指标
func printPerformanceMetrics(t *testing.T, m PerformanceMetrics) {
	t.Log("\n========== 性能分析 ==========")
	t.Logf("总测试数: %d", m.TotalTests)
	t.Logf("平均延迟: %.2f ms", m.AvgLatencyMs)
	t.Logf("最小延迟: %d ms", m.MinLatencyMs)
	t.Logf("最大延迟: %d ms", m.MaxLatencyMs)
	t.Logf("P50 延迟: %d ms", m.P50LatencyMs)
	t.Logf("P95 延迟: %d ms", m.P95LatencyMs)
	t.Logf("P99 延迟: %d ms", m.P99LatencyMs)
	t.Logf("吞吐量: %.2f req/s", m.ThroughputPerSec)
}

// printAccuracyMetrics 打印准确度指标
func printAccuracyMetrics(t *testing.T, m AccuracyMetrics) {
	t.Log("\n========== 准确度分析 ==========")
	t.Logf("总测试数: %d", m.TotalTests)
	t.Logf("通过: %d (%.1f%%)", m.PassedTests, m.Accuracy*100)
	t.Logf("失败: %d (%.1f%%)", m.FailedTests, (1-m.Accuracy)*100)
	t.Log("\n混淆矩阵:")
	t.Logf("  真阳性 (正确拦截): %d", m.TruePositives)
	t.Logf("  真阴性 (正确放行): %d", m.TrueNegatives)
	t.Logf("  假阳性 (误报): %d", m.FalsePositives)
	t.Logf("  假阴性 (漏报): %d", m.FalseNegatives)
	t.Log("\n评估指标:")
	t.Logf("  准确率 (Accuracy): %.2f%%", m.Accuracy*100)
	t.Logf("  精确率 (Precision): %.2f%%", m.Precision*100)
	t.Logf("  召回率 (Recall): %.2f%%", m.Recall*100)
	t.Logf("  F1 分数: %.2f", m.F1Score)
}

// generateRecommendations 生成优化建议
func generateRecommendations(perf PerformanceMetrics, acc AccuracyMetrics, results []TestResult) []string {
	recommendations := []string{}

	// 性能优化建议
	if perf.AvgLatencyMs > 100 {
		recommendations = append(recommendations,
			fmt.Sprintf("⚠️  平均延迟 %.2fms 较高，建议启用缓存或使用更轻量的LLM模型", perf.AvgLatencyMs))
	}

	if perf.P99LatencyMs > 500 {
		recommendations = append(recommendations,
			fmt.Sprintf("⚠️  P99延迟 %dms 过高，建议并行执行意图分析和威胁检测", perf.P99LatencyMs))
	}

	// 准确度优化建议
	if acc.FalsePositives > 0 {
		recommendations = append(recommendations,
			fmt.Sprintf("⚠️  误报率 %.1f%% ，建议降低 severity_threshold 或调整正则规则",
				float64(acc.FalsePositives)/float64(acc.TotalTests)*100))
	}

	if acc.FalseNegatives > 0 {
		recommendations = append(recommendations,
			fmt.Sprintf("⚠️  漏报率 %.1f%% ，建议增加威胁检测规则或使用更强的LLM模型",
				float64(acc.FalseNegatives)/float64(acc.TotalTests)*100))
	}

	if acc.Precision < 0.95 {
		recommendations = append(recommendations,
			"⚠️  精确率偏低，建议优化威胁检测的置信度阈值")
	}

	if acc.Recall < 0.95 {
		recommendations = append(recommendations,
			"⚠️  召回率偏低，建议增加更多威胁模式匹配规则")
	}

	// 分类别分析
	categoryStats := make(map[string]int)
	categoryFailed := make(map[string]int)
	for _, r := range results {
		categoryStats[r.Category]++
		if !r.Passed {
			categoryFailed[r.Category]++
		}
	}

	for cat, total := range categoryStats {
		failed := categoryFailed[cat]
		failRate := float64(failed) / float64(total)
		if failRate > 0.2 {
			recommendations = append(recommendations,
				fmt.Sprintf("⚠️  类别 '%s' 失败率 %.1f%% ，需要针对性优化", cat, failRate*100))
		}
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "✓ 检测引擎表现良好，暂无优化建议")
	}

	return recommendations
}

// printRecommendations 打印优化建议
func printRecommendations(t *testing.T, recs []string) {
	t.Log("\n========== 优化建议 ==========")
	for _, rec := range recs {
		t.Log(rec)
	}
}

// saveAnalysisReport 保存分析报告
func saveAnalysisReport(t *testing.T, db *sql.DB, perf PerformanceMetrics, acc AccuracyMetrics, results []TestResult) {
	testRunID := fmt.Sprintf("run_%d", time.Now().Unix())

	recs := generateRecommendations(perf, acc, results)
	recsJSON, _ := json.Marshal(recs)

	query := `
		INSERT INTO security_engine_analysis 
		(test_run_id, total_tests, passed_tests, failed_tests, accuracy,
		 false_positives, false_negatives, true_positives, true_negatives,
		 precision_rate, recall_rate, f1_score, avg_latency_ms, p95_latency_ms, 
		 p99_latency_ms, recommendations)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`

	_, err := db.Exec(query, testRunID, acc.TotalTests, acc.PassedTests, acc.FailedTests,
		acc.Accuracy, acc.FalsePositives, acc.FalseNegatives, acc.TruePositives, acc.TrueNegatives,
		acc.Precision, acc.Recall, acc.F1Score, perf.AvgLatencyMs, perf.P95LatencyMs,
		perf.P99LatencyMs, recsJSON)

	if err != nil {
		t.Logf("保存分析报告失败: %v", err)
	} else {
		t.Logf("\n✓ 分析报告已保存到数据库 (test_run_id: %s)", testRunID)
	}
}
