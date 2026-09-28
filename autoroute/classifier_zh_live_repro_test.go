package autoroute

import (
	"context"
	"strings"
	"testing"
)

// 2026-09-28 live repro, round 1+2.
//
// Round 1: The offline matrix test (classifier_prompt_matrix_test.go:49,
// classifier_test.go:478-498) asserts that "用 Python 写一个快速排序" classifies
// as TaskCode. Live probing of the deployed binary with a closely related
// prompt consistently classified it as TaskChat (conf 0.1, reason
// "default: no strong signal, falling back to chat"). Pattern P1 was tightened
// to allow an interleaved language/framework name between the verb and the
// programming object.
//
// Round 2 (extension): two more live misses found in the same session:
//   - "帮我写一个 Python 解析 CSV 的脚本" stayed in chat because P3 only
//     matched "写个/做个/帮我写个/帮我做个/写一个简单". Extended to "帮我写一个
//     /帮我做(一个)?/写一个简单" with the same shared language vocabulary.
//   - "classify sentiment: this store is okay" was hijacked by the
//     "import/include statement" pattern because the test prompt carried
//     `db.query(...)` in some earlier runs; the bare prompt then fell through
//     to code 0.40 via a different path. Added explicit intent classification
//     keywords for the "classify sentiment / classify tone / classify polarity"
//     verb+object variants so the Channel 1.5 hard override fires before the
//     keyword scan.
//
// Pin all three regressions at once so future keyword/pattern changes show up
// in CI rather than only as runtime surprises during a manual probe.
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

func TestHeuristicClassifier_LiveRepro_BangWoXieYiGe(t *testing.T) {
	// "帮我写一个 <LANG> 解析 CSV 的脚本" — P3 colloquial-colloquial gate.
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	prompts := []string{
		"帮我写一个 Python 解析 CSV 的脚本",
		"帮我写一个 SQL 查询",
		"帮我写一个 Go 锁的实现",
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
	}
}

func TestHeuristicClassifier_LiveRepro_ClassifySentiment(t *testing.T) {
	// "classify sentiment: …" — Channel 1.5 hard override for intent
	// classification. The Chinese "情感分类" has been on the keyword list
	// since 2026-09-14; the English verb+object form was missing.
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	prompts := []string{
		"classify sentiment: this store is okay",
		"classify tone: the reply is friendly",
		"classify polarity of: terrible service",
	}
	for _, p := range prompts {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary != TaskIntentClassification {
			t.Errorf("expected TaskIntentClassification for %q, got %s conf=%.2f reason=%s",
				p, res.Primary, res.Confidence, res.Reason)
		}
	}
}

func TestHeuristicClassifier_LiveRepro_NoSpaceLanguageAndCreativeFP(t *testing.T) {
	// R73 审计 M-8 + M-4 回归钉。
	// 正例：零空格语言名（"写一个Python快速排序"）此前漏归 chat（M-8）。
	// 负例：无语言段的弱对象（示例）不再被 P3 拉成 code（M-4 拆分，
	// "致辞示例"是创意写作形态）。
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	for _, p := range []string{
		"写一个Python快速排序",
		"帮我写一个Python脚本",
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary != TaskCode {
			t.Errorf("expected TaskCode for %q, got %s conf=%.2f reason=%s",
				p, res.Primary, res.Confidence, res.Reason)
		}
	}

	for _, p := range []string{
		"帮我写一个致辞示例",
		"帮我写一个婚礼致辞示例",
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary == TaskCode {
			t.Errorf("expected non-code for creative-form %q, got %s conf=%.2f reason=%s",
				p, res.Primary, res.Confidence, res.Reason)
		}
	}
}
