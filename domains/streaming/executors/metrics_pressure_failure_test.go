package executors

import (
	"testing"
)

// TestRecordPressureQueryFailure 测试压力查询失败记录
func TestRecordPressureQueryFailure(t *testing.T) {
	// 测试不应 panic
	RecordPressureQueryFailure("fpslot")
	RecordPressureQueryFailure("limiter")
	// 没有返回值，只要不 panic 就算通过
}

// TestPressureQueryFailureMetrics 测试指标记录
func TestPressureQueryFailureMetrics(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"fpslot failure", "fpslot"},
		{"limiter failure", "limiter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 调用应该成功且不 panic
			RecordPressureQueryFailure(tt.source)
		})
	}
}
