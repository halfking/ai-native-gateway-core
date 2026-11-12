package evaluation

import (
	"context"
	"errors"
	"testing"
)

// stubLLM 是测试用的 LLM 客户端，按输入返回固定输出。
type stubLLM struct {
	output string
	err    error
	calls  int
}

func (s *stubLLM) Generate(ctx context.Context, prompt string, options GenerateOptions) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.output, nil
}

// TestCompletenessMetricDetectsShortOutput 钉住完整指标必须能判负。
//
// 回归背景（2026-11-11 实测）：旧实现写成 len(actual) < len(expected)*0.5，
// 其中 len() 返回 int，与无类型浮点常量 0.5 相乘会被截断为 0，
// 于是该判据恒为 false ⇒ 本指标恒返回 1.0（永远满分），
// 即"缺覆盖的全绿"。本测试证明它现在能区分长短输出。
func TestCompletenessMetricDetectsShortOutput(t *testing.T) {
	m := &CompletenessMetric{}
	ctx := context.Background()

	expected := "这是一个很长很长的期望输出，包含很多内容用来测试完整性指标的判别能力是否真的生效"

	// 远短于期望 ⇒ 必须扣分（不能是满分）
	short, err := m.Calculate(ctx, expected, "短")
	if err != nil {
		t.Fatalf("calculate short: %v", err)
	}
	if short >= 1.0 {
		t.Fatalf("短输出应扣分，实际得分 %.2f（期望 < 1.0）—— 指标恒真的回归已复发", short)
	}

	// 明显长于期望 ⇒ 满分
	long, err := m.Calculate(ctx, expected, expected+expected)
	if err != nil {
		t.Fatalf("calculate long: %v", err)
	}
	if long != 1.0 {
		t.Fatalf("长输出应满分，实际 %.2f", long)
	}
}

// TestCompletenessMetricEmptyExpected 钉住期望为空时的边界行为。
func TestCompletenessMetricEmptyExpected(t *testing.T) {
	m := &CompletenessMetric{}
	ctx := context.Background()

	withOutput, err := m.Calculate(ctx, "", "有内容")
	if err != nil {
		t.Fatalf("calculate: %v", err)
	}
	if withOutput != 1.0 {
		t.Fatalf("期望为空且有输出时应满分，实际 %.2f", withOutput)
	}

	empty, err := m.Calculate(ctx, "", "")
	if err != nil {
		t.Fatalf("calculate: %v", err)
	}
	if empty != 0.0 {
		t.Fatalf("期望为空且无输出时应 0 分，实际 %.2f", empty)
	}
}

// TestEvaluatorRequiresLLMClient 钉住"没接 LLM 客户端必须报错而不是静默 0 分"。
//
// 这是"错的量具比没量具危险"的同款：静默 0 分会被读成"优化后更差"。
func TestEvaluatorRequiresLLMClient(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), nil)

	_, err := e.Evaluate(context.Background(), "prompt", optimizerEvalOpts())
	if err == nil {
		t.Fatal("未配置 LLM 客户端时必须报错，实际返回 nil error")
	}
	if !errors.Is(err, err) || err.Error() == "" {
		t.Fatalf("错误信息不应为空: %v", err)
	}
}

// TestEvaluatorNoTestCases 钉住空测试集必须报错。
func TestEvaluatorNoTestCases(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), &stubLLM{output: "x"})

	_, err := e.Evaluate(context.Background(), "prompt", emptyEvalOpts())
	if err == nil {
		t.Fatal("空测试集必须报错")
	}
}

// TestEvaluatorProducesScores 钉住正常路径能产出 0..1 的分数。
func TestEvaluatorProducesScores(t *testing.T) {
	llm := &stubLLM{output: "这是期望的输出内容"}
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), llm)

	score, err := e.Evaluate(context.Background(), "prompt", optimizerEvalOpts())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if score < 0 || score > 1 {
		t.Fatalf("得分应在 [0,1] 区间，实际 %.4f", score)
	}
	if llm.calls == 0 {
		t.Fatal("评估器未调用 LLM 客户端")
	}
}

// TestMetricBreakdownReturnsPerMetricScores 钉住逐指标明细可用。
func TestMetricBreakdownReturnsPerMetricScores(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), &stubLLM{output: "期望输出"})

	breakdown, err := e.MetricBreakdown(context.Background(), "prompt", optimizerEvalOpts())
	if err != nil {
		t.Fatalf("metric breakdown: %v", err)
	}
	if len(breakdown) == 0 {
		t.Fatal("逐指标明细不应为空")
	}
	for name, score := range breakdown {
		if score < 0 || score > 1 {
			t.Fatalf("指标 %s 得分越界: %.4f", name, score)
		}
	}
}

// TestAccuracyMetricIdenticalStrings 钉住完全一致的输出得满分。
func TestAccuracyMetricIdenticalStrings(t *testing.T) {
	m := &AccuracyMetric{}
	score, err := m.Calculate(context.Background(), "完全一样的文本", "完全一样的文本")
	if err != nil {
		t.Fatalf("calculate: %v", err)
	}
	if score != 1.0 {
		t.Fatalf("完全一致应得 1.0，实际 %.4f", score)
	}
}

// TestAccuracyMetricDifferentStrings 钉住完全不同的输出得 0 分。
func TestAccuracyMetricDifferentStrings(t *testing.T) {
	m := &AccuracyMetric{}
	score, err := m.Calculate(context.Background(), "abcdef", "uvwxyz")
	if err != nil {
		t.Fatalf("calculate: %v", err)
	}
	if score != 0.0 {
		t.Fatalf("完全不同应得 0.0，实际 %.4f", score)
	}
}
