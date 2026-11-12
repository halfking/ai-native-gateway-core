package evaluation

import (
	"context"
	"fmt"
	"math"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
)

// Evaluator 评估器接口实现
type Evaluator struct {
	config    EvaluatorConfig
	metrics   []Metric
	llmClient LLMClient
}

// EvaluatorConfig 评估器配置
type EvaluatorConfig struct {
	// 并发评估
	Concurrent bool
	MaxWorkers int

	// 评估超时（秒）
	Timeout int

	// 使用的评估模型
	EvaluatorModel string
}

// Metric 评估指标接口
type Metric interface {
	Name() string
	Calculate(ctx context.Context, expected, actual string) (float64, error)
	Weight() float64
}

// LLMClient LLM 客户端接口。
//
// 参数类型用 llm.Options（基座包），避免各层自定义同名但不同型的 Options
// 导致实现无法互相赋值 —— 这类"结构相同但类型不同"的坑在 Go 里编译期
// 才发现，代价很高。
type LLMClient interface {
	Generate(ctx context.Context, prompt string, options llm.Options) (string, error)
}

// GenerateOptions 生成选项（llm.Options 的别名，保持旧代码可编译）。
type GenerateOptions = llm.Options

// NewEvaluator 创建评估器
func NewEvaluator(cfg EvaluatorConfig, metrics []Metric, client LLMClient) *Evaluator {
	if len(metrics) == 0 {
		metrics = DefaultMetrics()
	}

	return &Evaluator{
		config:    cfg,
		metrics:   metrics,
		llmClient: client,
	}
}

// Evaluate 评估提示词（实现optimizer.Evaluator接口）
func (e *Evaluator) Evaluate(ctx context.Context, prompt string, opts optimizer.EvaluationOptions) (float64, error) {
	if len(opts.TestCases) == 0 {
		return 0.0, fmt.Errorf("no test cases provided")
	}

	totalScore := 0.0

	for _, testCase := range opts.TestCases {
		score, err := e.EvaluatePrompt(ctx, prompt, testCase)
		if err != nil {
			return 0.0, fmt.Errorf("evaluate test case %s: %w", testCase.ID, err)
		}
		totalScore += score
	}

	avgScore := totalScore / float64(len(opts.TestCases))
	return avgScore, nil
}

// EvaluatePrompt 评估单个提示词
func (e *Evaluator) EvaluatePrompt(ctx context.Context, prompt string, testCase optimizer.TestCase) (float64, error) {
	// 1. 使用提示词生成输出
	output, err := e.generateOutput(ctx, prompt, testCase.Input)
	if err != nil {
		return 0.0, fmt.Errorf("generate output: %w", err)
	}

	// 2. 计算各项指标
	scores := make(map[string]float64)
	totalWeight := 0.0

	for _, metric := range e.metrics {
		score, err := metric.Calculate(ctx, testCase.Expected, output)
		if err != nil {
			continue
		}

		scores[metric.Name()] = score
		totalWeight += metric.Weight()
	}

	// 3. 计算加权平均分
	if totalWeight == 0 {
		return 0.0, fmt.Errorf("no valid metrics")
	}

	weightedScore := 0.0
	for _, metric := range e.metrics {
		score := scores[metric.Name()]
		weight := metric.Weight() / totalWeight
		weightedScore += score * weight
	}

	return weightedScore, nil
}

// MetricBreakdown 逐指标得分明细（供上层展示"哪一项涨了/掉了"）。
//
// 与 Evaluate 的区别：Evaluate 只给加权总分，MetricBreakdown 给出每个指标
// 在全部测试用例上的平均分。优化器的 OptimizeWithEvaluation 会按类型断言
// 探测本方法，探测不到时静默降级为空 map（不影响主流程）。
func (e *Evaluator) MetricBreakdown(ctx context.Context, prompt string, opts optimizer.EvaluationOptions) (map[string]float64, error) {
	if len(opts.TestCases) == 0 {
		return nil, fmt.Errorf("no test cases provided")
	}

	sums := make(map[string]float64, len(e.metrics))
	counts := make(map[string]int, len(e.metrics))

	for _, testCase := range opts.TestCases {
		output, err := e.generateOutput(ctx, prompt, testCase.Input)
		if err != nil {
			return nil, fmt.Errorf("generate output for %s: %w", testCase.ID, err)
		}
		for _, metric := range e.metrics {
			score, err := metric.Calculate(ctx, testCase.Expected, output)
			if err != nil {
				continue
			}
			sums[metric.Name()] += score
			counts[metric.Name()]++
		}
	}

	breakdown := make(map[string]float64, len(sums))
	for name, sum := range sums {
		if counts[name] == 0 {
			continue
		}
		breakdown[name] = sum / float64(counts[name])
	}
	return breakdown, nil
}

// generateOutput 生成输出
func (e *Evaluator) generateOutput(ctx context.Context, prompt string, input string) (string, error) {
	if e.llmClient == nil {
		return "", fmt.Errorf("llm client not configured")
	}

	// 组合提示词和输入
	fullPrompt := prompt + "\n\n# 输入\n" + input

	output, err := e.llmClient.Generate(ctx, fullPrompt, GenerateOptions{
		Model:       e.config.EvaluatorModel,
		Temperature: 0.3,
		MaxTokens:   1000,
	})

	return output, err
}

// Compare 对比两个提示词
func (e *Evaluator) Compare(ctx context.Context, promptA, promptB string, testCases []optimizer.TestCase) (*ComparisonResult, error) {
	scoreA, err := e.Evaluate(ctx, promptA, optimizer.EvaluationOptions{TestCases: testCases})
	if err != nil {
		return nil, fmt.Errorf("evaluate prompt A: %w", err)
	}

	scoreB, err := e.Evaluate(ctx, promptB, optimizer.EvaluationOptions{TestCases: testCases})
	if err != nil {
		return nil, fmt.Errorf("evaluate prompt B: %w", err)
	}

	result := &ComparisonResult{
		PromptA:    promptA,
		PromptB:    promptB,
		ScoreA:     scoreA,
		ScoreB:     scoreB,
		Difference: scoreB - scoreA,
		Winner:     "A",
	}

	if scoreB > scoreA {
		result.Winner = "B"
	} else if math.Abs(scoreB-scoreA) < 0.01 {
		result.Winner = "Tie"
	}

	return result, nil
}

// ComparisonResult 对比结果
type ComparisonResult struct {
	PromptA    string
	PromptB    string
	ScoreA     float64
	ScoreB     float64
	Difference float64
	Winner     string
}

// DefaultEvaluatorConfig 默认评估器配置
func DefaultEvaluatorConfig() EvaluatorConfig {
	return EvaluatorConfig{
		Concurrent:     false,
		MaxWorkers:     4,
		Timeout:        30,
		EvaluatorModel: "gpt-4",
	}
}

// 内置评估指标

// AccuracyMetric 准确性指标
type AccuracyMetric struct{}

func (m *AccuracyMetric) Name() string    { return "accuracy" }
func (m *AccuracyMetric) Weight() float64 { return 1.0 }
func (m *AccuracyMetric) Calculate(ctx context.Context, expected, actual string) (float64, error) {
	// 简化实现：基于字符串相似度
	similarity := calculateStringSimilarity(expected, actual)
	return similarity, nil
}

// CompletenessMetric 完整性指标
type CompletenessMetric struct{}

func (m *CompletenessMetric) Name() string    { return "completeness" }
func (m *CompletenessMetric) Weight() float64 { return 0.8 }
func (m *CompletenessMetric) Calculate(ctx context.Context, expected, actual string) (float64, error) {
	// 注意：必须先把长度转成 float64 再乘系数。
	// 旧写法 len(expected)*0.5 会把无类型常量 0.5 直接转成 int 截断为 0，
	// 令本指标恒真（永远返回 1.0），即"缺覆盖的全绿"。
	expLen := float64(len(expected))
	actLen := float64(len(actual))

	if expLen == 0 {
		// 没有期望答案时，只要产出了内容即视为完整。
		if actLen > 0 {
			return 1.0, nil
		}
		return 0.0, nil
	}

	ratio := actLen / expLen
	switch {
	case ratio < 0.5:
		return 0.5, nil
	case ratio >= 0.8:
		return 1.0, nil
	default:
		return 0.75, nil
	}
}

// ConsistencyMetric 一致性指标
type ConsistencyMetric struct{}

func (m *ConsistencyMetric) Name() string    { return "consistency" }
func (m *ConsistencyMetric) Weight() float64 { return 0.6 }
func (m *ConsistencyMetric) Calculate(ctx context.Context, expected, actual string) (float64, error) {
	// 检查格式一致性
	score := 0.0
	if len(actual) > 0 {
		score += 0.5
	}
	if len(actual) > 10 {
		score += 0.5
	}
	return score, nil
}

// TokenUsageMetric Token使用指标
type TokenUsageMetric struct{}

func (m *TokenUsageMetric) Name() string    { return "token_usage" }
func (m *TokenUsageMetric) Weight() float64 { return 0.3 }
func (m *TokenUsageMetric) Calculate(ctx context.Context, expected, actual string) (float64, error) {
	// 估算token使用（简化：字符数/4）
	tokens := len(actual) / 4
	if tokens < 100 {
		return 1.0, nil
	}
	if tokens < 500 {
		return 0.8, nil
	}
	if tokens < 1000 {
		return 0.6, nil
	}
	return 0.4, nil
}

// DefaultMetrics 默认指标集
func DefaultMetrics() []Metric {
	return []Metric{
		&AccuracyMetric{},
		&CompletenessMetric{},
		&ConsistencyMetric{},
	}
}

// 辅助函数

// calculateStringSimilarity 计算字符串相似度
func calculateStringSimilarity(s1, s2 string) float64 {
	// 简化实现：Levenshtein距离的归一化版本
	if len(s1) == 0 && len(s2) == 0 {
		return 1.0
	}

	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	maxLen := float64(max(len(s1), len(s2)))
	distance := float64(levenshteinDistance(s1, s2))

	similarity := 1.0 - (distance / maxLen)
	if similarity < 0 {
		similarity = 0
	}

	return similarity
}

// levenshteinDistance 计算Levenshtein距离
func levenshteinDistance(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	// 创建矩阵
	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
	}

	// 初始化
	for i := 0; i <= len(s1); i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len(s2); j++ {
		matrix[0][j] = j
	}

	// 计算距离
	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}

			matrix[i][j] = min(
				matrix[i-1][j]+1,      // 删除
				matrix[i][j-1]+1,      // 插入
				matrix[i-1][j-1]+cost, // 替换
			)
		}
	}

	return matrix[len(s1)][len(s2)]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
