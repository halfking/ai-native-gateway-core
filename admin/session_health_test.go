package admin

import (
	"testing"
	"time"
)

func TestComputeHealth_PerfectSession(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:            10,
		SuccessCount:            10,
		ErrorCount:              0,
		AvgLatencyMs:            1000,
		ModelSwitchCount:        1,
		ComplianceIssuesCount:   0,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	if health.HealthScore != 100 {
		t.Errorf("expected score 100, got %d", health.HealthScore)
	}
	if health.HealthGrade != "A" {
		t.Errorf("expected grade A, got %s", health.HealthGrade)
	}
	if health.Outcome != "completed" {
		t.Errorf("expected outcome completed, got %s", health.Outcome)
	}
	if len(health.Penalties) != 0 {
		t.Errorf("expected 0 penalties, got %d", len(health.Penalties))
	}
}

func TestComputeHealth_ErrorDominated(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount: 10,
		SuccessCount: 3,
		ErrorCount:   7,
		AvgLatencyMs: 1000,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: error_ended(-30) + per_error(7*3=21)
	expectedScore := 100 - 30 - 21
	if health.HealthScore != expectedScore {
		t.Errorf("expected score %d, got %d", expectedScore, health.HealthScore)
	}
	if health.Outcome != "error" {
		t.Errorf("expected outcome error, got %s", health.Outcome)
	}
	if health.ErrorRate != 0.7 {
		t.Errorf("expected error rate 0.7, got %f", health.ErrorRate)
	}
}

func TestComputeHealth_Abandoned(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount: 1,
		SuccessCount: 1,
		ErrorCount:   0,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: abandoned(-15)
	if health.HealthScore != 85 {
		t.Errorf("expected score 85, got %d", health.HealthScore)
	}
	if health.Outcome != "abandoned" {
		t.Errorf("expected outcome abandoned, got %s", health.Outcome)
	}
}

func TestComputeHealth_HighLatency(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount: 5,
		SuccessCount: 5,
		ErrorCount:   0,
		AvgLatencyMs: 6200,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: high_latency(-15)
	if health.HealthScore != 85 {
		t.Errorf("expected score 85, got %d", health.HealthScore)
	}

	// 验证 penalties 包含 high_latency
	found := false
	for _, p := range health.Penalties {
		if p.Reason == "high_latency" && p.Deduction == 15 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected high_latency penalty")
	}
}

func TestComputeHealth_ErrorCap(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount: 50,
		ErrorCount:   20, // 20*3=60, but capped at 30
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 验证封顶
	errorPenalty := 0
	for _, p := range health.Penalties {
		if p.Reason == "per_error" {
			errorPenalty = p.Deduction
		}
	}
	if errorPenalty != 30 {
		t.Errorf("expected capped penalty 30, got %d", errorPenalty)
	}
}

func TestComputeHealth_FrequentModelSwitch(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:     10,
		SuccessCount:     10,
		ErrorCount:       0,
		ModelSwitchCount: 5, // > 3
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: frequent_model_switch(-10)
	if health.HealthScore != 90 {
		t.Errorf("expected score 90, got %d", health.HealthScore)
	}

	found := false
	for _, p := range health.Penalties {
		if p.Reason == "frequent_model_switch" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected frequent_model_switch penalty")
	}
}

func TestComputeHealth_PromptInjection(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:            5,
		SuccessCount:            5,
		ErrorCount:              0,
		PromptInjectionDetected: true,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: prompt_injection(-20)
	if health.HealthScore != 80 {
		t.Errorf("expected score 80, got %d", health.HealthScore)
	}
	if health.HealthGrade != "B" {
		t.Errorf("expected grade B, got %s", health.HealthGrade)
	}
}

func TestComputeHealth_ComplianceIssues(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:          10,
		SuccessCount:          10,
		ErrorCount:            0,
		ComplianceIssuesCount: 2, // 2*10=20
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 期望扣分: compliance_issue(-20)
	if health.HealthScore != 80 {
		t.Errorf("expected score 80, got %d", health.HealthScore)
	}
}

func TestComputeHealth_ComplianceCap(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:          10,
		SuccessCount:          10,
		ErrorCount:            0,
		ComplianceIssuesCount: 5, // 5*10=50, capped at 30
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	compliancePenalty := 0
	for _, p := range health.Penalties {
		if p.Reason == "compliance_issue" {
			compliancePenalty = p.Deduction
		}
	}
	if compliancePenalty != 30 {
		t.Errorf("expected capped penalty 30, got %d", compliancePenalty)
	}
}

func TestComputeHealth_SensitiveContent(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:        5,
		SuccessCount:        5,
		ErrorCount:          0,
		PIIDetected:         true,
		ToxicOutputDetected: true,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// PII(15) + Toxic(15) = 30, capped at 30
	sensitiveDeduction := 0
	for _, p := range health.Penalties {
		if p.Reason == "sensitive_content" {
			sensitiveDeduction = p.Deduction
		}
	}
	if sensitiveDeduction != 30 {
		t.Errorf("expected sensitive penalty 30, got %d", sensitiveDeduction)
	}
}

func TestComputeHealth_MultipleIssues(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount:            10,
		SuccessCount:            4,
		ErrorCount:              6, // error_ended(-30) + per_error(6*3=18)
		AvgLatencyMs:            6000, // high_latency(-15)
		ModelSwitchCount:        5, // frequent_model_switch(-10)
		ComplianceIssuesCount:   1, // compliance_issue(-10)
		PromptInjectionDetected: true, // prompt_injection(-20)
		PIIDetected:             true, // sensitive_content(-15)
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	// 总扣分: 30 + 18 + 15 + 10 + 10 + 20 + 15 = 118, but score min 0
	// 实际 score = max(0, 100 - 118) = 0
	if health.HealthScore > 0 {
		t.Errorf("expected score 0, got %d (penalties should exceed 100)", health.HealthScore)
	}
	if health.HealthGrade != "F" {
		t.Errorf("expected grade F, got %s", health.HealthGrade)
	}
	if health.Outcome != "error" {
		t.Errorf("expected outcome error, got %s", health.Outcome)
	}
}

func TestComputeHealth_EmptySession(t *testing.T) {
	summary := AnalyticsSessionSummary{
		RequestCount: 0,
		SuccessCount: 0,
		ErrorCount:   0,
	}

	health := ComputeHealth(summary, DefaultHealthScoreConfig())

	if health.Outcome != "unknown" {
		t.Errorf("expected outcome unknown, got %s", health.Outcome)
	}
	if health.OutcomeReason != "no requests" {
		t.Errorf("expected reason 'no requests', got %s", health.OutcomeReason)
	}
}

func TestGradeFromScore(t *testing.T) {
	tests := []struct {
		score int
		grade string
	}{
		{100, "A"},
		{95, "A"},
		{90, "A"},
		{89, "B"},
		{75, "B"},
		{74, "C"},
		{60, "C"},
		{59, "D"},
		{40, "D"},
		{39, "F"},
		{0, "F"},
	}

	for _, tt := range tests {
		got := gradeFromScore(tt.score)
		if got != tt.grade {
			t.Errorf("gradeFromScore(%d) = %s, want %s", tt.score, got, tt.grade)
		}
	}
}

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name     string
		summary  AnalyticsSessionSummary
		outcome  string
	}{
		{
			name: "no requests",
			summary: AnalyticsSessionSummary{
				RequestCount: 0,
			},
			outcome: "unknown",
		},
		{
			name: "error dominated",
			summary: AnalyticsSessionSummary{
				RequestCount: 10,
				ErrorCount:   6,
			},
			outcome: "error",
		},
		{
			name: "abandoned",
			summary: AnalyticsSessionSummary{
				RequestCount: 1,
				ErrorCount:   0,
			},
			outcome: "abandoned",
		},
		{
			name: "completed",
			summary: AnalyticsSessionSummary{
				RequestCount: 5,
				ErrorCount:   0,
			},
			outcome: "completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, _ := classifyOutcome(tt.summary)
			if outcome != tt.outcome {
				t.Errorf("classifyOutcome() = %s, want %s", outcome, tt.outcome)
			}
		})
	}
}

func TestDefaultHealthScoreConfig(t *testing.T) {
	config := DefaultHealthScoreConfig()

	if config.ErrorEndedPenalty != 30 {
		t.Errorf("expected ErrorEndedPenalty 30, got %d", config.ErrorEndedPenalty)
	}
	if config.HighLatencyThresholdMs != 5000 {
		t.Errorf("expected HighLatencyThresholdMs 5000, got %d", config.HighLatencyThresholdMs)
	}
	if config.SensitivePenaltyCap != 30 {
		t.Errorf("expected SensitivePenaltyCap 30, got %d", config.SensitivePenaltyCap)
	}
}

func TestComputeHealth_CustomConfig(t *testing.T) {
	// 测试自定义配置
	customConfig := HealthScoreConfig{
		ErrorEndedPenalty:      50, // 更严格
		AbandonedPenalty:       20,
		PerErrorPenalty:        5,
		PerErrorCap:            50,
		PerCompliancePenalty:   15,
		PerComplianceCap:       50,
		HighLatencyThresholdMs: 3000, // 更严格
		HighLatencyPenalty:     20,
		ModelSwitchThreshold:   2,
		ModelSwitchPenalty:     15,
		PromptInjectionPenalty: 30,
		PIIPenalty:             20,
		ToxicOutputPenalty:     20,
		SensitivePenaltyCap:    50,
	}

	summary := AnalyticsSessionSummary{
		RequestCount: 10,
		ErrorCount:   6,
		AvgLatencyMs: 3500,
	}

	health := ComputeHealth(summary, customConfig)

	// error_ended(-50) + per_error(6*5=30) + high_latency(-20) = -100
	if health.HealthScore != 0 {
		t.Errorf("expected score 0 with custom config, got %d", health.HealthScore)
	}
}

// Benchmark tests
func BenchmarkComputeHealth(b *testing.B) {
	summary := AnalyticsSessionSummary{
		RequestCount:            10,
		SuccessCount:            8,
		ErrorCount:              2,
		AvgLatencyMs:            2000,
		ModelSwitchCount:        1,
		ComplianceIssuesCount:   0,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}
	config := DefaultHealthScoreConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeHealth(summary, config)
	}
}

// 创建一个辅助函数来构建测试用的 AnalyticsSessionSummary
func createTestSummary(requestCount, errorCount, avgLatencyMs, modelSwitchCount, complianceCount int, promptInjection, pii, toxic bool) AnalyticsSessionSummary {
	return AnalyticsSessionSummary{
		GwSessionID:             "test-session",
		TenantID:                "test-tenant",
		FirstRequestAt:          time.Now().Add(-1 * time.Hour),
		LastRequestAt:           time.Now(),
		RequestCount:            requestCount,
		SuccessCount:            requestCount - errorCount,
		ErrorCount:              errorCount,
		AvgLatencyMs:            avgLatencyMs,
		ModelSwitchCount:        modelSwitchCount,
		ComplianceIssuesCount:   complianceCount,
		PromptInjectionDetected: promptInjection,
		PIIDetected:             pii,
		ToxicOutputDetected:     toxic,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}
}

func TestComputeHealth_RealWorldScenarios(t *testing.T) {
	t.Run("typical good session", func(t *testing.T) {
		summary := createTestSummary(15, 1, 1500, 2, 0, false, false, false)
		health := ComputeHealth(summary, DefaultHealthScoreConfig())

		// 只有 per_error(-3)
		if health.HealthScore != 97 {
			t.Errorf("expected score 97, got %d", health.HealthScore)
		}
		if health.HealthGrade != "A" {
			t.Errorf("expected grade A, got %s", health.HealthGrade)
		}
	})

	t.Run("session with moderate issues", func(t *testing.T) {
		summary := createTestSummary(20, 3, 4000, 2, 1, false, false, false)
		health := ComputeHealth(summary, DefaultHealthScoreConfig())

		// per_error(3*3=9) + compliance_issue(1*10=10) = -19
		expectedScore := 100 - 9 - 10
		if health.HealthScore != expectedScore {
			t.Errorf("expected score %d, got %d", expectedScore, health.HealthScore)
		}
		if health.HealthGrade != "B" {
			t.Errorf("expected grade B, got %s", health.HealthGrade)
		}
	})
}
