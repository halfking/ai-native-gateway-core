package autoroute

import (
	"context"
	"strings"
	"testing"
)

// 2026-09-28 live repro.
//
// The offline matrix test (classifier_prompt_matrix_test.go:49,
// classifier_test.go:478-498) asserts that "用 Python 写一个快速排序" classifies
// as TaskCode. Live probing of the deployed binary with a closely related
// prompt consistently classifies it as TaskChat (conf 0.1, reason
// "default: no strong signal, falling back to chat").
//
// Pin the exact prompt the live probe used so the regression shows up in CI
// rather than only as a runtime surprise.
func TestHeuristicClassifier_LiveRepro_ZhQuickSortStillCode(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	prompts := []string{
		"写一个 Python 快速排序算法并解释时间复杂度",
		"用 Python 写一个快速排序算法",
		"用 Python 写一个快速排序",
		"用python写一个快排算法",
	}
	for _, p := range prompts {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary != TaskCode {
			t.Errorf("expected TaskCode for %q, got %s conf=%.2f reason=%s",
				p, res.Primary, res.Confidence, res.Reason)
		}
		if !strings.Contains(strings.ToLower(res.Reason), "code") &&
			!strings.Contains(strings.ToLower(res.Reason), "coding") {
			t.Errorf("reason for %q should reference the code channel, got %q",
				p, res.Reason)
		}
	}
}