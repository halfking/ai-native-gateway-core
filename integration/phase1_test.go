package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/safety"
)

// 全局 metrics recorder 用于所有测试
var testMetrics = metrics.NewPrometheusRecorder()

func init() {
	metrics.SetGlobal(testMetrics)
}

// TestPhase1Integration_SafetyWithMetrics 测试 Safety + Metrics 集成
func TestPhase1Integration_SafetyWithMetrics(t *testing.T) {
	// Given: Safety Filter + Prometheus Metrics
	safetyFilter := safety.NewContentFilter(safety.BuiltinRules())

	// When: 检测正常内容
	start := time.Now()
	result, err := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
		Content: "Hello, how are you?",
	})
	duration := time.Since(start)

	// Then: 通过检测
	require.NoError(t, err)
	assert.True(t, result.Safe)
	assert.Equal(t, safety.ActionAllow, result.Action)

	// And: Metrics 已记录
	testMetrics.RecordSafetyCheck("request", duration)
	testMetrics.RecordSafetyAction(string(result.Action), "low")

	// When: 检测敏感内容
	start = time.Now()
	result, err = safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
		Content: "My API key is sk-proj1234567890abcdefT3BlbkFJ12345678901234567890",
	})
	duration = time.Since(start)

	// Then: 拦截
	require.NoError(t, err)
	assert.False(t, result.Safe)
	assert.Equal(t, safety.ActionBlock, result.Action)

	// And: Metrics 已记录
	testMetrics.RecordSafetyCheck("request", duration)
	testMetrics.RecordSafetyAction(string(result.Action), "critical")
	for _, hit := range result.Hits {
		testMetrics.RecordSafetyRuleHit(hit.RuleID, hit.RuleName, string(hit.Severity))
	}
}

// TestPhase1Integration_SafetyPIIWithMetrics 测试 PII 检测 + 脱敏 + Metrics
func TestPhase1Integration_SafetyPIIWithMetrics(t *testing.T) {
	// Given
	safetyFilter := safety.NewContentFilter(safety.BuiltinRules())

	// When: 检测 PII
	content := "我的手机号是 13800138000，身份证是 110101199001011234"
	result, err := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
		Content: content,
	})

	// Then: 脱敏
	require.NoError(t, err)
	assert.Equal(t, safety.ActionSanitize, result.Action)
	assert.NotEmpty(t, result.SanitizedContent)
	assert.NotContains(t, result.SanitizedContent, "13800138000")
	assert.NotContains(t, result.SanitizedContent, "110101199001011234")

	// And: Metrics 记录
	testMetrics.RecordSafetyAction(string(result.Action), "high")
	for _, hit := range result.Hits {
		testMetrics.RecordSafetyRuleHit(hit.RuleID, hit.RuleName, string(hit.Severity))
	}
}

// TestPhase1Integration_SafetyUpdateRulesWithMetrics 测试动态规则更新
func TestPhase1Integration_SafetyUpdateRulesWithMetrics(t *testing.T) {
	// Given: 初始空规则
	safetyFilter := safety.NewContentFilter([]safety.Rule{})

	// When: 正常内容应放行
	result, _ := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
		Content: "test keyword",
	})
	assert.True(t, result.Safe)

	// And: 更新规则
	newRules := []safety.Rule{
		{
			ID:       "test_rule",
			Name:     "Test Rule",
			Type:     safety.RuleTypeKeyword,
			Pattern:  "keyword",
			Action:   safety.ActionBlock,
			Severity: safety.SeverityHigh,
			Enabled:  true,
		},
	}
	err := safetyFilter.UpdateRules(newRules)
	require.NoError(t, err)

	// And: 更新 Metrics
	testMetrics.SetSafetyRulesCount(true, len(newRules))

	// When: 再次检测
	result, _ = safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
		Content: "test keyword",
	})

	// Then: 应拦截
	assert.False(t, result.Safe)
	assert.Equal(t, safety.ActionBlock, result.Action)

	// And: Metrics 记录
	testMetrics.RecordSafetyAction(string(result.Action), "high")
}

// TestPhase1Integration_ConcurrentSafetyCheck 测试并发安全检测
func TestPhase1Integration_ConcurrentSafetyCheck(t *testing.T) {
	// Given
	safetyFilter := safety.NewContentFilter(safety.BuiltinRules())

	// When: 100 个并发请求
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			content := "normal content"
			if idx%10 == 0 {
				content = "sk-proj1234567890abcdefT3BlbkFJ12345678901234567890"
			}

			start := time.Now()
			result, err := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
				Content: content,
			})

			assert.NoError(t, err)
			testMetrics.RecordSafetyCheck("request", time.Since(start))
			testMetrics.RecordSafetyAction(string(result.Action), "low")

			done <- true
		}(i)
	}

	// Then: 所有请求完成
	for i := 0; i < 100; i++ {
		<-done
	}
}

// BenchmarkPhase1Integration_E2E Phase 1 端到端性能
func BenchmarkPhase1Integration_E2E(b *testing.B) {
	safetyFilter := safety.NewContentFilter(safety.BuiltinRules())
	content := "Hello, how are you today?"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		result, _ := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
			Content: content,
		})
		duration := time.Since(start)

		testMetrics.RecordSafetyCheck("request", duration)
		testMetrics.RecordSafetyAction(string(result.Action), "low")
	}
}

// BenchmarkPhase1Integration_WithBlock 带拦截的性能
func BenchmarkPhase1Integration_WithBlock(b *testing.B) {
	safetyFilter := safety.NewContentFilter(safety.BuiltinRules())
	content := "My key is sk-proj1234567890abcdefT3BlbkFJ12345678901234567890"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		result, _ := safetyFilter.CheckRequest(context.Background(), &safety.CheckRequest{
			Content: content,
		})
		duration := time.Since(start)

		testMetrics.RecordSafetyCheck("request", duration)
		testMetrics.RecordSafetyAction(string(result.Action), "critical")
		for _, hit := range result.Hits {
			testMetrics.RecordSafetyRuleHit(hit.RuleID, hit.RuleName, string(hit.Severity))
		}
	}
}
