package strategies

import (
	"context"
	"strings"
	"testing"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// TestStructuralAddsAllSections 钉住结构化策略产出全部关键章节。
func TestStructuralAddsAllSections(t *testing.T) {
	s := NewStructural(DefaultStructuralConfig())
	ctx := context.Background()

	out, err := s.Apply(ctx, "帮我翻译这段文字", optimizer.StrategyContext{
		TaskType: "translation",
		Domain:   "technical",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, want := range []string{
		"# 角色定义",
		"# 任务目标",
		"# 约束条件",
		"# 输出格式",
		"# 质量检查",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少章节 %q:\n%s", want, out)
		}
	}
}

// TestStructuralRoleReflectsTaskType 钉住角色定义随任务类型变化。
//
// 断言必须落在"角色定义"这一段的具体措辞上，而不是落在"翻译"/"工程师"
// 这类可能出现在别处（任务目标原文、约束行）的词上 ——
// 否则删掉角色分支后，断言仍会被原文里的同名词满足 ⇒ 守卫打不中。
// （2026-11-11 变异验证 ARM11 实测踩到过这个坑。）
func TestStructuralRoleReflectsTaskType(t *testing.T) {
	s := NewStructural(DefaultStructuralConfig())
	ctx := context.Background()

	// 传入的原文刻意不含"翻译"/"工程师"，避免原文满足断言。
	trans, err := s.Apply(ctx, "帮我处理这段文字", optimizer.StrategyContext{TaskType: "translation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	code, err := s.Apply(ctx, "帮我处理这段文字", optimizer.StrategyContext{TaskType: "code_generation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if trans == code {
		t.Error("不同任务类型应产出不同角色定义")
	}
	if !strings.Contains(trans, "专业翻译专家") {
		t.Errorf("翻译任务角色应含\"专业翻译专家\":\n%s", trans)
	}
	if !strings.Contains(code, "资深软件工程师") {
		t.Errorf("代码任务角色应含\"资深软件工程师\":\n%s", code)
	}
}

// TestStructuralAddsTaskSpecificConstraints 钉住任务类型的专属约束被注入。
//
// 断言落在约束的完整措辞上，不能落在"最佳实践"这种
// 同时出现在角色定义里的词上（变异验证 ARM10 实测踩到过）。
func TestStructuralAddsTaskSpecificConstraints(t *testing.T) {
	s := NewStructural(DefaultStructuralConfig())
	ctx := context.Background()

	out, err := s.Apply(ctx, "帮我处理这段文字", optimizer.StrategyContext{TaskType: "code_generation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out, "代码必须遵循最佳实践和编码规范") {
		t.Errorf("代码任务应含编码规范约束:\n%s", out)
	}

	// 翻译任务的专属约束也要在。
	transOut, err := s.Apply(ctx, "帮我处理这段文字", optimizer.StrategyContext{TaskType: "translation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(transOut, "确保专业术语翻译准确") {
		t.Errorf("翻译任务应含术语约束:\n%s", transOut)
	}
}

// TestStructuralInjectsProvidedConstraints 钉住外部约束被并入。
func TestStructuralInjectsProvidedConstraints(t *testing.T) {
	s := NewStructural(DefaultStructuralConfig())
	ctx := context.Background()

	out, err := s.Apply(ctx, "做任务", optimizer.StrategyContext{
		Constraints: []string{"必须使用中文回答"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out, "必须使用中文回答") {
		t.Errorf("外部约束应被注入:\n%s", out)
	}
}

// TestStructuralScoreReflectsStructure 钉住结构评分能区分有无结构。
func TestStructuralScoreReflectsStructure(t *testing.T) {
	s := NewStructural(DefaultStructuralConfig())
	ctx := context.Background()

	structured, err := s.Apply(ctx, "做任务", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	high, err := s.Score(ctx, structured, optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	low, err := s.Score(ctx, "随便写点什么", optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if high <= low {
		t.Errorf("结构化提示词得分应更高: high=%.2f low=%.2f", high, low)
	}
}

// TestFewShotAddsExamples 钉住 Few-shot 策略注入示例。
func TestFewShotAddsExamples(t *testing.T) {
	s := NewFewShot(DefaultFewShotConfig(), nil)
	ctx := context.Background()

	out, err := s.Apply(ctx, "翻译文本", optimizer.StrategyContext{TaskType: "translation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out, "参考示例") {
		t.Errorf("应注入示例章节:\n%s", out)
	}
	if !strings.Contains(out, "示例 1") {
		t.Errorf("应至少有一个示例:\n%s", out)
	}
	if !strings.Contains(out, "说明:") {
		t.Errorf("应包含示例说明:\n%s", out)
	}
}

// TestFewShotNoExamplesLeavesPromptUnchanged 钉住无候选示例时不改提示词。
func TestFewShotNoExamplesLeavesPromptUnchanged(t *testing.T) {
	s := NewFewShot(DefaultFewShotConfig(), nil)
	ctx := context.Background()

	original := "一个没有匹配示例的任务类型 xyzzy"
	out, err := s.Apply(ctx, original, optimizer.StrategyContext{TaskType: "unknown_type_xyzzy"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != original {
		t.Errorf("无示例时提示词不应被改写:\ngot  %q\nwant %q", out, original)
	}
}

// TestFewShotFiltersLowQuality 钉住低质量示例被过滤掉。
func TestFewShotFiltersLowQuality(t *testing.T) {
	cfg := DefaultFewShotConfig()
	cfg.QualityThreshold = 0.95

	s := NewFewShot(cfg, nil)
	ctx := context.Background()

	out, err := s.Apply(ctx, "翻译文本", optimizer.StrategyContext{TaskType: "translation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// 预置示例质量为 0.92/0.95，阈值 0.95 ⇒ 只留 1 条。
	if strings.Count(out, "## 示例") > 1 {
		t.Errorf("低质量示例应被过滤:\n%s", out)
	}
}

// TestFewShotScoreHigherWithExamples 钉住含示例的提示词得分更高。
func TestFewShotScoreHigherWithExamples(t *testing.T) {
	s := NewFewShot(DefaultFewShotConfig(), nil)
	ctx := context.Background()

	withExamples, err := s.Apply(ctx, "翻译文本", optimizer.StrategyContext{TaskType: "translation"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	high, err := s.Score(ctx, withExamples, optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	low, err := s.Score(ctx, "没有示例的提示词", optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if high <= low {
		t.Errorf("含示例应得分更高: high=%.2f low=%.2f", high, low)
	}
}

// TestIterativeWithoutLLMIsNoop 钉住无 LLM 客户端 / 无测试用例时迭代策略是 no-op。
func TestIterativeWithoutLLMIsNoop(t *testing.T) {
	s := NewIterative(DefaultIterativeConfig(), nil, nil)
	ctx := context.Background()

	original := "原始提示词"
	out, err := s.Apply(ctx, original, optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != original {
		t.Errorf("无 LLM 客户端时应原样返回:\ngot  %q\nwant %q", out, original)
	}
}

// TestIterativeImprovesPrompt 钉住迭代优化能选出更好的变体。
func TestIterativeImprovesPrompt(t *testing.T) {
	cfg := DefaultIterativeConfig()
	cfg.VariantsPerIteration = 2
	cfg.MaxIterations = 3

	// LLM 每次返回不同变体；评估器给"含 BEST"的变体最高分。
	llm := &sequenceLLM{
		outputs: []string{
			"含 BEST 的变体",
			"普通变体",
		},
	}
	eval := &keywordEvaluator{keyword: "BEST", score: 0.9, otherScore: 0.2}

	s := NewIterative(cfg, llm, eval)
	ctx := context.Background()

	out, err := s.Apply(ctx, "原始提示词", optimizer.StrategyContext{
		TestCases: []optimizer.TestCase{
			{ID: "t1", Input: "i", Expected: "e"},
			{ID: "t2", Input: "j", Expected: "f"},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out, "BEST") {
		t.Errorf("迭代优化应保留高分变体，实际输出 %q", out)
	}
}

// TestIterativeWithoutTestCasesIsNoop 钉住没有测试用例时迭代优化不改动提示词。
//
// 回归背景：getTestCases 原先硬编码返回空切片 ⇒ 迭代策略是死代码。
// 修正后仍必须保证"没度量就不瞎改"这条底线。
func TestIterativeWithoutTestCasesIsNoop(t *testing.T) {
	cfg := DefaultIterativeConfig()
	s := NewIterative(cfg, &sequenceLLM{outputs: []string{"变体"}}, &keywordEvaluator{keyword: "BEST"})
	ctx := context.Background()

	original := "原始提示词"
	out, err := s.Apply(ctx, original, optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != original {
		t.Errorf("无测试用例时不应改动提示词:\ngot  %q\nwant %q", out, original)
	}
}

// ── 测试替身 ────────────────────────────────────────────────────────

// sequenceLLM 按顺序返回预设输出。
type sequenceLLM struct {
	outputs []string
	idx     int
}

func (s *sequenceLLM) Generate(ctx context.Context, prompt string, options GenerateOptions) (string, error) {
	if s.idx >= len(s.outputs) {
		s.idx = len(s.outputs) - 1
	}
	out := s.outputs[s.idx]
	s.idx++
	return out, nil
}

// keywordEvaluator 含关键词给高分，否则给低分。
type keywordEvaluator struct {
	keyword    string
	score      float64
	otherScore float64
}

func (k *keywordEvaluator) Evaluate(ctx context.Context, prompt string, opts optimizer.EvaluationOptions) (float64, error) {
	return k.EvaluatePrompt(ctx, prompt, optimizer.TestCase{})
}

func (k *keywordEvaluator) EvaluatePrompt(ctx context.Context, prompt string, tc optimizer.TestCase) (float64, error) {
	if strings.Contains(prompt, k.keyword) {
		return k.score, nil
	}
	return k.otherScore, nil
}

func (k *keywordEvaluator) EvaluatePromptQuality(ctx context.Context, prompt string) (float64, error) {
	return k.score, nil
}
