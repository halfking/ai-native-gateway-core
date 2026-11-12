package promptoptimizer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAnalyzeIntentInfersTaskType 钉住意图推断：未显式指定时从正文推断。
func TestAnalyzeIntentInfersTaskType(t *testing.T) {
	o := &optimizer{config: defaultConfig()}
	ctx := context.Background()

	cases := []struct {
		prompt string
		want   string
	}{
		{"帮我翻译这段文字", "translation"},
		{"写一个函数计算斐波那契数列", "code_generation"},
		{"分析一下这组数据", "analysis"},
		{"总结这篇文章的要点", "summarization"},
		{"什么是提示词工程？", "qa"},
		{"帮我把这段话改写得更正式", "general"},
	}

	for _, tc := range cases {
		got, err := o.analyzeIntent(ctx, tc.prompt, OptimizeOptions{})
		if err != nil {
			t.Fatalf("analyzeIntent(%q): %v", tc.prompt, err)
		}
		if got.TaskType != tc.want {
			t.Errorf("analyzeIntent(%q).TaskType = %q, want %q", tc.prompt, got.TaskType, tc.want)
		}
	}
}

// TestAnalyzeIntentPrefersExplicitOptions 钉住显式传入的 TaskType/Domain 优先于推断。
func TestAnalyzeIntentPrefersExplicitOptions(t *testing.T) {
	o := &optimizer{config: defaultConfig()}

	intent, err := o.analyzeIntent(context.Background(), "帮我翻译这段文字", OptimizeOptions{
		TaskType: "code_generation",
		Domain:   "medical",
	})
	if err != nil {
		t.Fatalf("analyzeIntent: %v", err)
	}
	if intent.TaskType != "code_generation" {
		t.Errorf("显式 TaskType 应优先，实际 %q", intent.TaskType)
	}
	if intent.Domain != "medical" {
		t.Errorf("显式 Domain 应优先，实际 %q", intent.Domain)
	}
}

// TestAnalyzeIntentExtractsConstraints 钉住显式约束句被抽取出来。
func TestAnalyzeIntentExtractsConstraints(t *testing.T) {
	o := &optimizer{config: defaultConfig()}

	intent, err := o.analyzeIntent(context.Background(),
		"写一份报告。必须包含财务数据。不得使用未经验证的数据。", OptimizeOptions{})
	if err != nil {
		t.Fatalf("analyzeIntent: %v", err)
	}
	if len(intent.Constraints) != 2 {
		t.Fatalf("应抽出 2 条约束，实际 %d 条: %v", len(intent.Constraints), intent.Constraints)
	}
	joined := strings.Join(intent.Constraints, "|")
	if !strings.Contains(joined, "必须") || !strings.Contains(joined, "不得") {
		t.Errorf("约束内容不对: %v", intent.Constraints)
	}
}

// TestAnalyzeIntentRejectsEmptyPrompt 钉住空提示词必须报错。
func TestAnalyzeIntentRejectsEmptyPrompt(t *testing.T) {
	o := &optimizer{config: defaultConfig()}

	if _, err := o.analyzeIntent(context.Background(), "   ", OptimizeOptions{}); err == nil {
		t.Fatal("空提示词必须报错")
	}
}

// TestOptimizeWithEvaluationRequiresEvaluator 钉住缺评估器时明确报错而非 panic。
//
// 回归背景：旧实现直接 o.evaluator.Evaluate(...)，
// evaluator 为 nil（接口零值）时是 nil 接口调用 ⇒ 运行期 panic。
func TestOptimizeWithEvaluationRequiresEvaluator(t *testing.T) {
	o := &optimizer{config: defaultConfig()} // 未设置 evaluator

	_, err := o.OptimizeWithEvaluation(context.Background(), "prompt", EvaluationOptions{
		TestCases: []TestCase{{ID: "t1", Input: "i", Expected: "e"}},
	})
	if err == nil {
		t.Fatal("缺评估器时必须报错")
	}
	if !strings.Contains(err.Error(), "evaluator") {
		t.Errorf("错误信息应指向 evaluator，实际: %v", err)
	}
}

// TestOptimizeWithEvaluationRequiresTestCases 钉住空测试集必须报错。
func TestOptimizeWithEvaluationRequiresTestCases(t *testing.T) {
	o := &optimizer{config: defaultConfig()}

	_, err := o.OptimizeWithEvaluation(context.Background(), "prompt", EvaluationOptions{})
	if err == nil {
		t.Fatal("空测试集必须报错")
	}
}

// TestOptimizeWithEvaluationZeroBaselineNoNaN 钉住基线为 0 时不产生 NaN/Inf。
//
// 回归背景：旧实现 improvement = (opt-base)/base*100，
// base==0 时得 NaN（0/0）或 +Inf（x/0）⇒ JSON 序列化会失败或写出非法 JSON。
func TestOptimizeWithEvaluationZeroBaselineNoNaN(t *testing.T) {
	o := &optimizer{
		config:    defaultConfig(),
		evaluator: &fixedEvaluator{score: 0},
	}

	result, err := o.OptimizeWithEvaluation(context.Background(), "prompt", EvaluationOptions{
		TestCases: []TestCase{{ID: "t1", Input: "i", Expected: "e"}},
	})
	if err != nil {
		t.Fatalf("OptimizeWithEvaluation: %v", err)
	}
	if isNaNOrInf(result.Improvement) {
		t.Fatalf("基线为 0 时 Improvement 不得为 NaN/Inf，实际 %v", result.Improvement)
	}
	if result.AbsoluteDelta != 0 {
		t.Fatalf("基线为 0 且优化后也为 0 时 AbsoluteDelta 应为 0，实际 %v", result.AbsoluteDelta)
	}
}

// TestOptimizeWithEvaluationNormalPath 钉住正常路径能算出提升幅度。
func TestOptimizeWithEvaluationNormalPath(t *testing.T) {
	o := &optimizer{
		config:    defaultConfig(),
		evaluator: &fixedEvaluator{score: 0.5},
	}

	result, err := o.OptimizeWithEvaluation(context.Background(), "prompt", EvaluationOptions{
		TestCases: []TestCase{{ID: "t1", Input: "i", Expected: "e"}},
	})
	if err != nil {
		t.Fatalf("OptimizeWithEvaluation: %v", err)
	}
	if result.BaselineScore != 0.5 || result.OptimizedScore != 0.5 {
		t.Fatalf("得分不对: base=%.2f opt=%.2f", result.BaselineScore, result.OptimizedScore)
	}
	if result.Improvement != 0 {
		t.Fatalf("同分时提升应为 0，实际 %.4f", result.Improvement)
	}
}

// TestOptimizeEmptyPromptReturnsError 钉住空提示词走完整链路时报错。
func TestOptimizeEmptyPromptReturnsError(t *testing.T) {
	o := NewOptimizer()

	_, err := o.Optimize(context.Background(), "", OptimizeOptions{})
	if err == nil {
		t.Fatal("空提示词必须报错")
	}
}

// TestOptimizeAppliesStrategiesInOrder 钉住策略按注册顺序依次应用。
func TestOptimizeAppliesStrategiesInOrder(t *testing.T) {
	o := NewOptimizer(
		WithStrategies(
			&recordingStrategy{name: "first", marker: "AAA"},
			&recordingStrategy{name: "second", marker: "BBB"},
		),
	)

	result, err := o.Optimize(context.Background(), "原始提示词", OptimizeOptions{})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(result.AppliedStrategies) != 2 {
		t.Fatalf("应应用 2 个策略，实际 %d", len(result.AppliedStrategies))
	}
	if result.AppliedStrategies[0] != "first" || result.AppliedStrategies[1] != "second" {
		t.Errorf("策略顺序不对: %v", result.AppliedStrategies)
	}
	if !strings.Contains(result.Optimized, "AAA") || !strings.Contains(result.Optimized, "BBB") {
		t.Errorf("两个策略的产物都应出现: %q", result.Optimized)
	}
}

// TestOptimizeStrategyErrorPropagates 钉住策略报错会向上传播。
func TestOptimizeStrategyErrorPropagates(t *testing.T) {
	o := NewOptimizer(WithStrategies(&failingStrategy{}))

	_, err := o.Optimize(context.Background(), "提示词", OptimizeOptions{})
	if err == nil {
		t.Fatal("策略错误必须传播")
	}
	if !errors.Is(err, errFailingStrategy) {
		t.Errorf("错误应可用 errors.Is 检出: %v", err)
	}
}

// TestOptimizeSelectsNamedStrategies 钉住按名字筛选策略。
func TestOptimizeSelectsNamedStrategies(t *testing.T) {
	o := NewOptimizer(
		WithStrategies(
			&recordingStrategy{name: "alpha", marker: "AAA"},
			&recordingStrategy{name: "beta", marker: "BBB"},
		),
	)

	result, err := o.Optimize(context.Background(), "提示词", OptimizeOptions{
		Strategies: []string{"beta"},
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(result.AppliedStrategies) != 1 || result.AppliedStrategies[0] != "beta" {
		t.Errorf("应只应用 beta，实际 %v", result.AppliedStrategies)
	}
	if strings.Contains(result.Optimized, "AAA") {
		t.Errorf("未选中的策略不应产生内容: %q", result.Optimized)
	}
}

// TestOptimizeBatchPreservesOrder 钉住批量优化保持输入顺序。
func TestOptimizeBatchPreservesOrder(t *testing.T) {
	o := NewOptimizer(WithStrategies(&recordingStrategy{name: "s", marker: "X"}))

	results, err := o.OptimizeBatch(context.Background(),
		[]string{"一", "二", "三"},
		OptimizeOptions{})
	if err != nil {
		t.Fatalf("OptimizeBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("应返回 3 条结果，实际 %d", len(results))
	}
	for i, want := range []string{"一", "二", "三"} {
		if results[i].Original != want {
			t.Errorf("第 %d 条顺序错位: got %q want %q", i, results[i].Original, want)
		}
	}
}

// TestOptimizeBatchEmptyInput 钉住空输入返回空结果而非报错。
func TestOptimizeBatchEmptyInput(t *testing.T) {
	o := NewOptimizer()

	results, err := o.OptimizeBatch(context.Background(), nil, OptimizeOptions{})
	if err != nil {
		t.Fatalf("OptimizeBatch: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("空输入应返回空结果，实际 %d 条", len(results))
	}
}

// ── 测试替身 ────────────────────────────────────────────────────────

// fixedEvaluator 返回固定分数的评估器。
type fixedEvaluator struct {
	score float64
}

func (f *fixedEvaluator) Evaluate(ctx context.Context, prompt string, opts EvaluationOptions) (float64, error) {
	return f.score, nil
}

func (f *fixedEvaluator) EvaluatePrompt(ctx context.Context, prompt string, tc TestCase) (float64, error) {
	return f.score, nil
}

func (f *fixedEvaluator) EvaluatePromptQuality(ctx context.Context, prompt string) (float64, error) {
	return f.score, nil
}

// recordingStrategy 在提示词里追加自己的标记，用于验证顺序。
type recordingStrategy struct {
	name   string
	marker string
}

func (r *recordingStrategy) Name() string { return r.name }

func (r *recordingStrategy) Apply(ctx context.Context, prompt string, sc StrategyContext) (string, error) {
	return prompt + " " + r.marker, nil
}

func (r *recordingStrategy) Score(ctx context.Context, prompt string, tc TestCase) (float64, error) {
	return 1, nil
}

// errFailingStrategy 是失败策略的哨兵错误。
var errFailingStrategy = errors.New("failing strategy")

// failingStrategy 总是失败的策略。
type failingStrategy struct{}

func (f *failingStrategy) Name() string { return "failing" }

func (f *failingStrategy) Apply(ctx context.Context, prompt string, sc StrategyContext) (string, error) {
	return "", errFailingStrategy
}

func (f *failingStrategy) Score(ctx context.Context, prompt string, tc TestCase) (float64, error) {
	return 0, nil
}

// isNaNOrInf 判断浮点数是否为 NaN 或 ±Inf。
func isNaNOrInf(f float64) bool {
	if f != f {
		return true // NaN
	}
	return f > maxFloat || f < -maxFloat
}

const maxFloat = 1.7976931348623157e+308
