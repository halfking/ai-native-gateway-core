package executors

import (
	"context"
	"math"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// headroomStrategy 偏好剩余并发余量（headroom）最大的候选。
//
// 复用现有 calculateHeadroom（router_scoring.go:196）的语义：headroom 越大
// 越健康。但 headroom 不能绕过健康过滤——不健康候选已在 Router 的 state/health
// 阶段被过滤，到 strategy 步的候选都是 routable 的。本策略只在 routable 候选
// 里选余量最大的。
//
// Unknown 值语义：ConcurrencyLimit 未知（nil）→ 中性分（不奖励也不惩罚），
// 不绕过健康过滤。用 0.5（headroom 中位）作为中性分。
//
// 评分方向：lower = better，headroom 越大越好 → 返回 (1 - headroom)。
type headroomStrategy struct {
	r *Router
}

func (s headroomStrategy) Name() string { return "headroom" }

func (s headroomStrategy) Score(ctx context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	if c.ConcurrencyLimit == nil {
		// 并发上限未知 → 中性分，不绕过健康过滤。
		return 0.5, nil
	}
	// calculateHeadroom 返回 [0,1]，越大越好。lower=better → 1-headroom。
	h := calculateHeadroom(c)
	if h < 0 {
		h = 0
	}
	if h > 1 {
		h = 1
	}
	// 若 ctx 里有 limiter，进一步用实时占用修正（与 P2C 一致）。
	// 这里保守：只用静态 calculateHeadroom，避免 shadow 引入额外副作用。
	return 1.0 - h, nil
}

// 避免 unused import（math 用于 MaxFloat64 的对称性，若未来加惩罚用）。
var _ = math.MaxFloat64
