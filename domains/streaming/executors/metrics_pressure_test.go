// metrics_pressure_test.go - Phase 2.4 指标函数单元测试

package executors

import "testing"

// TestRecordPressurePenalty 测试惩罚记录函数不会 panic
func TestRecordPressurePenalty(t *testing.T) {
	// 调用多次，确保多次记录也能正常工作
	RecordPressurePenalty("ursm_v2_authoritative", "gpt-4", 0.30)
	RecordPressurePenalty("legacy_state_manager", "claude-opus-4", 0.50)
	RecordPressurePenalty("db_only", "gemini-2.0", 0.0)

	// 无需断言（指标在 Prometheus 中可观察），仅验证不 panic
}

// TestRecordPressureSignal 测试信号记录函数
func TestRecordPressureSignal(t *testing.T) {
	RecordPressureSignal("fp_slots", 123, 0.75)
	RecordPressureSignal("limiter", 456, 0.60)
	RecordPressureSignal("fp_slots", 0, 0.0)

	// 无需断言
}

// TestSetPressureAwareRoutingEnabled 测试 Feature flag 状态切换
func TestSetPressureAwareRoutingEnabled(t *testing.T) {
	// 切换为启用
	SetPressureAwareRoutingEnabled(true)
	// 切换为禁用
	SetPressureAwareRoutingEnabled(false)
	// 再次启用
	SetPressureAwareRoutingEnabled(true)

	// 无需断言
}
