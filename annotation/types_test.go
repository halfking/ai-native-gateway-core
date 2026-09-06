// annotation/types_test.go — 2026-09-06
//
// P2.1标注类型单元测试

package annotation

import (
	"testing"
)

func TestIsValidReason(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected bool
	}{
		{"valid performance", "performance", true},
		{"valid cost", "cost", true},
		{"valid availability", "availability", true},
		{"valid quality", "quality", true},
		{"valid other", "other", true},
		{"valid correct", "correct", true},
		{"invalid empty", "", false},
		{"invalid wrong", "wrong_reason", false},
		{"invalid case", "Performance", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidReason(tt.reason)
			if result != tt.expected {
				t.Errorf("IsValidReason(%q) = %v, want %v", tt.reason, result, tt.expected)
			}
		})
	}
}

func TestValidAnnotationReasons(t *testing.T) {
	reasons := ValidAnnotationReasons()
	
	// 检查数量
	expectedCount := 6
	if len(reasons) != expectedCount {
		t.Errorf("ValidAnnotationReasons() returned %d reasons, want %d", len(reasons), expectedCount)
	}

	// 检查必需的原因
	requiredReasons := []string{"performance", "cost", "availability", "quality", "other", "correct"}
	for _, required := range requiredReasons {
		found := false
		for _, reason := range reasons {
			if reason == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ValidAnnotationReasons() missing required reason: %s", required)
		}
	}
}
