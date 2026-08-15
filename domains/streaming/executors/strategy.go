package executors

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// StrategyInput 携带评分所需的路由上下文（不含 secret）。
// 现有 P2C 用 LoadScoreWeights + Router 内部 limiter/fpslot 状态；
// 新策略（GW-04 cost/cache/context/headroom）主要用 Candidate 自身字段，
// StrategyInput 提供它们可能需要的 request 级元数据。
type StrategyInput struct {
	Policy           *provider.Policy
	EgressPreference []string
	TenantID         string
	Canonical        string
	RequestID        string
	LoadScoreWeights LoadScoreWeights
}

// Strategy 是 omni-ref2 GW-03 的窄路由策略接口。
//
// 设计约束（README §5 R1）：
//   - 只翻译评分函数，不复制 OmniRoute 的路由编排。
//   - 候选顺序保持：去重 → state/health → billing round → tier → strategy
//     → sticky/protocol affinity → canary。Strategy 只在 "strategy" 步生效，
//     且当前实现只在 ShadowStrategy 上使用——不改变实际选中候选。
//   - 未知值语义：未知价格不是免费，未知 context 不是无限，未知 headroom
//     不能绕过健康过滤。各实现（GW-04）必须遵守。
//
// Score 返回 float64，越低越好（与现有 calculateLoadScore / p2cOrder 一致方向，
// 便于 shadow diff 直接对比）。error 仅用于"该候选无法评分"（应被判最大惩罚，
// 而非让整个请求失败）；实现应尽量返回惩罚分而非 error。
type Strategy interface {
	// Name 返回稳定策略名（p2c|cost-optimized|cache-optimized|...），
	// 用作 metrics label 和 shadow diff 标识。
	Name() string
	// Score 对单个候选评分。lower = better。
	Score(ctx context.Context, cand provider.Candidate, in StrategyInput) (float64, error)
}

// p2cStrategy 把现有 P2C 的 loadScore 适配为 Strategy。
// 它是默认实现，Score 直接复用 calculateLoadScore（router_scoring.go:43），
// 保证 shadow 对比时与线上 P2C 行为一致。
type p2cStrategy struct {
	r *Router
}

func (p2cStrategy) Name() string { return "p2c" }

func (s p2cStrategy) Score(ctx context.Context, cand provider.Candidate, in StrategyInput) (float64, error) {
	// calculateLoadScore 需要 *Router 取 limiter/fpslot 状态；strategy 不持有 ctx
	// 里的 router 引用，通过 p2cStrategy.r 注入。
	return calculateLoadScore(cand, s.r, ctx, in.LoadScoreWeights), nil
}

// NewP2CStrategy 构造一个复用现有 P2C loadScore 的 Strategy。
// r 是宿主 Router，提供 limiter/fpslot/headroom 计算所需的运行时状态。
func NewP2CStrategy(r *Router) Strategy {
	return p2cStrategy{r: r}
}

// NewStrategyByName 按名字构造 shadow 策略。
//
// GW-03 仅提供 "p2c"（identity shadow，用于验证 shadow 管线本身）。
// GW-04 在此 switch 增加 cost-optimized/cache-optimized/context-aware/headroom。
// 未知名字返回 (nil, error)，调用方保持 ShadowStrategy=nil（现状）。
//
// 设计：策略注册集中在此函数，main.go 只读 env 名字调用本函数。
func NewStrategyByName(name string, r *Router) (Strategy, error) {
	switch name {
	case "", "off", "none":
		return nil, nil
	case "p2c":
		return NewP2CStrategy(r), nil
	case "cost-optimized":
		return costOptimizedStrategy{}, nil
	case "cache-optimized":
		return cacheOptimizedStrategy{}, nil
	case "context-aware":
		return contextAwareStrategy{}, nil
	case "headroom":
		return headroomStrategy{r: r}, nil
	default:
		return nil, fmt.Errorf("unknown shadow strategy %q (supported: p2c, cost-optimized, cache-optimized, context-aware, headroom)", name)
	}
}

// shadowOutcome 是 shadow diff 的结果分类，用作 metrics label（低基数）。
type shadowOutcome string

const (
	shadowAgreed    shadowOutcome = "agreed"    // shadow 最优候选 == 实际最优
	shadowDisagreed shadowOutcome = "disagreed" // 不同
)

// scoreWithShadow 对 bucket 用 actualOrder 给出的实际顺序，同时用 ShadowStrategy
// （如设置）独立评分，记录 agreed/disagreed metric。它不修改 actualOrder 返回的顺序。
//
// 这是 GW-03 的核心：线上 P2C/bandit 行为零变化，shadow 只观测。
// 复用 URSM v2 shadow diff 的观测模式（domains/ursm/v2/shadow/diff.go）。
func (r *Router) scoreWithShadow(ctx context.Context, bucket []provider.Candidate, actualOrder []provider.Candidate, in StrategyInput) {
	if r.ShadowStrategy == nil || len(actualOrder) == 0 {
		return
	}
	// shadow 评分，取最低分候选（lower = better）。
	var bestIdx int
	bestScore := -1.0
	for i, c := range bucket {
		score, err := r.ShadowStrategy.Score(ctx, c, in)
		if err != nil || score < 0 {
			// 无法评分/惩罚分；负分视为不可选，跳过。
			continue
		}
		if bestScore < 0 || score < bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	outcome := shadowAgreed
	// actualOrder[0] 是实际首选；bucket[bestIdx] 是 shadow 首选。
	if bestIdx >= 0 && bestIdx < len(bucket) {
		if bucket[bestIdx].CredentialID != actualOrder[0].CredentialID {
			outcome = shadowDisagreed
		}
	}
	recordShadowStrategyOutcome(r.ShadowStrategy.Name(), string(outcome))
}
