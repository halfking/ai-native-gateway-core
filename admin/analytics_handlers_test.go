package admin

// analytics_handlers_test.go — 分析详情端点测试 (2026-11-20)

import (
	"testing"
)

// TestIsValidDimension 测试维度验证
func TestIsValidDimension(t *testing.T) {
	tests := []struct {
		dimension string
		expected  bool
	}{
		{"model", true},
		{"provider", true},
		{"client", true},
		{"tenant", true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.dimension, func(t *testing.T) {
			result := isValidDimension(tt.dimension)
			if result != tt.expected {
				t.Errorf("isValidDimension(%q) = %v, want %v", tt.dimension, result, tt.expected)
			}
		})
	}
}

// TestGetDimensionColumn 测试列名映射
//
// 2026-11-23 改钉：旧期望值（model_name/provider_name）钉的是与 usage_facts
// 真实 schema（537 迁移：raw_model_name/provider_id bigint）不符的缺陷行为
// ⇒ 反向锁。改钉真实列；client 维度 usage_facts 无真列，主路径失败由
// request_stats_dim_minute（agent_name）降级接住 ⇒ client_id 作为「主路径
// 必失败 ⇒ 走降级」的哨兵值钉住（见 TestAnalyticsQueries_AlignRealSchema）。
func TestGetDimensionColumn(t *testing.T) {
	tests := []struct {
		dimension string
		expected  string
	}{
		{"model", "raw_model_name"},
		{"provider", "provider_id"},
		{"client", "client_id"},
		{"tenant", "tenant_id"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.dimension, func(t *testing.T) {
			result := getDimensionColumn(tt.dimension)
			if result != tt.expected {
				t.Errorf("getDimensionColumn(%q) = %v, want %v", tt.dimension, result, tt.expected)
			}
		})
	}
}
