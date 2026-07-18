package metrics

import "time"

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
	RecordSchedulerSelection(credentialID string, duration time.Duration)
	UpdateSchedulerWeight(credentialID string, weight int)
	UpdateSchedulerCurrentWeight(credentialID string, weight int)
	UpdateSchedulerEffectiveWeight(credentialID string, weight int)
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
func (n *NoopRecorder) RecordSchedulerSelection(credentialID string, duration time.Duration)       {}
func (n *NoopRecorder) UpdateSchedulerWeight(credentialID string, weight int)                      {}
func (n *NoopRecorder) UpdateSchedulerCurrentWeight(credentialID string, weight int)               {}
func (n *NoopRecorder) UpdateSchedulerEffectiveWeight(credentialID string, weight int)             {}
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

// Global 是全局默认 Recorder
var Global Recorder = NewNoopRecorder()

// SetGlobal 设置全局 Recorder
func SetGlobal(r Recorder) {
	Global = r
}
