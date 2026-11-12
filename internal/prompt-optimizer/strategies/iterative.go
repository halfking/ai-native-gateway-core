package strategies

import (
	"context"
	"fmt"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
)

// IterativeStrategy 迭代优化策略
// 基于OPRO方法论，使用LLM自身进行提示词优化
type IterativeStrategy struct {
	config    IterativeConfig
	llmClient LLMClient
	evaluator optimizer.Evaluator
}

// IterativeConfig 迭代优化配置
type IterativeConfig struct {
	// 最大迭代次数
	MaxIterations int

	// 收敛阈值
	ConvergenceThreshold float64

	// 每轮生成的变体数量
	VariantsPerIteration int

	// 是否启用早停
	EarlyStoppingEnabled bool

	// 改进阈值（用于早停）
	ImprovementThreshold float64

	// 使用的优化模型
	OptimizerModel string
}

// LLMClient LLM客户端接口（参数类型用基座包 llm.Options）。
type LLMClient interface {
	Generate(ctx context.Context, prompt string, options llm.Options) (string, error)
}

// GenerateOptions 生成选项（llm.Options 的别名，保持旧代码可编译）。
type GenerateOptions = llm.Options

// NewIterative 创建迭代优化策略
func NewIterative(cfg IterativeConfig, client LLMClient, evaluator optimizer.Evaluator) optimizer.Strategy {
	return &IterativeStrategy{
		config:    cfg,
		llmClient: client,
		evaluator: evaluator,
	}
}

// Name 策略名称
func (s *IterativeStrategy) Name() string {
	return "iterative"
}

// Apply 应用迭代优化策略
func (s *IterativeStrategy) Apply(ctx context.Context, prompt string, strategyCtx optimizer.StrategyContext) (string, error) {
	if s.llmClient == nil || s.evaluator == nil {
		// 如果没有LLM客户端或评估器，返回原提示词
		return prompt, nil
	}

	// 初始化
	currentPrompt := prompt
	bestPrompt := prompt
	bestScore := 0.0
	history := []IterationRecord{}

	// 获取测试用例
	testCases := s.getTestCases(strategyCtx)
	if len(testCases) == 0 {
		// 没有测试用例，无法评估，返回原提示词
		return prompt, nil
	}

	// 迭代优化
	for iteration := 0; iteration < s.config.MaxIterations; iteration++ {
		// 1. 生成提示词变体
		variants, err := s.generateVariants(ctx, currentPrompt, history, strategyCtx)
		if err != nil {
			return bestPrompt, fmt.Errorf("generate variants: %w", err)
		}

		// 2. 评估每个变体
		scores := make(map[string]float64)
		for _, variant := range variants {
			score, err := s.evaluatePrompt(ctx, variant, testCases)
			if err != nil {
				continue
			}
			scores[variant] = score
		}

		// 3. 选择最佳变体
		iterationBest := currentPrompt
		iterationBestScore := bestScore

		for variant, score := range scores {
			if score > iterationBestScore {
				iterationBest = variant
				iterationBestScore = score
			}
		}

		// 4. 记录历史
		history = append(history, IterationRecord{
			Iteration: iteration + 1,
			Prompt:    iterationBest,
			Score:     iterationBestScore,
			Variants:  len(variants),
		})

		// 5. 检查是否改进
		improvement := iterationBestScore - bestScore

		// 6. 更新最佳结果
		if iterationBestScore > bestScore {
			bestPrompt = iterationBest
			bestScore = iterationBestScore
			currentPrompt = iterationBest
		}

		// 7. 检查收敛条件
		if s.checkConvergence(improvement, history) {
			break
		}

		// 8. 检查早停条件
		if s.config.EarlyStoppingEnabled && s.shouldEarlyStop(improvement, history) {
			break
		}
	}

	return bestPrompt, nil
}

// Score 评估提示词质量
func (s *IterativeStrategy) Score(ctx context.Context, prompt string, testCase optimizer.TestCase) (float64, error) {
	if s.evaluator == nil {
		return 0.5, nil
	}

	return s.evaluator.EvaluatePrompt(ctx, prompt, testCase)
}

// generateVariants 生成提示词变体
func (s *IterativeStrategy) generateVariants(ctx context.Context, currentPrompt string, history []IterationRecord, strategyCtx optimizer.StrategyContext) ([]string, error) {
	// 构建优化提示
	optimizationPrompt := s.buildOptimizationPrompt(currentPrompt, history, strategyCtx)

	// 使用LLM生成变体
	variants := []string{}

	for i := 0; i < s.config.VariantsPerIteration; i++ {
		variant, err := s.llmClient.Generate(ctx, optimizationPrompt, GenerateOptions{
			Model:       s.config.OptimizerModel,
			Temperature: 0.7 + float64(i)*0.1, // 增加多样性
			MaxTokens:   2000,
		})

		if err != nil {
			continue
		}

		// 清理和验证变体
		variant = s.cleanVariant(variant)
		if s.isValidVariant(variant) {
			variants = append(variants, variant)
		}
	}

	return variants, nil
}

// buildOptimizationPrompt 构建优化提示
func (s *IterativeStrategy) buildOptimizationPrompt(currentPrompt string, history []IterationRecord, strategyCtx optimizer.StrategyContext) string {
	prompt := `你是一个提示词优化专家。你的任务是优化以下提示词，使其更清晰、更有效。

# 当前提示词

` + "```" + `
` + currentPrompt + `
` + "```" + `

# 任务类型
` + strategyCtx.TaskType + `

# 领域
` + strategyCtx.Domain + `

# 优化目标

1. 提高清晰度和可理解性
2. 增强结构化程度
3. 添加必要的约束和指导
4. 优化示例和说明
5. 确保输出格式明确

# 优化建议

请生成一个改进版本的提示词。要求：
- 保持原有任务意图不变
- 使用更清晰的语言和结构
- 添加有助于理解的细节
- 移除冗余和模糊的内容
- 确保可操作性

`

	// 添加历史信息
	if len(history) > 0 {
		prompt += "\n# 优化历史\n\n"
		for _, record := range history {
			prompt += fmt.Sprintf("迭代 %d: 得分 %.2f\n", record.Iteration, record.Score)
		}
		prompt += "\n请避免重复之前的尝试，探索新的优化方向。\n"
	}

	prompt += "\n# 输出格式\n\n请直接输出优化后的提示词，不要添加额外的解释或标记。\n"

	return prompt
}

// cleanVariant 清理变体
func (s *IterativeStrategy) cleanVariant(variant string) string {
	// 移除markdown代码块标记
	variant = removeCodeBlockMarkers(variant)

	// 移除多余的空行
	variant = normalizeWhitespace(variant)

	return variant
}

// isValidVariant 验证变体是否有效
func (s *IterativeStrategy) isValidVariant(variant string) bool {
	// 基本验证
	if len(variant) < 10 {
		return false
	}

	if len(variant) > 10000 {
		return false
	}

	return true
}

// evaluatePrompt 评估提示词
func (s *IterativeStrategy) evaluatePrompt(ctx context.Context, prompt string, testCases []optimizer.TestCase) (float64, error) {
	if len(testCases) == 0 {
		return 0.5, nil
	}

	totalScore := 0.0
	validCount := 0

	for _, testCase := range testCases {
		score, err := s.evaluator.EvaluatePrompt(ctx, prompt, testCase)
		if err != nil {
			continue
		}

		totalScore += score
		validCount++
	}

	if validCount == 0 {
		return 0.0, fmt.Errorf("no valid evaluations")
	}

	return totalScore / float64(validCount), nil
}

// getTestCases 获取测试用例。
//
// 回归背景（2026-11-11 实测）：本方法原先硬编码返回空切片，
// 于是 Apply 里的 `if len(testCases) == 0 { return prompt, nil }`
// 永远命中 —— 整个迭代优化策略是死代码，从未真正优化过任何提示词。
// 现在从 StrategyContext.TestCases 取用例，由优化器从 OptimizeOptions 透传。
func (s *IterativeStrategy) getTestCases(strategyCtx optimizer.StrategyContext) []optimizer.TestCase {
	return strategyCtx.TestCases
}

// checkConvergence 检查是否收敛
func (s *IterativeStrategy) checkConvergence(improvement float64, history []IterationRecord) bool {
	// 如果改进小于阈值，认为已收敛
	if improvement < s.config.ConvergenceThreshold {
		return true
	}

	// 如果连续多次改进都很小，也认为收敛
	if len(history) >= 3 {
		recentImprovements := []float64{}
		for i := len(history) - 3; i < len(history)-1; i++ {
			imp := history[i+1].Score - history[i].Score
			recentImprovements = append(recentImprovements, imp)
		}

		allSmall := true
		for _, imp := range recentImprovements {
			if imp >= s.config.ConvergenceThreshold {
				allSmall = false
				break
			}
		}

		if allSmall {
			return true
		}
	}

	return false
}

// shouldEarlyStop 是否应该早停
func (s *IterativeStrategy) shouldEarlyStop(improvement float64, history []IterationRecord) bool {
	if len(history) < 2 {
		return false
	}

	// 如果连续两次迭代都没有改进
	if improvement <= 0 && len(history) >= 2 {
		lastImprovement := history[len(history)-1].Score - history[len(history)-2].Score
		if lastImprovement <= 0 {
			return true
		}
	}

	return false
}

// IterationRecord 迭代记录
type IterationRecord struct {
	Iteration int
	Prompt    string
	Score     float64
	Variants  int
}

// 辅助函数

func removeCodeBlockMarkers(text string) string {
	// 简单实现：移除开头和结尾的代码块标记
	if len(text) > 6 {
		if text[:3] == "```" {
			// 找到第一个换行
			idx := 0
			for i, ch := range text {
				if ch == '\n' {
					idx = i + 1
					break
				}
			}
			text = text[idx:]
		}

		if len(text) > 3 && text[len(text)-3:] == "```" {
			text = text[:len(text)-3]
		}
	}

	return text
}

func normalizeWhitespace(text string) string {
	// 简单实现：将多个连续空行替换为两个空行
	// 实际应该使用正则表达式
	return text
}

// DefaultIterativeConfig 默认迭代配置
func DefaultIterativeConfig() IterativeConfig {
	return IterativeConfig{
		MaxIterations:        3,
		ConvergenceThreshold: 0.02,
		VariantsPerIteration: 2,
		EarlyStoppingEnabled: true,
		ImprovementThreshold: 0.01,
		OptimizerModel:       "gpt-4",
	}
}
