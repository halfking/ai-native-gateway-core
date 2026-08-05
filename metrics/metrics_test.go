package metrics

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
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

	// P0-2 ShadowWrite methods — must not panic on the NoopRecorder.
	r.RecordShadowWriteFailure("attachment")
	r.RecordShadowWriteFailure("session_v2")
	r.RecordRingBufferDropped(3)
	r.RecordRawAuditWriteFailure()
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

// TestPrometheusRecorder_ShadowWriteCounters (P0-2) pins the contract
// that the three new ShadowWrite counters actually move when their
// methods are called. Without this regression test, adding the
// counters in PrometheusRecorder and forgetting to wire them up would
// compile but report zero forever — exactly the "data loss is silent"
// failure mode that R-3.3 / R-3.4 in the 2026-07-28 audit call out.
//
// We check the *delta* before/after a single Inc(), so the test does
// not depend on global state from other tests in the package.
//
// Note: uses the package-level testRecorder (NewPrometheusRecorder uses
// promauto which registers to the default global registry; creating
// a second recorder would panic with "duplicate metrics collector
// registration attempted").
func TestPrometheusRecorder_ShadowWriteCounters(t *testing.T) {
	r := testRecorder

	// helper: read the current counter value via Collect + proto.
	readCounter := func(counter interface{ Write(*dto.Metric) error }) float64 {
		m := &dto.Metric{}
		if err := counter.Write(m); err != nil {
			t.Fatalf("counter.Write: %v", err)
		}
		return m.GetCounter().GetValue()
	}

	// shadowWriteFailed is a *CounterVec — need to read a specific label.
	beforeAttach := readCounter(r.shadowWriteFailed.WithLabelValues("attachment"))
	r.RecordShadowWriteFailure("attachment")
	afterAttach := readCounter(r.shadowWriteFailed.WithLabelValues("attachment"))
	assert.Equal(t, beforeAttach+1, afterAttach, "shadowWriteFailed{attachment} must increment by 1")

	beforeSession := readCounter(r.shadowWriteFailed.WithLabelValues("session_v2"))
	r.RecordShadowWriteFailure("session_v2")
	afterSession := readCounter(r.shadowWriteFailed.WithLabelValues("session_v2"))
	assert.Equal(t, beforeSession+1, afterSession, "shadowWriteFailed{session_v2} must increment by 1")

	beforeRing := readCounter(r.ringBufferDropped)
	r.RecordRingBufferDropped(5)
	afterRing := readCounter(r.ringBufferDropped)
	assert.Equal(t, beforeRing+5, afterRing, "ringBufferDropped must add 5")

	// zero-count is a no-op (avoids spurious churn in metrics scrapes).
	r.RecordRingBufferDropped(0)
	stillRing := readCounter(r.ringBufferDropped)
	assert.Equal(t, afterRing, stillRing, "RecordRingBufferDropped(0) must not increment")

	beforeRaw := readCounter(r.rawAuditWriteFailed)
	r.RecordRawAuditWriteFailure()
	afterRaw := readCounter(r.rawAuditWriteFailed)
	assert.Equal(t, beforeRaw+1, afterRaw, "rawAuditWriteFailed must increment by 1")
}

// TestNoopRecorder_ShadowWriteNoCrash pins that the NoopRecorder
// exposes the three new methods without panic, so tests / dry-runs
// that don't need real Prometheus can use NoopRecorder freely.
func TestNoopRecorder_ShadowWriteNoCrash(t *testing.T) {
	r := NewNoopRecorder()
	r.RecordShadowWriteFailure("attachment")
	r.RecordShadowWriteFailure("session_v2")
	r.RecordRingBufferDropped(7)
	r.RecordRawAuditWriteFailure()
	// Calling the methods on NoopRecorder must not panic — assert.NotPanics
	// documents this contract explicitly so a future refactor that adds
	// e.g. an internal channel and forgets to guard it fails this test.
	assert.NotPanics(t, func() {
		r.RecordShadowWriteFailure("ringbuffer_drop")
	})
}

// TestNoopRecorder_StreamSynthesizedDoneNoCrash pins that the
// NoopRecorder exposes RecordStreamSynthesizedDone without panic —
// mirrors TestNoopRecorder_ShadowWriteNoCrash contract test
// (P1 hot-patch 2026-08-06).
func TestNoopRecorder_StreamSynthesizedDoneNoCrash(t *testing.T) {
	r := NewNoopRecorder()
	assert.NotPanics(t, func() {
		r.RecordStreamSynthesizedDone()
		r.RecordStreamSynthesizedDone()
	})
}

// TestPrometheusRecorder_StreamSynthesizedDoneCounter mirrors
// TestPrometheusRecorder_ShadowWriteCounters for the P1 2026-08-06
// hot-patch: confirms the PrometheusRecorder increments the
// llm_gateway_stream_synthesized_done_total counter by exactly 1 per
// RecordStreamSynthesizedDone call.
func TestPrometheusRecorder_StreamSynthesizedDoneCounter(t *testing.T) {
	r := testRecorder

	readCounter := func(counter interface{ Write(*dto.Metric) error }) float64 {
		m := &dto.Metric{}
		if err := counter.Write(m); err != nil {
			t.Fatalf("counter.Write: %v", err)
		}
		return m.GetCounter().GetValue()
	}

	before := readCounter(r.streamSynthDoneTotal)
	r.RecordStreamSynthesizedDone()
	after := readCounter(r.streamSynthDoneTotal)
	assert.Equal(t, before+1, after, "streamSynthDoneTotal must increment by 1")
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
