package strategies

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// FewShotStrategy Few-shot示例优化策略
// 自动选择和组织高质量示例以提升提示词效果
type FewShotStrategy struct {
	config       FewShotConfig
	exampleStore ExampleStore
}

// FewShotConfig Few-shot策略配置
type FewShotConfig struct {
	// 最大示例数量
	MaxExamples int

	// 示例选择策略: diversity, similarity, difficulty
	SelectionStrategy string

	// 是否添加示例说明
	AddExplanations bool

	// 示例质量阈值 (0-1)
	QualityThreshold float64

	// 是否动态调整示例数量
	DynamicCount bool
}

// ExampleStore 示例存储接口
type ExampleStore interface {
	// GetExamples 获取示例
	GetExamples(ctx context.Context, taskType, domain string) ([]optimizer.Example, error)

	// FindSimilar 查找相似示例
	FindSimilar(ctx context.Context, input string, limit int) ([]optimizer.Example, error)

	// RankExamples 对示例进行排序
	RankExamples(ctx context.Context, examples []optimizer.Example, criteria string) []optimizer.Example
}

// NewFewShot 创建Few-shot策略
func NewFewShot(cfg FewShotConfig, store ExampleStore) optimizer.Strategy {
	return &FewShotStrategy{
		config:       cfg,
		exampleStore: store,
	}
}

// Name 策略名称
func (f *FewShotStrategy) Name() string {
	return "fewshot"
}

// Apply 应用Few-shot策略
func (f *FewShotStrategy) Apply(ctx context.Context, prompt string, strategyCtx optimizer.StrategyContext) (string, error) {
	// 1. 获取候选示例
	examples, err := f.getCandidateExamples(ctx, strategyCtx)
	if err != nil {
		return "", fmt.Errorf("get candidate examples: %w", err)
	}

	// 2. 过滤低质量示例
	examples = f.filterByQuality(examples)

	// 3. 选择最佳示例
	selected := f.selectExamples(examples, strategyCtx)

	// 4. 如果没有合适的示例，返回原提示词
	if len(selected) == 0 {
		return prompt, nil
	}

	// 5. 构建包含示例的提示词
	enhanced := f.buildPromptWithExamples(prompt, selected)

	return enhanced, nil
}

// Score 评估提示词质量
func (f *FewShotStrategy) Score(ctx context.Context, prompt string, testCase optimizer.TestCase) (float64, error) {
	score := 0.0

	// 检查是否包含示例
	if strings.Contains(prompt, "示例") || strings.Contains(prompt, "Example") {
		score += 0.3
	}

	// 检查示例数量（通过计数"输入:"或"Input:"）
	exampleCount := strings.Count(prompt, "输入:") + strings.Count(prompt, "Input:")
	if exampleCount >= 1 && exampleCount <= 5 {
		score += 0.4
	}

	// 检查是否有示例说明
	if strings.Contains(prompt, "说明:") || strings.Contains(prompt, "Explanation:") {
		score += 0.3
	}

	return score, nil
}

// getCandidateExamples 获取候选示例
func (f *FewShotStrategy) getCandidateExamples(ctx context.Context, strategyCtx optimizer.StrategyContext) ([]optimizer.Example, error) {
	if f.exampleStore == nil {
		// 如果没有示例存储，使用预定义示例
		return f.getBuiltInExamples(strategyCtx), nil
	}

	// 从存储中获取示例
	examples, err := f.exampleStore.GetExamples(ctx, strategyCtx.TaskType, strategyCtx.Domain)
	if err != nil {
		return nil, err
	}

	// 合并预定义示例和存储中的示例
	builtIn := f.getBuiltInExamples(strategyCtx)
	examples = append(examples, builtIn...)

	return examples, nil
}

// getBuiltInExamples 获取预定义示例
func (f *FewShotStrategy) getBuiltInExamples(strategyCtx optimizer.StrategyContext) []optimizer.Example {
	examples := []optimizer.Example{}

	switch strategyCtx.TaskType {
	case "translation":
		examples = append(examples,
			optimizer.Example{
				Input:       "Hello, world!",
				Output:      "你好，世界！",
				Explanation: "简单的问候语翻译，保持标点符号风格",
				Quality:     0.95,
			},
			optimizer.Example{
				Input:       "Machine learning is transforming industries.",
				Output:      "机器学习正在改变各行各业。",
				Explanation: "技术术语翻译，注意专业性和流畅度",
				Quality:     0.92,
			},
		)
	case "code_generation":
		examples = append(examples,
			optimizer.Example{
				Input: "写一个函数计算斐波那契数列的第n项",
				Output: `def fibonacci(n):
    if n <= 1:
        return n
    return fibonacci(n-1) + fibonacci(n-2)`,
				Explanation: "递归实现，简洁但性能较低，适合小规模使用",
				Quality:     0.88,
			},
		)
	case "qa":
		examples = append(examples,
			optimizer.Example{
				Input:       "什么是提示词工程？",
				Output:      "提示词工程是设计和优化输入提示以获得AI模型最佳输出的技术。它涉及结构化指令、添加上下文、提供示例等方法，以提高模型响应的准确性和相关性。",
				Explanation: "清晰定义+具体方法+价值说明的完整回答结构",
				Quality:     0.90,
			},
		)
	}

	return examples
}

// filterByQuality 按质量过滤示例
func (f *FewShotStrategy) filterByQuality(examples []optimizer.Example) []optimizer.Example {
	filtered := []optimizer.Example{}

	for _, ex := range examples {
		if ex.Quality >= f.config.QualityThreshold {
			filtered = append(filtered, ex)
		}
	}

	return filtered
}

// selectExamples 选择最佳示例
func (f *FewShotStrategy) selectExamples(examples []optimizer.Example, strategyCtx optimizer.StrategyContext) []optimizer.Example {
	if len(examples) == 0 {
		return []optimizer.Example{}
	}

	// 确定要选择的示例数量
	count := f.config.MaxExamples
	if f.config.DynamicCount {
		count = f.calculateOptimalCount(examples, strategyCtx)
	}

	if count > len(examples) {
		count = len(examples)
	}

	// 根据选择策略选择示例
	switch f.config.SelectionStrategy {
	case "diversity":
		return f.selectByDiversity(examples, count)
	case "similarity":
		return f.selectBySimilarity(examples, count, strategyCtx)
	case "difficulty":
		return f.selectByDifficulty(examples, count)
	default:
		// 默认按质量选择
		return f.selectByQuality(examples, count)
	}
}

// selectByQuality 按质量选择
func (f *FewShotStrategy) selectByQuality(examples []optimizer.Example, count int) []optimizer.Example {
	// 按质量降序排序
	sort.Slice(examples, func(i, j int) bool {
		return examples[i].Quality > examples[j].Quality
	})

	if count > len(examples) {
		count = len(examples)
	}

	return examples[:count]
}

// selectByDiversity 按多样性选择
func (f *FewShotStrategy) selectByDiversity(examples []optimizer.Example, count int) []optimizer.Example {
	if len(examples) <= count {
		return examples
	}

	selected := []optimizer.Example{}
	remaining := make([]optimizer.Example, len(examples))
	copy(remaining, examples)

	// 先选择质量最高的
	selected = append(selected, remaining[0])
	remaining = remaining[1:]

	// 迭代选择最不相似的示例
	for len(selected) < count && len(remaining) > 0 {
		maxDiversity := -1.0
		maxIdx := 0

		for i, candidate := range remaining {
			minSimilarity := 1.0
			for _, sel := range selected {
				similarity := f.calculateSimilarity(candidate, sel)
				if similarity < minSimilarity {
					minSimilarity = similarity
				}
			}

			if minSimilarity > maxDiversity {
				maxDiversity = minSimilarity
				maxIdx = i
			}
		}

		selected = append(selected, remaining[maxIdx])
		remaining = append(remaining[:maxIdx], remaining[maxIdx+1:]...)
	}

	return selected
}

// selectBySimilarity 按相似度选择
func (f *FewShotStrategy) selectBySimilarity(examples []optimizer.Example, count int, strategyCtx optimizer.StrategyContext) []optimizer.Example {
	// 简化实现：返回质量最高的几个
	// 实际应该根据任务上下文计算相似度
	return f.selectByQuality(examples, count)
}

// selectByDifficulty 按难度选择（递进式）
func (f *FewShotStrategy) selectByDifficulty(examples []optimizer.Example, count int) []optimizer.Example {
	// 简化实现：假设输入长度代表难度
	sort.Slice(examples, func(i, j int) bool {
		return len(examples[i].Input) < len(examples[j].Input)
	})

	if count > len(examples) {
		count = len(examples)
	}

	return examples[:count]
}

// calculateSimilarity 计算示例相似度
func (f *FewShotStrategy) calculateSimilarity(a, b optimizer.Example) float64 {
	// 简化实现：基于输入长度差异
	lenA := float64(len(a.Input))
	lenB := float64(len(b.Input))

	diff := math.Abs(lenA - lenB)
	maxLen := math.Max(lenA, lenB)

	if maxLen == 0 {
		return 1.0
	}

	return 1.0 - (diff / maxLen)
}

// calculateOptimalCount 计算最优示例数量
func (f *FewShotStrategy) calculateOptimalCount(examples []optimizer.Example, strategyCtx optimizer.StrategyContext) int {
	// 基于任务复杂度和示例质量动态调整
	avgQuality := 0.0
	for _, ex := range examples {
		avgQuality += ex.Quality
	}
	avgQuality /= float64(len(examples))

	// 质量越高，需要的示例越少
	if avgQuality > 0.9 {
		return 2
	} else if avgQuality > 0.8 {
		return 3
	} else if avgQuality > 0.7 {
		return 4
	}

	return 5
}

// buildPromptWithExamples 构建包含示例的提示词
func (f *FewShotStrategy) buildPromptWithExamples(prompt string, examples []optimizer.Example) string {
	var builder strings.Builder

	// 添加原始提示词
	builder.WriteString(prompt)
	builder.WriteString("\n\n")

	// 添加示例部分
	builder.WriteString("# 参考示例\n\n")
	builder.WriteString("以下是一些参考示例，帮助你更好地理解任务要求：\n\n")

	for i, ex := range examples {
		builder.WriteString(fmt.Sprintf("## 示例 %d\n\n", i+1))
		builder.WriteString(fmt.Sprintf("**输入:**\n```\n%s\n```\n\n", ex.Input))
		builder.WriteString(fmt.Sprintf("**输出:**\n```\n%s\n```\n\n", ex.Output))

		if f.config.AddExplanations && ex.Explanation != "" {
			builder.WriteString(fmt.Sprintf("**说明:** %s\n\n", ex.Explanation))
		}
	}

	builder.WriteString("请参考以上示例的格式和质量标准完成任务。")

	return builder.String()
}

// DefaultFewShotConfig 默认Few-shot配置
func DefaultFewShotConfig() FewShotConfig {
	return FewShotConfig{
		MaxExamples:       3,
		SelectionStrategy: "diversity",
		AddExplanations:   true,
		QualityThreshold:  0.8,
		DynamicCount:      true,
	}
}
