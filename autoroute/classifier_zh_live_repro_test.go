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

// TestHeuristicClassifier_LiveRepro_FrameworkVocabularyIsShared pins the
// remaining half of audit item L-4: the commit that introduced the P1
// language segment claimed "词表与 P2 共用", but P2 grew a framework family
// (django/flask/spring/gin/echo/flutter/nextjs/nuxt/tailwind/html/css/
// powershell) that P1 and P3b never received. A request of the shape
// "写一个 <框架> <编程对象>" therefore fell through every pattern — P1's
// language slot had no entry, P2 requires 用, and P3a's prefix list has no
// bare "写一个" — and landed on chat 0.1.
//
// The fix is a single shared vocabulary constant rather than another
// hand-copied list, so the three positions cannot drift apart again.
func TestHeuristicClassifier_LiveRepro_FrameworkVocabularyIsShared(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	// Framework names that P2 accepted but P1/P3b did not.
	for _, p := range []string{
		"写一个 Django 中间件",
		"写一个 Flask 服务",
		"实现一个 Spring 接口",
		"写一个 Gin 路由",
		"写一个 Echo 中间件",
		"写一个 Flutter 组件",
		"写一个 NextJS 组件",
		"写一个 Nuxt 组件",
		"写一个 Tailwind 组件",
		"写一个 HTML 表单组件",
		"写一个 CSS 组件",
		"写一个 Powershell 脚本",
		"帮我写一个 Django 方法", // P3b: language + generic artifact
		"写一个Django中间件",    // zero-space variant of the same gap
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

	// Guard the other direction: widening the P1 language slot must not turn
	// creative or office writing into code. These carry no framework name, so
	// they stay outside every shared-vocabulary position.
	for _, p := range []string{
		"帮我写一个致辞示例",
		"帮我写一个婚礼致辞示例",
		"写一个关于春天的散文",
		"写一个产品发布方案的规划",
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary == TaskCode {
			t.Errorf("expected non-code for %q, got %s conf=%.2f reason=%s",
				p, res.Primary, res.Confidence, res.Reason)
		}
	}
}

// TestHeuristicClassifier_LiveRepro_FrameworkWordsDoNotStealNonCodeTasks
// pins the false-positive boundary measured while widening the shared
// vocabulary, case by case rather than as one aggregate assertion.
//
// A framework name in the sentence is not enough to make a request code: these
// four were the live probes where the stronger channel must win, and each one
// regresses loudly if the language slot grows again without a re-measurement.
func TestHeuristicClassifier_LiveRepro_FrameworkWordsDoNotStealNonCodeTasks(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	for _, tc := range []struct {
		prompt string
		want   TaskType
		why    string
	}{
		{"写一个 Spring Cloud 微服务架构方案", TaskPlanning,
			"framework + architecture plan is planning, not a coding ask"},
		{"用 React 做一个年度规划", TaskPlanning,
			"P2 already accepted 用 React 做一个; 规划 must still win"},
		{"写一个 HTML 邮件文案", TaskCreative,
			"HTML plus marketing copy is creative writing"},
		{"写一个 Gin 路由的压测报告", TaskCode,
			"Gin + 路由 is a real coding object, unlike the three above"},
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: tc.prompt})
		if err != nil {
			t.Fatalf("err on %q: %v", tc.prompt, err)
		}
		if res.Primary != tc.want {
			t.Errorf("%q => %s conf=%.2f reason=%s, want %s (%s)",
				tc.prompt, res.Primary, res.Confidence, res.Reason, tc.want, tc.why)
		}
	}
}

// TestHeuristicClassifier_LiveRepro_PlanningArtifactWithoutPlanVerb covers
// the gap measured while closing L-4: prompts that name a planning *artifact*
// but not a canonical planning verb fell all the way to chat 0.10.
//
// "帮我写一个 NextJS 项目的技术选型文档" and "写一个 Django 项目的迁移计划"
// both state the deliverable (技术选型 / 迁移计划) — that is what makes them
// planning. The verb list deliberately excludes 写/做 as generic verbs
// (2026-09-14: avoiding creative false positives), so these two needed a
// separate artifact rule: a domain qualifier plus a planning deliverable,
// with no verb requirement at all.
func TestHeuristicClassifier_LiveRepro_PlanningArtifactWithoutPlanVerb(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	for _, tc := range []struct {
		prompt string
		why    string
	}{
		{"帮我写一个 NextJS 项目的技术选型文档", "技术选型 is a planning deliverable"},
		{"写一个 Django 项目的迁移计划", "迁移计划 is a planning deliverable"},
		{"帮我出一个 Redis 缓存的容量计划", "容量计划 is a planning deliverable"},
		{"写一个 MySQL 到 PG 的数据迁移方案", "数据迁移方案 is a planning deliverable"},
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: tc.prompt})
		if err != nil {
			t.Fatalf("err on %q: %v", tc.prompt, err)
		}
		if res.Primary != TaskPlanning {
			t.Errorf("%q => %s conf=%.2f reason=%s, want planning (%s)",
				tc.prompt, res.Primary, res.Confidence, res.Reason, tc.why)
		}
	}

	// A weekly report is document writing, not planning, and certainly not
	// code. Only the "not code" half is pinned: whether it should eventually
	// be chat or a document task type is a product decision, and pinning a
	// label here would freeze an open question.
	for _, p := range []string{
		"写一个 Flutter 团队周报",
		"写一个项目周报模板",
		"帮我写一个会议纪要",
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary == TaskCode {
			t.Errorf("%q => %s conf=%.2f reason=%s, must not be code",
				p, res.Primary, res.Confidence, res.Reason)
		}
	}

	// The artifact rule's measured false positives. Both are statements about a
	// plan that already exists; the retroactive guard is what keeps them out.
	for _, p := range []string{
		"把这个项目的计划删掉",
		"帮我看一下技术方案",
		"回顾一下这个项目的路线图",
	} {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: p})
		if err != nil {
			t.Fatalf("err on %q: %v", p, err)
		}
		if res.Primary == TaskPlanning {
			t.Errorf("%q => planning conf=%.2f reason=%s, want not-planning "+
				"(a statement about an existing plan, not a request to produce one)",
				p, res.Confidence, res.Reason)
		}
	}

	// The strong coding channel still wins over the new artifact rule.
	res, err := c.Classify(context.Background(),
		ClassificationSignals{LastUserPrompt: "先制定计划然后实现一个缓存"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Primary != TaskCode {
		t.Errorf("plan-mode coding => %s conf=%.2f, want TaskCode", res.Primary, res.Confidence)
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
