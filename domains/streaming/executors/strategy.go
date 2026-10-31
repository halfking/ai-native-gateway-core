package executors

import (
	"context"
	"fmt"
	"math"

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
	// StickySnapshot (R48 §4 B 方案)：planCandidates 调用一次
	// r.StickyLoad.Snapshot(ids) 后构造的本请求 sticky/recency 视图快照。
	// calculateLoadScore 优先读此 map 替代每候选 Info()，把 n×2 次写锁降为
	// 1 次。为 nil/缺 key 时回退 Info()，向后兼容旧 StrategyInput 构造面。
	StickySnapshot map[int]StickyLoadInfo
	// SuccessRatePoolStats (P1-A, 2026-10-28 两层路由审计 G3)：filterHealth
	// 之后整个候选池的成功率池统计（均值/平均绝对偏差/池深）。离群是【池】的
	// 属性不是单候选的属性 ⇒ 必须在池级算一次再随上下文下发，而不是在每个
	// 候选的 calculateLoadScore 里各算一遍。nil = 未接线（维度关闭，行为与
	// 接线前逐字节一致）。
	SuccessRatePoolStats *SuccessRatePoolStats
	// MaxCandidates 是本次请求的候选总数上限（routing.max_candidates 热加载）。
	//
	// ★ 2026-10-31 12h 审计轮 P2-2：★ 此前该上限是在 planByTier **内部**读的
	//   ⇒ ★ planByTier 变成一个**会做 I/O** 的函数（settings 读 settings_kv
	//   ⇒ 一次 SELECT）⇒ ★ 它既不再是纯函数，★ 又在一次请求里被读 1~2 次
	//   （round1 / round2 各一次）⇒ ★ 每请求多 1~2 次无缓存 DB 往返。
	//   ⇒ ★ 修法与 SuccessRatePoolStats 同一形状：★★ 在**请求边界**
	//   （planCandidates）读一次，再随上下文下发 ⇒ ★ planByTier 回到纯函数，
	//   ★ 每请求只读一次。
	//   ★ 0 = 未提供（调用方没接线）⇒ planByTier 回退到自带的热加载读数，
	//   ★ 保证既有测试调用面（8 处直接构造 StrategyInput）行为不变。
	MaxCandidates int
}

// SuccessRatePoolStats 是 P1-A 离群判定的池级统计（Envoy success-rate
// ejection 的池深护栏版）。Mean/MAD 覆盖「有 RecentSuccessRate 且 samples
// ≥ MinSamples」的候选；Depth 是该合格样本数（不是全池大小——两个有数据的
// 候选不构成有统计意义的池）。
type SuccessRatePoolStats struct {
	Mean  float64
	MAD   float64 // 平均绝对偏差（mean absolute deviation）
	Depth int     // 合格样本数（samples ≥ MinSamples 的候选数）
}

// 离群判据常量（audit 2026-10-28 §4 P1-A）：均值 − 3×MAD 以下、样本 ≥30、
// 池深 ≥3。池深护栏是本仓版对 Envoy ejection 的关键修正——浅池里「离群」
// 只是噪声（两个节点一好一坏，坏的天然离群 1.5×MAD+）。
const (
	outlierMinSamples = 30
	outlierMinDepth   = 3
	outlierMADFactor  = 3.0
	// outlierPenaltyStep 是 P1-A 第一步的固定强惩罚（审计档「loadScore+0.5」
	// 的实现形态：composite 的量纲是无权重小和，0.5 是质量项默认权重 0.2 的
	// 2.5 倍，足以把慢性病节点压出首跳，但不参与硬弹出）。
	outlierPenaltyStep = 0.5
)

// ComputeSuccessRatePoolStats 从候选池算成功率池统计。无合格样本时返回 nil
//（调用方置 nil ⇒ 维度关闭）。
func ComputeSuccessRatePoolStats(cands []provider.Candidate) *SuccessRatePoolStats {
	rates := make([]float64, 0, len(cands))
	for i := range cands {
		if cands[i].RecentSuccessRate != nil && cands[i].RecentSamples >= outlierMinSamples {
			rates = append(rates, *cands[i].RecentSuccessRate)
		}
	}
	if len(rates) < outlierMinDepth {
		return nil
	}
	var sum float64
	for _, v := range rates {
		sum += v
	}
	mean := sum / float64(len(rates))
	var devSum float64
	for _, v := range rates {
		devSum += math.Abs(v - mean)
	}
	return &SuccessRatePoolStats{Mean: mean, MAD: devSum / float64(len(rates)), Depth: len(rates)}
}

// successRateOutlierPenalty 判定单个候选是否为池内成功率离群（慢性病：
// 成功率低但无连败 ⇒ NodeState/breaker 的判死面永远抓不到它），离群 ⇒
// 固定强惩罚 outlierPenaltyStep。stats 为 nil / 池深不足 / 候选无合格样本 ⇒ 0。
func successRateOutlierPenalty(c provider.Candidate, stats *SuccessRatePoolStats) float64 {
	if stats == nil || stats.Depth < outlierMinDepth {
		return 0
	}
	if c.RecentSuccessRate == nil || c.RecentSamples < outlierMinSamples {
		return 0
	}
	if *c.RecentSuccessRate <= stats.Mean-outlierMADFactor*stats.MAD {
		return outlierPenaltyStep
	}
	return 0
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
	// Strategy.Score 路径不走 planCandidates，不走 sticky snapshot 优化（保守走 Info() 回退）。
	return calculateLoadScore(cand, s.r, ctx, in.LoadScoreWeights, in), nil
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
