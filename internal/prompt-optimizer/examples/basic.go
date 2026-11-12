// Package examples 演示提示词优化模块的三种典型用法。
//
// 这里刻意做成普通库包（而不是 package main）：① 可被 examples_test.go
// 直接调用做冒烟验证；② 不与 cmd/ 下的可执行文件抢 main 入口。
package examples

import (
	"context"
	"fmt"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/evaluation"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/harness"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies"
)

// NewBasicOptimizer 构造一个「结构化 + Few-shot + Harness 注入」的优化器。
func NewBasicOptimizer() optimizer.Optimizer {
	return optimizer.NewOptimizer(
		optimizer.WithStrategies(
			strategies.NewStructural(strategies.DefaultStructuralConfig()),
			strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil),
		),
		optimizer.WithHarnessInjector(
			harness.NewInjector(
				harness.DefaultInjectorConfig(),
				harness.NewMockToolRegistry(),
				harness.NewMockSafetyRules(),
			),
		),
	)
}

// RunBasic 演示：优化单条提示词。
func RunBasic(ctx context.Context) (*optimizer.OptimizedPrompt, error) {
	original := "帮我翻译这段文字"

	result, err := NewBasicOptimizer().Optimize(ctx, original, optimizer.OptimizeOptions{
		TaskType:      "translation",
		Domain:        "technical",
		TargetModel:   "gpt-4",
		UseMemora:     false,
		InjectHarness: true,
		MaxIterations: 3,
	})
	if err != nil {
		return nil, fmt.Errorf("optimize: %w", err)
	}
	return result, nil
}

// RunEvaluation 演示：优化并对基线做评估对比。
//
// 注意：评估器在没有接入真实 LLM 客户端时会返回明确的错误，
// 而不是静默返回 0 分 —— 避免"缺覆盖的全绿"。
func RunEvaluation(ctx context.Context) (*optimizer.OptimizationResult, error) {
	evaluator := evaluation.NewEvaluator(
		evaluation.DefaultEvaluatorConfig(),
		evaluation.DefaultMetrics(),
		nil, // 生产环境应接入真实 LLM 客户端
	)

	opt := optimizer.NewOptimizer(
		optimizer.WithStrategies(
			strategies.NewStructural(strategies.DefaultStructuralConfig()),
		),
		optimizer.WithEvaluator(evaluator),
	)

	testCases := []optimizer.TestCase{
		{ID: "test1", Input: "n=5", Expected: "fibonacci(5) = 5"},
		{ID: "test2", Input: "n=10", Expected: "fibonacci(10) = 55"},
	}

	result, err := opt.OptimizeWithEvaluation(ctx, "写一个函数计算斐波那契数列", optimizer.EvaluationOptions{
		TestCases: testCases,
		Metrics:   []string{"accuracy", "completeness"},
		ABTest:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("optimize with evaluation: %w", err)
	}
	return result, nil
}

// RunBatch 演示：批量优化多条提示词。
func RunBatch(ctx context.Context) ([]*optimizer.OptimizedPrompt, error) {
	prompts := []string{
		"总结这篇文章",
		"分析这组数据",
		"生成一个报告",
	}

	results, err := optimizer.NewOptimizer(
		optimizer.WithStrategies(
			strategies.NewStructural(strategies.DefaultStructuralConfig()),
		),
	).OptimizeBatch(ctx, prompts, optimizer.OptimizeOptions{
		TaskType:      "general",
		InjectHarness: true,
	})
	if err != nil {
		return nil, fmt.Errorf("optimize batch: %w", err)
	}
	return results, nil
}

// FormatBasicReport 把基础示例结果格式化成可读文本（供 CLI/文档复用）。
func FormatBasicReport(result *optimizer.OptimizedPrompt) string {
	var sb strings.Builder
	sb.WriteString("=== 优化前 ===\n")
	sb.WriteString(result.Original)
	sb.WriteString("\n\n=== 优化后 ===\n")
	sb.WriteString(result.Optimized)
	sb.WriteString("\n\n应用的策略: ")
	sb.WriteString(strings.Join(result.AppliedStrategies, ", "))
	sb.WriteString("\n注入的 Harness 要素: ")
	sb.WriteString(strings.Join(result.HarnessElements, ", "))
	sb.WriteString(fmt.Sprintf("\n耗时: %v\n置信度: %.2f\n", result.Metadata.Duration, result.Metadata.ConfidenceScore))
	return sb.String()
}
