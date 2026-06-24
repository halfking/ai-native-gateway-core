package autoroute

import (
	"strings"
	"time"
)

// scoring_new.go — 新增的 8 维评分函数（需求 #3）
//
// scoreVersionRecency 和 scoreStrengthMatch

// scoreVersionRecency 根据模型发布时间和版本级次计算新旧度评分（0-100）。
//
// 策略：
//   - 高难度任务（reasoning/agent/code/long_context）→ 最新版得分最高（released_at 最近 180 天 = 100）
//   - 普通任务（chat/creative/function_call/vision）→ 次新版得分最高（version_rank=2 = 100，最新版略低 80）
//   - 按 released_at 衰减：每 365 天衰减到 50%（指数衰减）
//   - 未知 released_at 或 version_rank → 50（中性，不惩罚也不奖励）
//
// 高难度任务定义：需要最新推理能力、上下文理解、代码生成的任务。
func scoreVersionRecency(c Candidate, task TaskType) float64 {
	// 未知发布时间 → 中性分
	if c.ReleasedAt == nil {
		return 50
	}

	now := time.Now()
	daysSinceRelease := int(now.Sub(*c.ReleasedAt).Hours() / 24)
	if daysSinceRelease < 0 {
		daysSinceRelease = 0 // 未来日期（数据错误），当作今天发布
	}

	// 判断是否为高难度任务
	isHighDifficulty := task == TaskReasoning || task == TaskAgent ||
		task == TaskCode || task == TaskLongContext

	if isHighDifficulty {
		// 高难度任务：最新版（180 天内）得分最高 100，逐年衰减
		// 公式：score = 100 * exp(-days / 365)
		// 0 天 = 100, 180 天 ≈ 90, 365 天 = 50, 730 天 = 25
		decayFactor := float64(daysSinceRelease) / 365.0
		score := 100.0 * exp(-decayFactor)
		if score < 10 {
			score = 10 // 最低保底 10 分（避免老版本完全被排除）
		}
		return score
	}

	// 普通任务：次新版得分最高（避免普通任务浪费最新版资源）
	// version_rank = 1（最新版）→ 80 分
	// version_rank = 2（次新版）→ 100 分
	// version_rank >= 3（稳定版）→ 按 released_at 衰减
	if c.VersionRank == 1 {
		// 最新版在普通任务上略低（鼓励用次新版）
		return 80
	}
	if c.VersionRank == 2 {
		// 次新版（sweet spot for 普通任务）
		return 100
	}

	// version_rank >= 3 或 未设置：按 released_at 衰减
	// 365 天内 = 70-90，1-2 年 = 50-70，2 年+ = 30-50
	decayFactor := float64(daysSinceRelease) / 730.0 // 2 年衰减周期
	score := 90.0 * exp(-decayFactor)
	if score < 20 {
		score = 20
	}
	return score
}

// scoreStrengthMatch 计算候选模型的优势方向与任务类型的匹配度（0-100）。
//
// 与 scoreMatch（基于 tags）的区别：
//   - tags 是宽泛的能力标签（reasoning/code/...），自动化生成或推断
//   - strengths 是运营人工标注的优势方向，更精准（如 "math"/"long_context"/"multimodal"）
//
// 公式：
//   required = 任务类型所需的优势方向集合（定义见下）
//   hits = |required ∩ candidate.strengths|
//   score = (hits / |required|) × 100，至少 50（避免未标注模型被过度惩罚）
//
// 任务 → 优势方向映射：
//   reasoning    : ["reasoning", "math", "logic"]
//   code         : ["code", "programming"]
//   agent        : ["agent", "tool_use"]
//   creative     : ["creative", "writing"]
//   long_context : ["long_context"]
//   vision       : ["vision", "multimodal"]
//   function_call: ["tool_use", "function_call"]
//   chat         : []  (无特定要求，返回 75 基准分)
func scoreStrengthMatch(c Candidate, task TaskType) float64 {
	required := requiredStrengthsForTask(task)
	if len(required) == 0 {
		// chat 或未知任务 → 基准分 75（不惩罚也不特别奖励）
		return 75
	}

	// 候选模型未标注 strengths → 返回 50（中性，但低于有标注的）
	if len(c.Strengths) == 0 {
		return 50
	}

	// 计算交集
	hits := 0
	for _, r := range required {
		for _, s := range c.Strengths {
			if strings.EqualFold(s, r) || containsFold(s, r) {
				hits++
				break
			}
		}
	}

	// 匹配度：hits / required，映射到 50-100 区间
	// 全匹配 = 100，0 匹配 = 50
	matchRatio := float64(hits) / float64(len(required))
	score := 50 + matchRatio*50
	return score
}

// requiredStrengthsForTask 返回任务类型所需的优势方向列表。
//
// 与 requiredTagsForTask 的区别：strengths 更精准，要求更严格。
func requiredStrengthsForTask(task TaskType) []string {
	switch task {
	case TaskReasoning:
		return []string{"reasoning", "math", "logic"}
	case TaskCode:
		return []string{"code", "programming"}
	case TaskAgent:
		return []string{"agent", "tool_use"}
	case TaskCreative:
		return []string{"creative", "writing"}
	case TaskLongContext:
		return []string{"long_context"}
	case TaskVision:
		return []string{"vision", "multimodal"}
	case TaskFunctionCall:
		return []string{"tool_use", "function_call"}
	case TaskChat:
		return []string{} // 无特定要求
	default:
		return []string{}
	}
}

// exp 是简单的 e^x 近似（泰勒展开前 5 项），避免引入 math 包。
// 精度：|error| < 0.01 for x ∈ [-2, 2]
func exp(x float64) float64 {
	if x < -5 {
		return 0
	}
	if x > 5 {
		return 148.4 // e^5 ≈ 148.4
	}
	// e^x ≈ 1 + x + x²/2 + x³/6 + x⁴/24 + x⁵/120
	result := 1.0
	term := 1.0
	for i := 1; i <= 10; i++ {
		term *= x / float64(i)
		result += term
		if term < 0.00001 && term > -0.00001 {
			break
		}
	}
	return result
}
