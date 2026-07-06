// bg/session_health_worker_test.go — Tests for Session Health Worker

package bg

import (
	"testing"
)

func TestHealthScoreConfigDefaults(t *testing.T) {
	config := defaultHealthScoreConfig()

	if config.ErrorEndedPenalty != 30 {
		t.Errorf("ErrorEndedPenalty = %d, want 30", config.ErrorEndedPenalty)
	}
	if config.AbandonedPenalty != 15 {
		t.Errorf("AbandonedPenalty = %d, want 15", config.AbandonedPenalty)
	}
	if config.HighLatencyThresholdMs != 5000 {
		t.Errorf("HighLatencyThresholdMs = %d, want 5000", config.HighLatencyThresholdMs)
	}
}

func TestComputeHealthFromFields(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:            10,
		ErrorCount:              2,
		AvgLatencyMs:            2000,
		ModelSwitchCount:        1,
		ComplianceIssuesCount:   0,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	if health.HealthScore < 0 || health.HealthScore > 100 {
		t.Errorf("health score out of range: %d", health.HealthScore)
	}

	if health.HealthGrade == "" {
		t.Error("health grade should not be empty")
	}

	if health.Outcome != "completed" {
		t.Errorf("expected outcome 'completed', got %s", health.Outcome)
	}
}

func TestComputeHealthFromFieldsHighError(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount: 10,
		ErrorCount:   8, // 80% error rate
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	if health.Outcome != "error" {
		t.Errorf("expected outcome 'error', got %s", health.Outcome)
	}

	// Should have error_ended penalty (-30)
	if health.HealthScore > 70 {
		t.Errorf("expected significant penalty for high error rate, got score %d", health.HealthScore)
	}
}

func TestComputeHealthFromFieldsAbandoned(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount: 1,
		ErrorCount:   0,
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	if health.Outcome != "abandoned" {
		t.Errorf("expected outcome 'abandoned', got %s", health.Outcome)
	}

	// Should have abandoned penalty (-15)
	expectedScore := 100 - config.AbandonedPenalty
	if health.HealthScore != expectedScore {
		t.Errorf("health score = %d, want %d", health.HealthScore, expectedScore)
	}
}

func TestComputeHealthFromFieldsHighLatency(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount: 5,
		ErrorCount:   0,
		AvgLatencyMs: 6000, // > 5000ms threshold
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	// Should have high latency penalty (-15)
	if health.HealthScore > 85 {
		t.Errorf("expected latency penalty, got score %d", health.HealthScore)
	}
}

func TestComputeHealthFromFieldsComplianceIssues(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:            5,
		ErrorCount:              0,
		ComplianceIssuesCount:   2,
		PromptInjectionDetected: true,
		PIIDetected:             true,
		ToxicOutputDetected:     true,
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	// Multiple penalties should apply
	if health.HealthScore >= 100 {
		t.Errorf("expected penalties for compliance issues, got score %d", health.HealthScore)
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
		{80, "B"},
		{75, "B"},
		{74, "C"},
		{65, "C"},
		{60, "C"},
		{59, "D"},
		{50, "D"},
		{40, "D"},
		{39, "F"},
		{20, "F"},
		{0, "F"},
	}

	for _, tt := range tests {
		grade := gradeFromScore(tt.score)
		if grade != tt.grade {
			t.Errorf("gradeFromScore(%d) = %s, want %s", tt.score, grade, tt.grade)
		}
	}
}

func TestClassifyOutcome(t *testing.T) {
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
		{"low error rate", 10, 2, "completed"},
		{"exactly 50%", 10, 5, "completed"},
		{"high error rate", 10, 6, "error"},
		{"very high error rate", 10, 9, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := classifyOutcome(tt.requestCount, tt.errorCount)
			if outcome != tt.expected {
				t.Errorf("classifyOutcome(%d, %d) = %s, want %s",
					tt.requestCount, tt.errorCount, outcome, tt.expected)
			}
		})
	}
}

func TestMinFunction(t *testing.T) {
	tests := []struct {
		a, b, expected int
	}{
		{5, 10, 5},
		{10, 5, 5},
		{7, 7, 7},
		{0, 100, 0},
		{-5, 5, -5},
	}

	for _, tt := range tests {
		result := min(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
		}
	}
}

func TestHealthScorePenaltyCaps(t *testing.T) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:          100,
		ErrorCount:            50, // Should trigger per_error_cap (30)
		ComplianceIssuesCount: 10, // Should trigger per_compliance_cap (30)
		PIIDetected:           true,
		ToxicOutputDetected:   true, // Combined should trigger sensitive_cap (30)
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	// Score should not go below 0
	if health.HealthScore < 0 {
		t.Errorf("health score should not be negative, got %d", health.HealthScore)
	}
}

func TestHealthScoreZeroFloor(t *testing.T) {
	// Test that score doesn't go below 0
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:            1,  // abandoned (-15)
		ErrorCount:              1,  // error_ended (-30) + per_error (-3)
		AvgLatencyMs:            6000, // high_latency (-15)
		ModelSwitchCount:        5,  // model_switch (-10)
		ComplianceIssuesCount:   5,  // compliance (-30 cap)
		PromptInjectionDetected: true, // prompt_injection (-20)
		PIIDetected:             true, // pii + toxic (-30 cap)
		ToxicOutputDetected:     true,
	}

	config := defaultHealthScoreConfig()
	health := computeHealthFromFields(summary, config)

	if health.HealthScore != 0 {
		t.Errorf("expected health score to be floored at 0, got %d", health.HealthScore)
	}

	if health.HealthGrade != "F" {
		t.Errorf("expected grade F for score 0, got %s", health.HealthGrade)
	}
}

// Benchmark
func BenchmarkComputeHealthFromFields(b *testing.B) {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:            50,
		ErrorCount:              5,
		AvgLatencyMs:            2000,
		ModelSwitchCount:        2,
		ComplianceIssuesCount:   1,
		PromptInjectionDetected: false,
		PIIDetected:             false,
		ToxicOutputDetected:     false,
	}

	config := defaultHealthScoreConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeHealthFromFields(summary, config)
	}
}
