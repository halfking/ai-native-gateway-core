// admin/session_health_api_test.go — Tests for Session Health API

package admin

import (
	"testing"
)

func TestComputeAndPersistHealth(t *testing.T) {
	// 测试健康分计算与持久化
	summary := AnalyticsSessionSummary{
		GwSessionID:             "gw_test_123",
		RequestCount:            10,
		SuccessCount:            8,
		ErrorCount:              2,
		AvgLatencyMs:            1500,
		ModelSwitchCount:        1,
		ComplianceIssuesCount:   0,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 验证基本计算
	if health.HealthScore < 0 || health.HealthScore > 100 {
		t.Errorf("health score out of range: %d", health.HealthScore)
	}

	// 验证等级
	expectedGrade := gradeFromScore(health.HealthScore)
	if health.HealthGrade != expectedGrade {
		t.Errorf("grade mismatch: got %s, want %s", health.HealthGrade, expectedGrade)
	}

	// 验证结果分类
	if health.Outcome != "completed" {
		t.Errorf("unexpected outcome: %s", health.Outcome)
	}
}

func TestHealthScoreNoRequests(t *testing.T) {
	summary := AnalyticsSessionSummary{
		GwSessionID:  "gw_empty",
		RequestCount: 0,
		ErrorCount:   0,
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 无请求应该被分类为 unknown
	if health.Outcome != "unknown" {
		t.Errorf("expected outcome 'unknown', got %s", health.Outcome)
	}

	// 应该有 abandoned 扣分（request_count <= 1）
	if health.HealthScore >= 100 {
		t.Errorf("expected penalty for no requests, got score %d", health.HealthScore)
	}
}

func TestHealthScoreHighError(t *testing.T) {
	summary := AnalyticsSessionSummary{
		GwSessionID:  "gw_error",
		RequestCount: 10,
		ErrorCount:   8, // 80% 错误率
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 高错误率应该分类为 error
	if health.Outcome != "error" {
		t.Errorf("expected outcome 'error', got %s", health.Outcome)
	}

	// 应该有 error_ended 扣分（-30）
	expectedMaxScore := 100 - config.ErrorEndedPenalty
	if health.HealthScore > expectedMaxScore {
		t.Errorf("expected score <= %d after error penalty, got %d", expectedMaxScore, health.HealthScore)
	}
}

func TestHealthScoreHighLatency(t *testing.T) {
	summary := AnalyticsSessionSummary{
		GwSessionID:  "gw_slow",
		RequestCount: 5,
		SuccessCount: 5,
		ErrorCount:   0,
		AvgLatencyMs: 6000, // > 5000ms threshold
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 检查是否有高延迟扣分
	hasLatencyPenalty := false
	for _, p := range health.Penalties {
		if p.Reason == "high_latency" {
			hasLatencyPenalty = true
			if p.Deduction != config.HighLatencyPenalty {
				t.Errorf("latency penalty mismatch: got %d, want %d", p.Deduction, config.HighLatencyPenalty)
			}
		}
	}
	if !hasLatencyPenalty {
		t.Error("expected high_latency penalty")
	}
}

func TestHealthScoreCompliance(t *testing.T) {
	summary := AnalyticsSessionSummary{
		GwSessionID:             "gw_compliance",
		RequestCount:            5,
		SuccessCount:            5,
		ErrorCount:              0,
		ComplianceIssuesCount:   2,
		PromptInjectionDetected: true,
		PIIDetected:             true,
		ToxicOutputDetected:     true,
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 应该有多个扣分项
	if len(health.Penalties) == 0 {
		t.Error("expected penalties for compliance issues")
	}

	// 验证分数被扣减
	if health.HealthScore >= 100 {
		t.Errorf("expected score < 100 with compliance issues, got %d", health.HealthScore)
	}
}

func TestHealthGradeMapping(t *testing.T) {
	tests := []struct {
		score int
		grade string
	}{
		{100, "A"},
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
		grade := gradeFromScore(tt.score)
		if grade != tt.grade {
			t.Errorf("gradeFromScore(%d) = %s, want %s", tt.score, grade, tt.grade)
		}
	}
}

func TestOutcomeClassification(t *testing.T) {
	tests := []struct {
		name         string
		requestCount int
		errorCount   int
		expected     string
	}{
		{"no requests", 0, 0, "unknown"},
		{"single success", 1, 0, "abandoned"},
		{"single error", 1, 1, "error"},
		{"normal completion", 10, 0, "completed"},
		{"some errors", 10, 2, "completed"},
		{"high error rate", 10, 6, "error"},
		{"exactly 50%", 10, 5, "completed"}, // 边界情况：50% 不算 error
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := AnalyticsSessionSummary{
				RequestCount: tt.requestCount,
				ErrorCount:   tt.errorCount,
			}
			// classifyOutcome 返回 (outcome, reason)；此处校验 outcome（第一个返回值）。
			outcome, _ := classifyOutcome(summary)
			if outcome != tt.expected {
				t.Errorf("outcome = %s, want %s", outcome, tt.expected)
			}
		})
	}
}

func TestHealthScorePenaltyCaps(t *testing.T) {
	// 测试封顶逻辑
	summary := AnalyticsSessionSummary{
		GwSessionID:           "gw_capped",
		RequestCount:          100,
		ErrorCount:            50,             // 应该触发 per_error_cap (30)
		ComplianceIssuesCount: 10,             // 应该触发 per_compliance_cap (30)
		PIIDetected:           true,           // 15
		ToxicOutputDetected:   true,           // 15, 合计 30 (sensitive_cap)
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 验证 penalty 明细
	penaltySum := 0
	for _, p := range health.Penalties {
		penaltySum += p.Deduction
		switch p.Reason {
		case "per_error":
			if p.Deduction != config.PerErrorCap {
				t.Errorf("per_error should be capped at %d, got %d", config.PerErrorCap, p.Deduction)
			}
		case "compliance_issue":
			if p.Deduction != config.PerComplianceCap {
				t.Errorf("compliance should be capped at %d, got %d", config.PerComplianceCap, p.Deduction)
			}
		case "sensitive_content":
			if p.Deduction != config.SensitivePenaltyCap {
				t.Errorf("sensitive should be capped at %d, got %d", config.SensitivePenaltyCap, p.Deduction)
			}
		}
	}

	expectedScore := 100 - penaltySum
	if expectedScore < 0 {
		expectedScore = 0
	}
	if health.HealthScore != expectedScore {
		t.Errorf("health score = %d, expected %d (100 - %d)", health.HealthScore, expectedScore, penaltySum)
	}
}

func TestHealthScoreErrorRate(t *testing.T) {
	summary := AnalyticsSessionSummary{
		GwSessionID:  "gw_test",
		RequestCount: 10,
		ErrorCount:   3,
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	expectedErrorRate := 0.3
	if health.ErrorRate != expectedErrorRate {
		t.Errorf("error rate = %.2f, want %.2f", health.ErrorRate, expectedErrorRate)
	}
}

// Benchmark tests
func BenchmarkComputeHealthAPI(b *testing.B) {
	summary := AnalyticsSessionSummary{
		GwSessionID:             "gw_bench",
		RequestCount:            50,
		SuccessCount:            45,
		ErrorCount:              5,
		AvgLatencyMs:            2000,
		ModelSwitchCount:        2,
		ComplianceIssuesCount:   1,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}

	config := DefaultHealthScoreConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeHealth(summary, config)
	}
}
