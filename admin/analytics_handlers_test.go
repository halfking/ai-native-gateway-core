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
func TestGetDimensionColumn(t *testing.T) {
	tests := []struct {
		dimension string
		expected  string
	}{
		{"model", "model_name"},
		{"provider", "provider_name"},
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
