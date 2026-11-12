package evaluation

import (
	"context"
	"fmt"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// PromptQualityMetric 提示词结构质量指标。
//
// 与 Accuracy/Completeness 的本质区别：后两者衡量"**模型输出**像不像期望答案"，
// 需要真的跑一次模型；本指标衡量"**提示词本身**写得好不好"，
// 只看结构与要素完备性 ⇒ 无需 LLM、确定性、可在离线环境跑。
//
// 适用场景：对比两个候选提示词哪个更好（/api/v1/compare 的默认口径）、
// 在没有模型通道时做结构性体检。
type PromptQualityMetric struct {
	// Weight 权重。
	WeightValue float64
}

// Name 指标名。
func (m *PromptQualityMetric) Name() string { return "prompt_quality" }

// Weight 权重。
func (m *PromptQualityMetric) Weight() float64 {
	if m.WeightValue <= 0 {
		return 1.0
	}
	return m.WeightValue
}

// Calculate 计算提示词结构质量分（0..1）。
//
// ctx/expected/actual 三个入参在本指标里不使用 —— 签名与 Metric 接口保持一致，
// 便于与其它指标混用。
func (m *PromptQualityMetric) Calculate(ctx context.Context, expected, actual string) (float64, error) {
	return ScorePromptQuality(actual), nil
}

// 结构要素定义：缺一项就扣分。
var promptQualityAspects = []struct {
	// name 要素名（仅用于错误信息）。
	name string
	// markers 该要素的特征标记（任一命中即算具备）。
	markers []string
	// weight 该要素在满分中的占比。
	weight float64
}{
	{"角色定义", []string{"# 角色定义", "你是一个", "你是一名", "Role:"}, 0.2},
	{"任务目标", []string{"# 任务目标", "## 目标", "任务:", "你的任务"}, 0.2},
	{"约束条件", []string{"# 约束条件", "## 约束", "必须", "不得", "禁止"}, 0.15},
	{"输出格式", []string{"# 输出格式", "## 输出格式", "输出格式", "请按以下格式"}, 0.15},
	{"执行步骤", []string{"# 执行步骤", "## 步骤", "1.", "2.", "步骤"}, 0.1},
	{"质量检查", []string{"# 质量检查", "## 质量", "自检", "检查:"}, 0.1},
	{"示例参考", []string{"# 参考示例", "## 示例", "示例 1", "Example 1"}, 0.05},
	{"错误处理", []string{"# 错误处理", "## 错误", "遇到错误"}, 0.05},
}

// ScorePromptQuality 给一段提示词打结构质量分（0..1）。
//
// 口径：按"要素完备度"打分 —— 缺失的要素按权重扣分。
// 这是**结构**评分，不是**效果**评分：它不能证明提示词效果更好，
// 只能证明提示词该有的要素齐不齐。文档已明确标注这一边界。
func ScorePromptQuality(prompt string) float64 {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return 0.0
	}

	score := 0.0
	for _, aspect := range promptQualityAspects {
		if containsAny(trimmed, aspect.markers) {
			score += aspect.weight
		}
	}
	return score
}

// containsAny 判断文本是否包含任一标记。
func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// EvaluatePromptQuality 对提示词做结构质量评估（不需要 LLM）。
func (e *Evaluator) EvaluatePromptQuality(ctx context.Context, prompt string) (float64, error) {
	if strings.TrimSpace(prompt) == "" {
		return 0.0, fmt.Errorf("evaluate prompt quality: prompt must not be empty")
	}
	return ScorePromptQuality(prompt), nil
}

// PromptQualityBreakdown 逐要素的完备情况（便于定位"缺了哪一块"）。
func (e *Evaluator) PromptQualityBreakdown(ctx context.Context, prompt string) (map[string]bool, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt quality breakdown: prompt must not be empty")
	}
	out := make(map[string]bool, len(promptQualityAspects))
	for _, aspect := range promptQualityAspects {
		out[aspect.name] = containsAny(prompt, aspect.markers)
	}
	return out, nil
}

// compile-time 断言：PromptQualityMetric 必须满足 Metric 接口。
var _ Metric = (*PromptQualityMetric)(nil)

// compile-time 断言：Evaluator 满足 optimizer.Evaluator 接口。
var _ optimizer.Evaluator = (*Evaluator)(nil)
