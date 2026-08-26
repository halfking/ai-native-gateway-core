package metrics

import (
	"sync/atomic"
	"time"
)

// Recorder 是统一的指标记录器接口
type Recorder interface {
	// Circuit Breaker
	RecordCircuitRequest(state string)
	RecordCircuitSuccess()
	RecordCircuitFailure()
	RecordCircuitStateChange(from, to string)
	RecordCircuitTrip()
	ObserveCircuitLatency(duration time.Duration)
	SetCircuitState(state string)
	SetCircuitErrorRate(rate float64)

	// Adapter
	RecordAdapterConversion(provider, direction string, duration time.Duration)
	RecordAdapterFailure(provider, reason string)
	RecordAdapterTokens(provider, tokenType string, count int)
	SetAdapterActive(provider string, active bool)

	// Scheduler
	RecordSchedulerSelection(providerID string, duration time.Duration)
	UpdateSchedulerWeight(providerID string, weight int)
	UpdateSchedulerCurrentWeight(providerID string, weight int)
	UpdateSchedulerEffectiveWeight(providerID string, weight int)
	SetSchedulerAvailableCredentials(count int)

	// Safety
	RecordSafetyCheck(checkType string, duration time.Duration)
	RecordSafetyAction(action, severity string)
	RecordSafetyRuleHit(ruleID, ruleName, severity string)
	RecordSafetyWhitelistHit()
	SetSafetyRulesCount(enabled bool, count int)

	// Pool
	UpdatePoolUtilization(poolID string, utilization float64)
	RecordPoolRequest(poolID, status string)
	SetPoolCapacity(poolID string, capacity int)
	SetPoolActiveCredentials(poolID string, count int)
	SetPoolHealthyCredentials(poolID string, count int)

	// ShadowWrite (P0-2): record failures from best-effort "soft" write paths
	// (attachmentmirror, sessionv2mirror, RingBuffer overflow, raw audit JSONL).
	// kind = "attachment" | "session_v2" | "ringbuffer_drop" | "raw_audit"
	RecordShadowWriteFailure(kind string)
	RecordRingBufferDropped(count uint64)
	RecordRawAuditWriteFailure()

	// StreamSynthesizedDone (P1 hot-patch 2026-08-06): count streams where
	// the gateway had to inject a trailing "data: [DONE]\n\n" frame because
	// the upstream closed without one (observed on MiniMax provider 14 /
	// credential 21 at ~13% of streams as of 2026-07-28). Excludes streams
	// where the upstream sent [DONE] naturally. Lets operator dashboards
	// split "real interruptions" from "minimax-expected-no-DONE" without
	// changing the existing isBenignEOF classification in
	// executor_chat.go:975 and handler.go:5609.
	RecordStreamSynthesizedDone()

	// URSMv2Shadow (P0-3): record what the shadow sidecar did with each
	// outcome during the URSM v2 cutover comparison window. result is
	// "recorded" (URSM v2 accepted the write) | "skipped" (ModeOff /
	// ShadowDoubleWrite off) | "failed" (Redis write error). Operators
	// diff legacy credentialstate log entries vs URSMv2Shadow counts
	// after a 7-day shadow run to confirm < 1% drift before cutover.
	RecordURSMv2ShadowResult(result string)
}

// NoopRecorder 是空实现，用于测试
type NoopRecorder struct{}

func NewNoopRecorder() *NoopRecorder {
	return &NoopRecorder{}
}

func (n *NoopRecorder) RecordCircuitRequest(state string)                                          {}
func (n *NoopRecorder) RecordCircuitSuccess()                                                      {}
func (n *NoopRecorder) RecordCircuitFailure()                                                      {}
func (n *NoopRecorder) RecordCircuitStateChange(from, to string)                                   {}
func (n *NoopRecorder) RecordCircuitTrip()                                                         {}
func (n *NoopRecorder) ObserveCircuitLatency(duration time.Duration)                               {}
func (n *NoopRecorder) SetCircuitState(state string)                                               {}
func (n *NoopRecorder) SetCircuitErrorRate(rate float64)                                           {}
func (n *NoopRecorder) RecordAdapterConversion(provider, direction string, duration time.Duration) {}
func (n *NoopRecorder) RecordAdapterFailure(provider, reason string)                               {}
func (n *NoopRecorder) RecordAdapterTokens(provider, tokenType string, count int)                  {}
func (n *NoopRecorder) SetAdapterActive(provider string, active bool)                              {}
func (n *NoopRecorder) RecordSchedulerSelection(providerID string, duration time.Duration)       {}
func (n *NoopRecorder) UpdateSchedulerWeight(providerID string, weight int)                      {}
func (n *NoopRecorder) UpdateSchedulerCurrentWeight(providerID string, weight int)               {}
func (n *NoopRecorder) UpdateSchedulerEffectiveWeight(providerID string, weight int)             {}
func (n *NoopRecorder) SetSchedulerAvailableCredentials(count int)                                 {}
func (n *NoopRecorder) RecordSafetyCheck(checkType string, duration time.Duration)                 {}
func (n *NoopRecorder) RecordSafetyAction(action, severity string)                                 {}
func (n *NoopRecorder) RecordSafetyRuleHit(ruleID, ruleName, severity string)                      {}
func (n *NoopRecorder) RecordSafetyWhitelistHit()                                                  {}
func (n *NoopRecorder) SetSafetyRulesCount(enabled bool, count int)                                {}
func (n *NoopRecorder) UpdatePoolUtilization(poolID string, utilization float64)                   {}
func (n *NoopRecorder) RecordPoolRequest(poolID, status string)                                    {}
func (n *NoopRecorder) SetPoolCapacity(poolID string, capacity int)                                {}
func (n *NoopRecorder) SetPoolActiveCredentials(poolID string, count int)                          {}
func (n *NoopRecorder) SetPoolHealthyCredentials(poolID string, count int)                         {}

// P0-2 ShadowWrite methods — no-op fallbacks.
func (n *NoopRecorder) RecordShadowWriteFailure(kind string) {}
func (n *NoopRecorder) RecordRingBufferDropped(count uint64) {}
func (n *NoopRecorder) RecordRawAuditWriteFailure()          {}

// P1 hot-patch 2026-08-06: stream synthesized [DONE] terminator counter
// (see Recorder interface comment for rationale).
func (n *NoopRecorder) RecordStreamSynthesizedDone() {}

// P0-3 URSMv2Shadow method — no-op fallback.
func (n *NoopRecorder) RecordURSMv2ShadowResult(result string) {}

// globalRecorder 保存全局默认 Recorder。
//
// 2026-07-27 concurrency fix: 之前是一个裸的包级变量 `var Global Recorder`,
// SetGlobal 无同步地写它，而请求路径同时在读 —— 这是一个真实的数据竞争
// (interface 值是 2 个 word，撕裂读可能拿到不匹配的 type/data 对)。
// 现在用 atomic.Pointer 保存，读写都是原子的。
// Recorder 是 interface，所以存 *Recorder。
var globalRecorder atomic.Pointer[Recorder]

func init() {
	var r Recorder = NewNoopRecorder()
	globalRecorder.Store(&r)
}

// Global 返回当前全局 Recorder。永不返回 nil。
//
// 注意：这里从包级变量改成了访问器函数。全仓 grep 确认包外没有任何
// `metrics.Global` 读取点（只有本包的测试），所以这次收窄不影响调用方。
func Global() Recorder {
	if p := globalRecorder.Load(); p != nil && *p != nil {
		return *p
	}
	return NewNoopRecorder()
}

// SetGlobal 设置全局 Recorder。签名保持不变。
func SetGlobal(r Recorder) {
	if r == nil {
		r = NewNoopRecorder()
	}
	globalRecorder.Store(&r)
}
