package modelquality

import (
	"context"
	"testing"
	"time"
)

func TestScoreCalculator_CalculateScore(t *testing.T) {
	calculator := &ScoreCalculator{}

	// 构造测试报告
	report := &BenchmarkReport{
		ID:             "test-001",
		ModelName:      "gpt-4",
		Provider:       "openai",
		TotalQuestions: 50,
		CorrectCount:   45,
		Accuracy:       90.0,
		ErrorCount:     0,
		Results: []TestResult{
			{QuestionID: "q1", Latency: 1000, Correct: true},
			{QuestionID: "q2", Latency: 1200, Correct: true},
			{QuestionID: "q3", Latency: 800, Correct: false},
			{QuestionID: "q4", Latency: 1500, Correct: true},
			{QuestionID: "q5", Latency: 900, Correct: true},
		},
	}

	score := calculator.CalculateScore(report)

	// 验证基础字段
	if score.ModelName != "gpt-4" {
		t.Errorf("Expected model name 'gpt-4', got '%s'", score.ModelName)
	}

	if score.Provider != "openai" {
		t.Errorf("Expected provider 'openai', got '%s'", score.Provider)
	}

	if score.Accuracy != 90.0 {
		t.Errorf("Expected accuracy 90.0, got %.2f", score.Accuracy)
	}

	// 验证稳定性
	if score.Stability != 100.0 {
		t.Errorf("Expected stability 100.0, got %.2f", score.Stability)
	}

	// 验证P95延迟
	if score.Latency <= 0 {
		t.Errorf("Expected positive latency, got %.2f", score.Latency)
	}

	// 验证综合评分
	if score.OverallScore <= 0 || score.OverallScore > 100 {
		t.Errorf("Expected overall score between 0-100, got %.2f", score.OverallScore)
	}

	// 验证评级
	if score.Grade == "" {
		t.Error("Expected non-empty grade")
	}
}

func TestScoreCalculator_GradeMapping(t *testing.T) {
	calculator := &ScoreCalculator{}

	tests := []struct {
		score         float64
		expectedGrade string
	}{
		{98.0, "A+"},
		{92.0, "A"},
		{87.0, "B+"},
		{82.0, "B"},
		{75.0, "C"},
		{65.0, "D"},
		{50.0, "F"},
	}

	for _, tt := range tests {
		grade := calculator.scoreToGrade(tt.score)
		if grade != tt.expectedGrade {
			t.Errorf("Score %.2f: expected grade '%s', got '%s'", tt.score, tt.expectedGrade, grade)
		}
	}
}

func TestBenchmarkExecutor_ParseAnswer(t *testing.T) {
	executor := &DefaultBenchmarkExecutor{}

	tests := []struct {
		response string
		expected string
	}{
		{"A", "A"},
		{"B", "B"},
		{"The answer is C", "C"},
		{"I think the answer is D", "D"},
		{"A. This is correct", "A"},
		{"  B  ", "B"},
		{`{"answer": "C"}`, "C"},
		{"Invalid response", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := executor.parseAnswer(tt.response)
		if result != tt.expected {
			t.Errorf("Input '%s': expected '%s', got '%s'", tt.response, tt.expected, result)
		}
	}
}

func TestMMLULiteSuite(t *testing.T) {
	suite := GetMMLULiteSuite()

	if suite.Type != BenchmarkTypeMMLULite {
		t.Errorf("Expected type MMLULite, got %s", suite.Type)
	}

	if len(suite.Questions) != 50 {
		t.Errorf("Expected 50 questions, got %d", len(suite.Questions))
	}

	// 验证题目结构
	for i, q := range suite.Questions {
		if q.ID == "" {
			t.Errorf("Question %d: empty ID", i)
		}
		if q.Subject == "" {
			t.Errorf("Question %d: empty subject", i)
		}
		if q.Question == "" {
			t.Errorf("Question %d: empty question text", i)
		}
		if len(q.Options) != 4 {
			t.Errorf("Question %d: expected 4 options, got %d", i, len(q.Options))
		}
		if q.Answer == "" || q.Answer < "A" || q.Answer > "D" {
			t.Errorf("Question %d: invalid answer '%s'", i, q.Answer)
		}
	}

	// 验证学科分布
	subjects := make(map[string]int)
	for _, q := range suite.Questions {
		subjects[q.Subject]++
	}

	expectedSubjects := []string{"computer_science", "mathematics", "physics", "history", "logic"}
	for _, subject := range expectedSubjects {
		if count, exists := subjects[subject]; !exists || count != 10 {
			t.Errorf("Expected 10 questions for subject '%s', got %d", subject, count)
		}
	}
}

func TestMockModelInvoker(t *testing.T) {
	invoker := NewMockModelInvoker()
	ctx := context.Background()

	response, tokenUsage, latency, err := invoker.InvokeModel(ctx, "openai", "gpt-4", "Test prompt")

	if err != nil {
		t.Logf("Mock invocation returned error (expected occasionally): %v", err)
	} else {
		if response == "" {
			t.Error("Expected non-empty response")
		}
		if tokenUsage <= 0 {
			t.Errorf("Expected positive token usage, got %d", tokenUsage)
		}
		if latency <= 0 {
			t.Errorf("Expected positive latency, got %v", latency)
		}
	}
}

func TestMockModelInvoker_QualityDrop(t *testing.T) {
	invoker := NewMockModelInvoker()
	// 跳过真实 sleep：该测试只验证成功率变化，延迟模拟无意义且会拖慢 200 次调用。
	invoker.sleepFn = func(time.Duration) {}
	ctx := context.Background()

	// 记录初始质量
	successCount := 0
	totalTests := 100

	for i := 0; i < totalTests; i++ {
		_, _, _, err := invoker.InvokeModel(ctx, "openai", "gpt-4", "Test")
		if err == nil {
			successCount++
		}
	}

	initialSuccessRate := float64(successCount) / float64(totalTests)
	t.Logf("Initial success rate: %.2f%%", initialSuccessRate*100)

	// 模拟质量下降
	invoker.SimulateQualityDrop("openai", 0.2, 1000)

	// 再次测试
	successCount = 0
	for i := 0; i < totalTests; i++ {
		_, _, _, err := invoker.InvokeModel(ctx, "openai", "gpt-4", "Test")
		if err == nil {
			successCount++
		}
	}

	finalSuccessRate := float64(successCount) / float64(totalTests)
	t.Logf("Final success rate: %.2f%%", finalSuccessRate*100)

	// 验证成功率下降
	if finalSuccessRate >= initialSuccessRate {
		t.Errorf("Expected success rate to drop, initial=%.2f, final=%.2f", initialSuccessRate, finalSuccessRate)
	}
}

func TestBenchmarkExecutor_Execute(t *testing.T) {
	// 使用mock invoker
	invoker := NewMockModelInvoker()
	executor := NewBenchmarkExecutor(invoker, 5*time.Second)

	// 创建小型测试套件
	suite := &BenchmarkSuite{
		Type:    BenchmarkTypeCustom,
		Name:    "Test Suite",
		Version: "1.0",
		Questions: []Question{
			{
				ID:       "test_001",
				Subject:  "test",
				Question: "Test question 1?",
				Options:  []string{"A", "B", "C", "D"},
				Answer:   "B",
			},
			{
				ID:       "test_002",
				Subject:  "test",
				Question: "Test question 2?",
				Options:  []string{"A", "B", "C", "D"},
				Answer:   "C",
			},
		},
	}

	ctx := context.Background()
	report, err := executor.Execute(ctx, "gpt-4", "openai", suite)

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if report.TotalQuestions != 2 {
		t.Errorf("Expected 2 total questions, got %d", report.TotalQuestions)
	}

	if len(report.Results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(report.Results))
	}

	if report.Status == "" {
		t.Error("Expected non-empty status")
	}

	if report.Duration <= 0 {
		t.Error("Expected positive duration")
	}
}

func TestBenchmarkExecutor_ExecuteWithTimeout(t *testing.T) {
	invoker := NewMockModelInvoker()
	executor := NewBenchmarkExecutor(invoker, 100*time.Millisecond) // 很短的超时

	suite := &BenchmarkSuite{
		Type: BenchmarkTypeCustom,
		Questions: []Question{
			{
				ID:       "test_001",
				Subject:  "test",
				Question: "Test question?",
				Options:  []string{"A", "B", "C", "D"},
				Answer:   "A",
			},
		},
	}

	ctx := context.Background()
	report, err := executor.Execute(ctx, "gpt-4", "openai", suite)

	// 应该有结果（可能有错误）
	if err != nil {
		t.Logf("Execute with short timeout: %v", err)
	}

	if report == nil {
		t.Fatal("Expected non-nil report")
	}
}

func TestMonitorConfig_Validation(t *testing.T) {
	config := &MonitorConfig{
		EnableScheduled:      true,
		ScheduleInterval:     24 * time.Hour,
		UseLiteBenchmark:     true,
		EnableAnomalyTrigger: true,
		ErrorRateThreshold:   0.3,
		LatencyThreshold:     5000,
		AlertOnQualityDrop:   true,
		QualityDropThreshold: 5.0,
		TargetModels: []ModelTarget{
			{
				Provider:  "openai",
				ModelName: "gpt-4",
				Alias:     "GPT-4",
			},
		},
	}

	// 验证配置有效性
	if !config.EnableScheduled {
		t.Error("Expected EnableScheduled to be true")
	}

	if config.ScheduleInterval <= 0 {
		t.Error("Expected positive schedule interval")
	}

	if len(config.TargetModels) == 0 {
		t.Error("Expected at least one target model")
	}
}
