package executors

import (
	"context"
	"math"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// costOptimizedStrategy 偏好单价（in+out per 1M tokens）最低的候选。
//
// Unknown 值语义（README §5 R1 硬约束）：未知价格不是免费。
// PriceInPer1M / PriceOutPer1M 为 *float64，nil 表示未知 → 返回最大惩罚分，
// 让该候选排到已知价格的候选之后，绝不当作零成本。
type costOptimizedStrategy struct{}

func (costOptimizedStrategy) Name() string { return "cost-optimized" }

func (s costOptimizedStrategy) Score(_ context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	if c.PriceInPer1M == nil || c.PriceOutPer1M == nil {
		// 未知价格 → 最大惩罚（不是免费）。
		return math.MaxFloat64, nil
	}
	// 混合单价：in + out（per 1M tokens）。lower = better。
	return *c.PriceInPer1M + *c.PriceOutPer1M, nil
}
