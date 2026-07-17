package main

import (
	"testing"
)

// TestValidatorHelpers tests the helper functions
func TestValidatorHelpers(t *testing.T) {
	tests := []struct {
		name     string
		fn       func() interface{}
		expected interface{}
	}{
		{
			name:     "abs with negative",
			fn:       func() interface{} { return abs(-100) },
			expected: int64(100),
		},
		{
			name:     "abs with positive",
			fn:       func() interface{} { return abs(100) },
			expected: int64(100),
		},
		{
			name:     "abs64 with negative",
			fn:       func() interface{} { return abs64(-1.5) },
			expected: float64(1.5),
		},
		{
			name:     "abs64 with positive",
			fn:       func() interface{} { return abs64(1.5) },
			expected: float64(1.5),
		},
		{
			name:     "max64 first larger",
			fn:       func() interface{} { return max64(10.5, 5.2) },
			expected: float64(10.5),
		},
		{
			name:     "max64 second larger",
			fn:       func() interface{} { return max64(5.2, 10.5) },
			expected: float64(10.5),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestValidationReportAllPassed tests the AllPassed method
func TestValidationReportAllPassed(t *testing.T) {
	tests := []struct {
		name     string
		report   *ValidationReport
		expected bool
	}{
		{
			name: "all checks passed",
			report: &ValidationReport{
				Checks: []ValidationCheck{
					{Name: "Check 1", Passed: true},
					{Name: "Check 2", Passed: true},
					{Name: "Check 3", Passed: true},
				},
			},
			expected: true,
		},
		{
			name: "one check failed",
			report: &ValidationReport{
				Checks: []ValidationCheck{
					{Name: "Check 1", Passed: true},
					{Name: "Check 2", Passed: false},
					{Name: "Check 3", Passed: true},
				},
			},
			expected: false,
		},
		{
			name: "all checks failed",
			report: &ValidationReport{
				Checks: []ValidationCheck{
					{Name: "Check 1", Passed: false},
					{Name: "Check 2", Passed: false},
				},
			},
			expected: false,
		},
		{
			name:     "empty report",
			report:   &ValidationReport{Checks: []ValidationCheck{}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.report.AllPassed()
			if got != tt.expected {
				t.Errorf("AllPassed() = %v, want %v", got, tt.expected)
			}
		})
	}
}
