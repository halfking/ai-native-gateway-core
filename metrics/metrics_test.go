package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 全局 recorder 用于所有测试
var testRecorder = NewPrometheusRecorder()

// TestNoopRecorder 测试空实现
func TestNoopRecorder(t *testing.T) {
	r := NewNoopRecorder()

	// 应该不会 panic
	r.RecordCircuitRequest("closed")
	r.RecordCircuitSuccess()
	r.RecordCircuitFailure()
	r.RecordCircuitStateChange("closed", "open")
	r.RecordCircuitTrip()
	r.ObserveCircuitLatency(time.Millisecond)
	r.SetCircuitState("open")
	r.SetCircuitErrorRate(0.1)

	r.RecordAdapterConversion("openai", "request", time.Microsecond)
	r.RecordAdapterFailure("openai", "validation")
	r.RecordAdapterTokens("openai", "prompt", 100)
	r.SetAdapterActive("openai", true)

	r.RecordSchedulerSelection("1", time.Microsecond)
	r.UpdateSchedulerWeight("1", 5)
	r.UpdateSchedulerCurrentWeight("1", 10)
	r.UpdateSchedulerEffectiveWeight("1", 5)
	r.SetSchedulerAvailableCredentials(3)

	r.RecordSafetyCheck("request", time.Microsecond)
	r.RecordSafetyAction("block", "high")
	r.RecordSafetyRuleHit("rule1", "Test Rule", "high")
	r.RecordSafetyWhitelistHit()
	r.SetSafetyRulesCount(true, 10)

	r.UpdatePoolUtilization("pool1", 0.8)
	r.RecordPoolRequest("pool1", "success")
	r.SetPoolCapacity("pool1", 100)
	r.SetPoolActiveCredentials("pool1", 50)
	r.SetPoolHealthyCredentials("pool1", 48)
}

// TestPrometheusRecorder 测试 Prometheus 实现
func TestPrometheusRecorder(t *testing.T) {
	r := testRecorder

	// Circuit Breaker
	r.RecordCircuitRequest("closed")
	r.RecordCircuitRequest("open")
	r.RecordCircuitSuccess()
	r.RecordCircuitFailure()
	r.RecordCircuitStateChange("closed", "open")
	r.RecordCircuitTrip()
	r.ObserveCircuitLatency(time.Millisecond)
	r.SetCircuitState("open")
	r.SetCircuitErrorRate(0.1)

	// Adapter
	r.RecordAdapterConversion("openai", "request", time.Microsecond)
	r.RecordAdapterConversion("anthropic", "response", time.Microsecond*2)
	r.RecordAdapterFailure("openai", "validation")
	r.RecordAdapterTokens("openai", "prompt", 100)
	r.RecordAdapterTokens("openai", "completion", 50)
	r.SetAdapterActive("openai", true)
	r.SetAdapterActive("anthropic", false)

	// Scheduler
	r.RecordSchedulerSelection("1", time.Microsecond)
	r.RecordSchedulerSelection("2", time.Microsecond)
	r.UpdateSchedulerWeight("1", 5)
	r.UpdateSchedulerWeight("2", 1)
	r.UpdateSchedulerCurrentWeight("1", 10)
	r.UpdateSchedulerEffectiveWeight("1", 5)
	r.SetSchedulerAvailableCredentials(3)

	// Safety
	r.RecordSafetyCheck("request", time.Microsecond)
	r.RecordSafetyCheck("response", time.Microsecond*2)
	r.RecordSafetyAction("block", "high")
	r.RecordSafetyAction("warn", "low")
	r.RecordSafetyRuleHit("rule1", "API Key Detection", "critical")
	r.RecordSafetyRuleHit("rule2", "PII Detection", "high")
	r.RecordSafetyWhitelistHit()
	r.SetSafetyRulesCount(true, 10)
	r.SetSafetyRulesCount(false, 2)

	// Pool
	r.UpdatePoolUtilization("pool1", 0.8)
	r.RecordPoolRequest("pool1", "success")
	r.RecordPoolRequest("pool1", "failure")
	r.SetPoolCapacity("pool1", 100)
	r.SetPoolActiveCredentials("pool1", 50)
	r.SetPoolHealthyCredentials("pool1", 48)

	// 验证不会 panic
	assert.NotNil(t, r)
}

// TestGlobalRecorder 测试全局 Recorder
func TestGlobalRecorder(t *testing.T) {
	// 默认是 NoopRecorder
	assert.NotNil(t, Global())

	// 设置为 Noop (避免重复注册 Prometheus metrics)
	noop := NewNoopRecorder()
	SetGlobal(noop)
	assert.Equal(t, noop, Global())

	// 使用全局 Recorder
	Global().RecordCircuitRequest("closed")
	Global().RecordCircuitSuccess()

	// 验证不会 panic
	Global().RecordAdapterConversion("openai", "request", time.Millisecond)
	Global().RecordSchedulerSelection("1", time.Microsecond)
}

// BenchmarkPrometheusRecorder_CircuitBreaker Circuit Breaker 指标性能
func BenchmarkPrometheusRecorder_CircuitBreaker(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testRecorder.RecordCircuitRequest("closed")
		testRecorder.RecordCircuitSuccess()
		testRecorder.ObserveCircuitLatency(time.Millisecond)
	}
}

// BenchmarkPrometheusRecorder_Scheduler Scheduler 指标性能
func BenchmarkPrometheusRecorder_Scheduler(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testRecorder.RecordSchedulerSelection("1", time.Microsecond)
		testRecorder.UpdateSchedulerWeight("1", 5)
	}
}

// BenchmarkPrometheusRecorder_Safety Safety 指标性能
func BenchmarkPrometheusRecorder_Safety(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testRecorder.RecordSafetyCheck("request", time.Microsecond)
		testRecorder.RecordSafetyAction("allow", "low")
	}
}
