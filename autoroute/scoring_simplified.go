package autoroute

import "strings"

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
// 最终得分（2 维路径）：IntentMatch * 0.6 + Price * 0.4 + Correction
func ScoreSimplified(c Candidate, task TaskType, avgPriceByCanonical map[int]float64, correctionScore float64) ScoringBreakdown {
	intentMatch := c.TaskMatchScore * 100

	avgCost := avgPriceByCanonical[c.CanonicalID]
	if avgCost == 0 {
		avgCost = c.UnitPriceInPer1M + c.UnitPriceOutPer1M
	}
	priceScore := (1000 - avgCost) / 10.0
	priceScore = clamp01to100(priceScore)

	correction := clampCorrection(correctionScore)

	composite := intentMatch*0.6 + priceScore*0.4 + correction

	return ScoringBreakdown{
		MatchScore:     intentMatch,
		PriceScore:     priceScore,
		ChannelQuality: scoreChannelQuality(c),
		Reliability:    scoreReliability(c),
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

// ScoreWithChannelQuality 实现 4 维评分（CHANNEL_QUALITY_ROUTING）：
//
//	FinalScore = IntentMatch * 0.4
//	           + Price * 0.2
//	           + ChannelQuality * 0.3
//	           + Reliability * 0.1
//	           + Correction
//
// 权重设计的理由：
//   - ChannelQuality 0.3：质量分主导，体现"质量优先于价格"
//   - IntentMatch 0.4：仍是最大权重，路由必须先匹配任务类型
//   - Price 0.2：从 0.4 降到 0.2，价格不再是主要决策因子
//   - Reliability 0.1：作为安全网，反映凭据的实时健康
//
// 池分层（preferred/fallback）由 RecommendV2 在调用本函数后单独
// 处理：本函数只产出 composite，不施加 demotion 系数。
//
// 当 ProviderCategory 为空（冷启动 / SQL 尚未加载该字段）时，
// ChannelQuality 走默认 base=40；不会拉黑候选，但会让该候选落入
// fallback 池。
func ScoreWithChannelQuality(c Candidate, task TaskType, avgPriceByCanonical map[int]float64, correctionScore float64) ScoringBreakdown {
	intentMatch := c.TaskMatchScore * 100

	avgCost := avgPriceByCanonical[c.CanonicalID]
	if avgCost == 0 {
		avgCost = c.UnitPriceInPer1M + c.UnitPriceOutPer1M
	}
	priceScore := (1000 - avgCost) / 10.0
	priceScore = clamp01to100(priceScore)

	channelQuality := scoreChannelQuality(c)
	reliability := scoreReliability(c)
	correction := clampCorrection(correctionScore)

	composite := intentMatch*0.4 +
		priceScore*0.2 +
		channelQuality*0.3 +
		reliability*0.1 +
		correction

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

// clampCorrection 把校正分钳制在 [-10, +10] 区间。
func clampCorrection(v float64) float64 {
	if v < -10 {
		return -10
	}
	if v > 10 {
		return 10
	}
	return v
}

// ── 池分层（preferred/fallback） ──────────────────────────────────

// StratifyByChannelQuality 把候选按 ChannelQuality 拆分为 preferred
// 与 fallback 两个池。preferred 池的 ChannelQuality 严格 >= 阈值。
//
// 阈值通过 ChannelQualityPreferredThreshold（=50）常量定义。
//
// 调用方在排序后处理：
//   - 若 preferred 池足够（>= topN），只返回 preferred
//   - 否则用 fallback 池补足，并对 fallback 的 composite 施加 demotion
//
// 注意：此函数只做拆分，不修改 composite。要施加 demotion 请调用
// ApplyFallbackDemotion。
func StratifyByChannelQuality(scored []ScoredCandidate) (preferred, fallback []ScoredCandidate) {
	for _, sc := range scored {
		if sc.Breakdown.ChannelQuality >= ChannelQualityPreferredThreshold {
			preferred = append(preferred, sc)
			continue
		}
		fallback = append(fallback, sc)
	}
	return preferred, fallback
}

// FallbackDemotionFactor 是 fallback 池的 demotion 系数（严格 < 1.0）。
// 设计：preferred 池未饱和时给 0.5（fallback 难以胜出），
// saturated 时给 0.85（仍低于 preferred 但保留竞争力）。
//
// 详见 RecommendV2 中根据主渠道饱和度切换系数的逻辑。
const FallbackDemotionFactor = 0.5
const FallbackDemotionFactorSaturated = 0.85

// ApplyFallbackDemotion 给 fallback 候选的 composite 乘 demotion 系数。
// 注意：MatchScore / PriceScore / ChannelQuality / Reliability 维度
// 分数保持不变，只调整 composite。便于可观测：仍能看到 fallback 候选
// 自身的通道质量分，只是总分会下降。
func ApplyFallbackDemotion(scored []ScoredCandidate, factor float64) {
	for i := range scored {
		scored[i].Breakdown.Composite *= factor
	}
}

// IsPreferredChannelSaturated 判断主渠道（Preferred 池）是否饱和。
// 规则：Preferred 池中所有候选的 PressureRatio >= 0.95。
//
// 当 Preferred 池为空时（仅 fallback 候选），返回 true（视为饱和，
// 让 fallback 有机会胜出）。
func IsPreferredChannelSaturated(preferred []ScoredCandidate) bool {
	if len(preferred) == 0 {
		return true
	}
	for _, sc := range preferred {
		if sc.Candidate.PressureRatio < 0.95 {
			return false
		}
	}
	return true
}

// ── 其它（保持兼容） ────────────────────────────────────────────

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

// ComputeCorrectionScore 计算会话校正分
//
// 基于上次任务结果：
//   - 上次成功且快速 → +5
//   - 上次失败 → -10
//   - 任务类型变化 → 0
//   - 无历史记录 → 0
//
// 参数：
//   - lastTask: 上次任务类型
//   - lastModel: 上次选择的模型 (canonical_name)
//   - lastSuccess: 上次是否成功
//   - lastLatencyMs: 上次延迟（毫秒）
//   - currentTask: 当前任务类型
//   - currentModel: 当前候选模型 (canonical_name)
//
// 返回：-10 ~ +10 的校正分
func ComputeCorrectionScore(
	lastTask TaskType,
	lastModel string,
	lastSuccess bool,
	lastLatencyMs int,
	currentTask TaskType,
	currentModel string,
) float64 {
	// 只对同一模型应用校正
	if lastModel != currentModel {
		return 0
	}

	// 任务类型变化 → 校正归零
	if lastTask != currentTask {
		return 0
	}

	// 上次失败 → 降权
	if !lastSuccess {
		return -10
	}

	// 上次成功且快速（< 2 秒）→ 小幅加分
	if lastLatencyMs > 0 && lastLatencyMs < 2000 {
		return 5
	}

	// 上次成功但较慢 → 不加分
	return 0
}
