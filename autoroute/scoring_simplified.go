package autoroute

import (
	"sort"
	"strings"
)

// scoring_simplified.go — 简化评分与通道质量评分。
//
// ScoreSimplified 实现 2 维评分（兼容旧逻辑）：
//   FinalScore = IntentMatch * 0.6 + Price * 0.4 + Correction
//
// ScoreWithChannelQuality 实现 4 维评分（CHANNEL_QUALITY_ROUTING 新逻辑）：
//   FinalScore = IntentMatch * 0.4 + Price * 0.2 + ChannelQuality * 0.3
//              + Reliability * 0.1 + Correction
//
// 业务诉求（CHANNEL_QUALITY_ROUTING_DESIGN.md）：
//   "可靠的资源可用时（如 Minimax 原厂），优先使用；免费的、不可靠的
//    （如 NVIDIA NIM 免费凭据）在主渠道未用满之前原则上跳过，除非
//    该凭据历史上没有错误发生。同成本情况下，质量更好者优先。"
//
// 4 维评分通过新增 ChannelQuality（通道质量）和 Reliability（运行时
// 可靠度）两个维度实现"质量优先于价格"的诉求。ChannelQuality 综合
// providers.category 静态分 + 实时健康度调整；Reliability 直接由
// success_rate + p95_latency 推导。

// ── 共享工具函数 ────────────────────────────────────────────────

// clamp01to100 把分数钳制在 [0, 100] 区间。
func clamp01to100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// deriveIsFree 推导 IsFree 字段（与 SQL 中的 CASE WHEN 保持一致）：
//   - billing_mode 在 free 类（free / token_plan / code_plan / agent_plan / monthly）
//   - cost_tier 标记为 free
//   - 价格为 0（in + out 都为 0）
//
// 三者满足其一即视为免费。保守取 TRUE，避免漏判。
func deriveIsFree(c Candidate) bool {
	if c.IsFree {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(c.BillingMode)) {
	case "free", "token_plan", "code_plan", "agent_plan", "monthly":
		return true
	}
	if strings.EqualFold(strings.TrimSpace(c.CostTier), "free") {
		return true
	}
	if c.UnitPriceInPer1M == 0 && c.UnitPriceOutPer1M == 0 {
		return true
	}
	return false
}

// ── 通道质量分（ChannelQuality） ──────────────────────────────────

// scoreChannelQuality 根据 providers.category 静态分 + 实时健康度调整，
// 输出 0-100 的通道质量分。
//
// 静态 base（按 category）：
//   - official, official_proxy      → 90
//   - self_host                     → 80
//   - aggregator                    → 60
//   - third_party_relay             → 50
//   - 其它 / 未知                    → 40
//
// 运行时 delta 叠加：
//   - success_rate > 0.95 且 p95 < 2000ms  → +10
//   - success_rate < 0.80                  → -20
//   - p95_latency_ms > 5000ms              → -15
//   - success_rate < 0.60                  → -30（强 demotion）
//   - 免费且 success_rate < 0.90           → -25（免费+不可靠 → 大幅降权）
//
// 最终分钳制在 [0, 100]。
//
// 设计意图：把官方/原厂渠道放在 60+ 档位，把"免费但不稳定"的第三方
// 凭据（典型：NVIDIA NIM 免费）压到 30 以下，从而被 RecommendV2 的
// 池分层逻辑降到 fallback 池。
func scoreChannelQuality(c Candidate) float64 {
	base := baseByCategory(c.ProviderCategory)

	delta := 0.0
	if c.SuccessRate > 0.95 && c.P95LatencyMs > 0 && c.P95LatencyMs < 2000 {
		delta += 10
	}
	if c.SuccessRate > 0 && c.SuccessRate < 0.80 {
		delta -= 20
	}
	if c.SuccessRate > 0 && c.SuccessRate < 0.60 {
		delta -= 30
	}
	if c.P95LatencyMs > 5000 {
		delta -= 15
	}
	if deriveIsFree(c) && c.SuccessRate > 0 && c.SuccessRate < 0.90 {
		delta -= 25
	}

	return clamp01to100(base + delta)
}

// baseByCategory 返回 category 静态分。
// 未知 category 给 40（中位偏下，避免冷启动误杀）。
func baseByCategory(category string) float64 {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "official", "official_proxy":
		return 90
	case "self_host":
		return 80
	case "aggregator":
		return 60
	case "third_party_relay":
		return 50
	default:
		return 40
	}
}

// ChannelQualityPreferredThreshold 是池分层的硬阈值：>= 阈值的候选
// 进入 Preferred 池，< 阈值的进入 Fallback 池。
//
// 选取 50 的理由：static base 最低的 'third_party_relay' 是 50，
// 加运行时 delta 容易跌破 50；而 'aggregator' 60 在历史表现差时也
// 会跌破。设 50 作为"是否值得优先"的判断线。
const ChannelQualityPreferredThreshold = 50.0

// ── 可靠度分（Reliability） ──────────────────────────────────────

// scoreReliability 输出 0-100 的运行时可靠度分。
//
//	score = success_rate*80 + latencyFactor
//
//	latencyFactor:
//	  p95 <= 1000ms → 20
//	  p95 <= 3000ms → 12
//	  p95 <= 5000ms → 6
//	  p95 >  5000ms → 0
//	  p95 == 0      → 10（中性，未知延迟）
//
//	success_rate = 0（冷启动）→ 50（中性，避免误杀）。
func scoreReliability(c Candidate) float64 {
	sr := c.SuccessRate
	if sr <= 0 {
		// 冷启动：给中性分 50（不影响排序）
		return 50
	}
	if sr > 1 {
		sr = 1
	}
	latency := 0.0
	switch {
	case c.P95LatencyMs == 0:
		latency = 10 // 未知延迟，中性
	case c.P95LatencyMs <= 1000:
		latency = 20
	case c.P95LatencyMs <= 3000:
		latency = 12
	case c.P95LatencyMs <= 5000:
		latency = 6
	default:
		latency = 0
	}
	return clamp01to100(sr*80 + latency)
}

// ── 评分函数 ────────────────────────────────────────────────────

// ScoreSimplified 实现 2 维评分（兼容旧逻辑）：
//
//	FinalScore = IntentMatch * 0.6 + Price * 0.4 + Correction
//
// 同时填充 ChannelQuality 与 Reliability 字段（保持 ScoringBreakdown
// 结构稳定，下游 X-Gw-Auto-Decision 头可直接观测通道质量分）。
//
// 评分维度：
//  1. IntentMatchScore (0-100): 任务类型与模型 Tags 的匹配度
//  2. PriceScore (0-100): 基于该 canonical 所有有效凭据的平均成本
//  3. CorrectionScore (-10 ~ +10): 基于上次任务结果的小幅校正
//  4. ChannelQuality (0-100): 通道静态分类 + 实时健康
//  5. Reliability (0-100): success_rate + p95_latency
//
// PriceScore（与 scorePrice 在 scoring.go 中的实现保持一致）：
//
//	blended = unit_price_in + unit_price_out  (1:1 input/output)
//	if blended == 0                  → 100 (free)
//	if PriceP75 == 0                 → 80   (no cohort baseline → 中性)
//	ratio = blended / PriceP75
//	score = clamp(100 * (1.5 - ratio), 0, 100)
//
// 0.5 × P75 → 100（最便宜）；1.0 × P75 → 50（基线）；1.5 × P75 → 0（最贵）。
// doc 16 §5-D 修复：用 cohort P75 归一化替代旧的 `(1000-avgCost)/10`，后者
// 把价格分数几乎钳制在 94–100 区间（avgCost ≈ 0–60/1M），丧失区分力。
//
// 最终得分（2 维路径）：IntentMatch * 0.6 + Price * 0.4 + Correction
func ScoreSimplified(c Candidate, task TaskType, costCtx CostContext, correctionScore float64) ScoringBreakdown {
	intentMatch := c.TaskMatchScore * 100

	priceScore := scorePriceByCostContext(c, costCtx)
	channelQuality := scoreChannelQuality(c)
	reliability := scoreReliability(c)
	correction := clampCorrection(correctionScore)

	composite := intentMatch*0.6 + priceScore*0.4 + correction

	return ScoringBreakdown{
		MatchScore:     intentMatch,
		PriceScore:     priceScore,
		ChannelQuality: channelQuality,
		Reliability:    reliability,
		Composite:      composite,
		// 其余维度保持为 0，保持结构兼容
		SpeedScore:     0,
		StabilityScore: 0,
		PressureScore:  0,
		ContextFit:     0,
		VersionRecency: 0,
		StrengthMatch:  0,
	}
}

func clampCorrection(v float64) float64 {
	if v < -10 {
		return -10
	}
	if v > 10 {
		return 10
	}
	return v
}

// scorePriceByCostContext 把候选价格归一化到 cohort P75，输出 0-100 的
// PriceScore。这是 doc 16 §5-D 的统一实现（替换旧的 `(1000-avgCost)/10`）：
//
//	blended = unit_price_in + unit_price_out  (1:1 input/output)
//	if blended <= 0                  → 100 (free 或未知 → 最优)
//	if costCtx.PriceP75 <= 0         → 80  (无 cohort 基线 → 中性)
//	ratio = blended / PriceP75
//	score = clamp(100 * (1.5 - ratio), 0, 100)
//
//	ratio 0.5 (half of P75) → 100 (最便宜)
//	ratio 1.0 (at P75)     → 50  (基线)
//	ratio 1.5 (50% more)   → 0   (最贵)
//
// 旧公式对 per-1M 单价（典型 0.1–60）的输出被钳制在 94–100，丧失区分力；
// P75 归一化恢复了跨 cohort 的可比性，与 scoring.go::scorePrice 完全一致。
//
// §5-F 混币种：当前 CostContext 假设 cohort 单币种，HasMixedPrices 字段
// 仍为占位。CNY/USD 同 cohort 评分的修复需要先在 Candidate 上加
// BillingCurrency 字段并引入 FX 注入（与 §5-F 一起处理）。
func scorePriceByCostContext(c Candidate, costCtx CostContext) float64 {
	blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
	if blended <= 0 {
		return 100
	}
	if costCtx.PriceP75 <= 0 {
		return 80
	}
	ratio := blended / costCtx.PriceP75
	score := 100 * (1.5 - ratio)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// recommendCostContext 从候选池构建 CostContext（cohort P75 归一化的基线）。
//
// doc 16 §5-D 修复：旧实现用 per-canonical `avgPriceByCanonical[canonicalID]`
// （数值小、范围窄 → PriceScore 几乎常数 94–100）。现在改用整个候选池
// 的 P75 单一基线，确保 cohort 内「最便宜」/「最贵」两端能拉开。
//
// 排除零价候选（free/未知价格不应作为 P75 基线，否则会拉低）。
func recommendCostContext(cands []Candidate) CostContext {
	prices := make([]float64, 0, len(cands))
	for _, c := range cands {
		blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if blended > 0 {
			prices = append(prices, blended)
		}
	}
	ctx := CostContext{}
	if len(prices) > 0 {
		sort.Float64s(prices)
		ctx.PriceP75 = percentileFloat(prices, 0.75)
	}
	return ctx
}

// ComputeAvgPriceByCanonical 计算每个 canonical model 的平均价格
// 只统计可用的 credentials（UnavailableReason == ""）
//
// 返回 map[canonical_id]avg_price
func ComputeAvgPriceByCanonical(candidates []Candidate) map[int]float64 {
	type priceAcc struct {
		sum   float64
		count int
	}

	acc := make(map[int]*priceAcc)

	for _, c := range candidates {
		if c.UnavailableReason != "" {
			continue // 跳过不可用的
		}

		price := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if _, ok := acc[c.CanonicalID]; !ok {
			acc[c.CanonicalID] = &priceAcc{}
		}
		acc[c.CanonicalID].sum += price
		acc[c.CanonicalID].count++
	}

	result := make(map[int]float64)
	for canonID, a := range acc {
		if a.count > 0 {
			result[canonID] = a.sum / float64(a.count)
		}
	}

	return result
}
