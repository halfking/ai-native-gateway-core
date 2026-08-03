package executors

import (
	"context"
	"math"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// contextAwareStrategy 偏好上下文窗口最大的候选。
//
// Unknown 值语义：未知 context 不是无限。ContextWindow 为 *int，nil 表示未知
// → 最大惩罚分，绝不当作无限容量候选。
//
// 评分方向：lower = better，但 context 越大越好，所以取负 context window
// （越大窗口 → 越负分 → 越优先）。为避免负数混淆 shadow diff，平移到正区间：
// score = -contextWindow，但 P2C/shadow 约定 lower=better，所以直接用
// -contextWindow（未知用 +MaxFloat64 惩罚）。
type contextAwareStrategy struct{}

func (contextAwareStrategy) Name() string { return "context-aware" }

func (s contextAwareStrategy) Score(_ context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	if c.ContextWindow == nil || *c.ContextWindow <= 0 {
		// 未知 / 非正 → 最大惩罚（不是无限）。
		return math.MaxFloat64, nil
	}
	// lower = better；context 越大越好 → 返回 -window。
	return -float64(*c.ContextWindow), nil
}
