package streaming

import (
	"testing"
)

func TestContinuationDetector_Chinese(t *testing.T) {
	detector := NewContinuationDetector()

	tests := []struct {
		message  string
		expected bool
	}{
		{"请继续", true},
		{"继续写下去", true},
		{"接着说", true},
		{"接下来呢", true},
		{"后面呢", true},
		{"往下", true},
		{"下一步", true},
		{"你好", false},
		{"这是一个很长的句子，不应该被识别为继续请求", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			result := detector.IsContinuation(tt.message)
			if result != tt.expected {
				t.Errorf("IsContinuation(%q) = %v, want %v", tt.message, result, tt.expected)
			}
		})
	}
}

func TestContinuationDetector_English(t *testing.T) {
	detector := NewContinuationDetector()

	tests := []struct {
		message  string
		expected bool
	}{
		{"continue", true},
		{"Continue", true},
		{"please continue", true},
		{"go on", true},
		{"more", true},
		{"keep going", true},
		{"go ahead", true},
		{"hello", false},
		{"This is a long sentence that should not be detected as continuation", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			result := detector.IsContinuation(tt.message)
			if result != tt.expected {
				t.Errorf("IsContinuation(%q) = %v, want %v", tt.message, result, tt.expected)
			}
		})
	}
}

func TestContinuationDetector_NilSafety(t *testing.T) {
	var detector *ContinuationDetector = nil

	// Should not panic
	result := detector.IsContinuation("continue")
	if result {
		t.Error("nil detector should return false")
	}
}

func TestContinuationDetector_CustomKeywords(t *testing.T) {
	detector := NewContinuationDetectorWithKeywords(
		[]string{"下文"},
		[]string{"next"},
	)

	if !detector.IsContinuation("下文") {
		t.Error("expected '下文' to be detected")
	}

	if !detector.IsContinuation("next") {
		t.Error("expected 'next' to be detected")
	}

	if detector.IsContinuation("continue") {
		t.Error("expected 'continue' NOT to be detected with custom keywords")
	}
}
