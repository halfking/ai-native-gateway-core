package streaming

import (
	"strings"
)

// ContinuationDetector detects if a user message is a continuation request
type ContinuationDetector struct {
	keywordsZh []string
	keywordsEn []string
}

// NewContinuationDetector creates a new detector with default keywords
func NewContinuationDetector() *ContinuationDetector {
	return &ContinuationDetector{
		keywordsZh: []string{
			"请继续", "继续", "接着", "接下来",
			"后面呢", "往下", "下一步", "后续",
		},
		keywordsEn: []string{
			"continue", "go on", "please continue",
			"next", "more", "keep going", "go ahead",
		},
	}
}

// NewContinuationDetectorWithKeywords creates a detector with custom keywords
func NewContinuationDetectorWithKeywords(zh, en []string) *ContinuationDetector {
	return &ContinuationDetector{
		keywordsZh: zh,
		keywordsEn: en,
	}
}

// IsContinuation checks if the message is a continuation request
func (cd *ContinuationDetector) IsContinuation(message string) bool {
	if cd == nil || message == "" {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(message))

	// Short messages are more likely to be continuation requests
	// e.g., "继续", "continue"
	if len(lower) < 20 {
		// Check Chinese keywords
		for _, kw := range cd.keywordsZh {
			if strings.Contains(lower, kw) {
				return true
			}
		}

		// Check English keywords
		for _, kw := range cd.keywordsEn {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}

	return false
}
