package executors

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/modeliqdata"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// LoadScoreWeights 定义路由评分的权重配置（Phase 1）
type LoadScoreWeights struct {
	ConcurrencyWeight float64 // 全局并发压力权重
	IdentityWeight    float64 // 单 identity 压力权重
	LatencyWeight     float64 // 响应延迟权重
	QualityWeight     float64 // 成功率权重
	// CostWeight / IQWeight 是 M2 RT-2 的可选扩展维度，默认 0（关闭）：
	// 关闭时 composite 与历史公式逐字节一致（OffIsByteIdentical 测试钉住）。
	//   - CostWeight：shadow cost-optimized 策略的混合单价维度进入生效评分
	//     （calculateCostPenalty，未知价格=最大惩罚，不是免费）。
	//   - IQWeight：模型标准 IQ（AA Intelligence Index 0-100）进入生效评分
	//     （calculateIQPenalty，未知 IQ=中性 0.5，fail-open）。
	CostWeight float64 // 可选：混合单价（in+out per 1M）惩罚权重
	IQWeight   float64 // 可选：标准 IQ 惩罚权重
}

// DefaultLoadScoreWeights 返回默认权重配置
//
// CostWeight/IQWeight（M2 RT-2）默认 0（关闭）：关闭时评分与历史公式完全一致。
// 通过 env 开启：LLM_GATEWAY_ROUTING_W_COST / LLM_GATEWAY_ROUTING_W_IQ
// （沿用本文件 W_HEADROOM/W_CAPACITY 的 env 惯例），非法值回落 0。
func DefaultLoadScoreWeights() LoadScoreWeights {
	return LoadScoreWeights{
		ConcurrencyWeight: 0.4,
		IdentityWeight:    0.1,
		LatencyWeight:     0.3,
		QualityWeight:     0.2,
		CostWeight:        envFloat("LLM_GATEWAY_ROUTING_W_COST", 0),
		IQWeight:          envFloat("LLM_GATEWAY_ROUTING_W_IQ", 0),
	}
}

// calculateLoadScore 计算凭据的综合负载分数
// 分数越低越好（越可能被选中）—— P2C 选择在 router.go 中取 min。
//
// 方向一致性：composite 的每一项都必须是「惩罚」。
//   - concurrency / identity / quality：天然是惩罚。
//   - latencyScore / headroom：calculateLatencyScore / calculateHeadroom 返回
//     「健康度/奖励」（越大越好，见 docs/design/2026-07-20-latency-aware-routing.md
//     §2.1 分段表：< 800ms → 1.0）。此处用 (1 - x) 转成惩罚后相加，
//     否则低延迟/高 headroom 的好凭据 composite 偏高被 P2C 惩罚，与设计意图
//     （偏好低延迟、高 headroom）相反。
//     2026-07-24 审计修正：60809965 重写后直接相加导致方向反转。
func calculateLoadScore(c provider.Candidate, r *Router, ctx context.Context, weights LoadScoreWeights) float64 {
	concurrencyScore := calculateConcurrencyScore(c, r, ctx)
	identityScore := calculateIdentityScore(c, r)
	latencyScore := calculateLatencyScore(c, r)
	qualityScore := calculateQualityScore(c)

	headroom := calculateHeadroom(c, r)
	headroomWeight := envFloat("LLM_GATEWAY_ROUTING_W_HEADROOM", 0.05)
	latencyPenalty := 1.0 - latencyScore
	headroomPenalty := 1.0 - headroom
	// Capacity nudge (2026-08-13): a deliberately tiny term so the credential's
	// concurrency capacity (Candidate.Weight) biases the P2C score in favor of
	// higher-capacity nodes even outside exact ties. It saturates at the default
	// weight (100) so normal pools are unaffected and only sub-default nodes get
	// a small penalty; the dominant load/health terms and the capacity-aware
	// tie-breaks (pickWeightedTie / banditOrder) carry the real weighting. Set
	// LLM_GATEWAY_ROUTING_W_CAPACITY=0 to disable entirely.
	capacityWeight := envFloat("LLM_GATEWAY_ROUTING_W_CAPACITY", 0.02)
	capacityPenalty := capacityPenaltyForWeight(c.Weight)
	composite :=
		concurrencyScore*weights.ConcurrencyWeight +
			identityScore*weights.IdentityWeight +
			latencyPenalty*weights.LatencyWeight +
			qualityScore*weights.QualityWeight +
			headroomPenalty*headroomWeight + // 高 headroom → 低惩罚 → 更易被选中
			capacityPenalty*capacityWeight

	// M2 RT-2 (2026-08-15): optional cost/IQ dimensions from the shadow
	// strategies enter the effective score. Guarded so the default (weights
	// zero / off) hot path stays byte-identical to the pre-RT-2 composite.
	var costPenalty, iqPenalty float64
	if weights.CostWeight > 0 {
		costPenalty = calculateCostPenalty(c)
		composite += costPenalty * weights.CostWeight
	}
	if weights.IQWeight > 0 {
		iqPenalty = calculateIQPenalty(c)
		composite += iqPenalty * weights.IQWeight
	}

	if rand.Float64() < 0.1 {
		slog.Info("LOAD_SCORE_V2",
			"credential_id", c.CredentialID,
			"concurrency_score", concurrencyScore,
			"identity_score", identityScore,
			"latency_score", latencyScore,
			"latency_penalty", latencyPenalty,
			"quality_score", qualityScore,
			"headroom", headroom,
			"headroom_penalty", headroomPenalty,
			"capacity_weight", c.Weight,
			"capacity_penalty", capacityPenalty,
			"cost_penalty", costPenalty,
			"iq_penalty", iqPenalty,
			"composite", composite,
		)
	}

	return composite
}

// calculateCostPenalty normalizes the shadow cost-optimized dimension — the
// blended unit price (PriceInPer1M + PriceOutPer1M, USD per 1M tokens) — into
// a 0..1 penalty for calculateLoadScore (M2 RT-2).
//
// Unknown-value semantics (README §5 R1, mirrors strategy_cost.go): an unknown
// price is NOT free → max penalty 1.0, so unpriced candidates never outrank
// cheap known ones when CostWeight is on. Known prices rise linearly with the
// blended price and saturate at the soft cap (env LLM_GATEWAY_ROUTING_COST_CAP,
// default $30/1M blended — roughly the premium-model frontier) so a single
// expensive outlier cannot dominate the composite.
func calculateCostPenalty(c provider.Candidate) float64 {
	if c.PriceInPer1M == nil || c.PriceOutPer1M == nil {
		return 1.0
	}
	cap := envFloat("LLM_GATEWAY_ROUTING_COST_CAP", 30)
	if cap <= 0 {
		cap = 30
	}
	blended := *c.PriceInPer1M + *c.PriceOutPer1M
	if blended <= 0 {
		return 0
	}
	p := blended / cap
	if p > 1.0 {
		p = 1.0
	}
	return p
}

// calculateIQPenalty turns the model's standard IQ (AA Intelligence Index,
// 0-100 — the same reference table as autoroute's RT-1 gate) into a 0..1
// penalty for calculateLoadScore (M2 RT-2): higher IQ → lower penalty.
//
// Unknown-value semantics: an unknown IQ is NEUTRAL 0.5 (fail-open, matching
// RT-1's gate semantics) — an unknown model is neither rewarded nor punished,
// so a stale reference table cannot empty/bias the pool. Resolution order:
// StandardizedName first, then RawModel.
func calculateIQPenalty(c provider.Candidate) float64 {
	name := c.StandardizedName
	if name == "" {
		name = c.RawModel
	}
	iq, found, _ := modeliqdata.LookupStandardIQ(name)
	if !found {
		return 0.5
	}
	if iq < 0 {
		iq = 0
	}
	if iq > 100 {
		iq = 100
	}
	return 1.0 - iq/100.0
}

// capacityPenaltyForWeight turns a candidate's capacity weight (the credential's
// concurrency capacity, see provider.applyCapacityWeightedLB) into a 0..1
// penalty for calculateLoadScore. The default/unknown weight (100) and any
// larger weight map to 0 (no penalty); sub-default weights rise linearly to 1.
// Unknown/zero weights are treated as the default so they are never penalized.
const defaultCapacityWeight = 100

func capacityPenaltyForWeight(weight int) float64 {
	if weight <= 0 {
		weight = defaultCapacityWeight
	}
	if weight >= defaultCapacityWeight {
		return 0
	}
	return 1.0 - float64(weight)/float64(defaultCapacityWeight)
}

// calculateConcurrencyScore 计算全局并发压力分数
// 返回 0.0-1.0，值越大表示压力越大
func calculateConcurrencyScore(c provider.Candidate, r *Router, ctx context.Context) float64 {
	if r.Limiter == nil {
		return 0.5
	}

	cred := r.Limiter.Credential(c.ProviderID, c.CredentialID)
	capacity := cred.Capacity()
	if capacity <= 0 {
		return 0.5
	}

	pressure := float64(cred.Used()) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}

	return pressure
}

// calculateIdentityScore 计算单 identity 压力分数
func calculateIdentityScore(c provider.Candidate, r *Router) float64 {
	if r.Limiter == nil {
		return 0.5
	}

	cred := r.Limiter.Credential(c.ProviderID, c.CredentialID)
	if cred == nil {
		return 0.5
	}

	inFlight := cred.Used()
	capacity := cred.Capacity()
	if capacity == 0 {
		return 0.5
	}

	pressure := float64(inFlight) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}

	return pressure
}

// calculateLatencyScore 计算延迟分数
// 使用饱和曲线：快速增长后趋于平缓
// 2026-07-20 P2-#5: concurrency-aware latency score (docs/design/2026-07-20-latency-aware-routing.md §2.1)
// idle slope × queue-amplification
func calculateLatencyScore(c provider.Candidate, r *Router) float64 {
	pressure := candidatePressure(c, r) // 复用 headroom 计算
	p95 := c.P95LatencyMs
	if p95 < 100 {
		return 0.0
	}
	// queue-amplification: pressure 超过 knee (0.6) 越多, p95 被放大
	// alpha=1.2, beta=1.8, knee=0.6 (env LLM_GATEWAY_PRESSURE_* 可改)
	alpha := envFloat("LLM_GATEWAY_PRESSURE_ALPHA", 1.2)
	beta := envFloat("LLM_GATEWAY_PRESSURE_BETA", 1.8)
	knee := envFloat("LLM_GATEWAY_PRESSURE_KNEE", 0.6)
	amp := 1.0
	if pressure > knee {
		delta := pressure - knee
		amp = 1.0 + alpha*mathPow(delta, beta)
	}
	observed := float64(p95) * amp
	// piecewise table (在 amplified observed 上)
	switch {
	case observed < 800:
		return 1.00
	case observed < 1500:
		return lerp(observed, 800, 1500, 1.00, 0.85)
	case observed < 3000:
		return lerp(observed, 1500, 3000, 0.85, 0.65)
	case observed < 10000:
		return lerp(observed, 3000, 10000, 0.65, 0.30)
	case observed < 30000:
		return lerp(observed, 10000, 30000, 0.30, 0.05)
	default:
		return 0.0 // hard block (> block threshold 默认 30s)
	}
}

// mathPow 包装 math.Pow, 处理 x<=0 边界
func mathPow(x, y float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Pow(x, y)
}

// lerp 线性插值
func lerp(x, x0, x1, y0, y1 float64) float64 {
	if x1 == x0 {
		return y0
	}
	return y0 + (y1-y0)*(x-x0)/(x1-x0)
}

// envFloat 从 env 读 float 配 fallback
func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

// candidatePressure 计算 candidate 当前的并发压力
// (在 0-1.5 范围: 1.0+ 表示超载)
//
// Realtime concurrency is read from Router.Limiter (the same source used by
// calculateConcurrencyScore/calculateIdentityScore). When no limiter is
// available, or the credential has no usable capacity, we fall back to the
// static ConcurrencyLimit and ultimately a neutral 0.5 — preserving the prior
// "unknown ⇒ medium" semantics instead of a hard constant.
func candidatePressure(c provider.Candidate, r *Router) float64 {
	// Prefer realtime in-flight pressure from the global limiter.
	if r != nil && r.Limiter != nil {
		cred := r.Limiter.Credential(c.ProviderID, c.CredentialID)
		capacity := cred.Capacity()
		if capacity > 0 {
			return float64(cred.Used()) / float64(capacity)
		}
	}
	// Fall back to static config: if a concurrency limit is known but we have
	// no realtime count, ActiveSessions (if reported) gives a coarse estimate.
	if c.ConcurrencyLimit != nil && *c.ConcurrencyLimit > 0 {
		if c.ActiveSessions > 0 {
			p := float64(c.ActiveSessions) / float64(*c.ConcurrencyLimit)
			if p > 1.0 {
				p = 1.0
			}
			return p
		}
		return 0.5 // 有限流但无实时数据: 假设中等
	}
	return 0.5 // 无限流: 假设中等
}

// calculateHeadroom bonus (P2-#5 §3.1): 奖励并发富裕的 candidate
func calculateHeadroom(c provider.Candidate, r *Router) float64 {
	gamma := envFloat("LLM_GATEWAY_HEADROOM_GAMMA", 1.0)
	pressure := candidatePressure(c, r)
	if pressure > 1.0 {
		return 0.0
	}
	return mathPow(1.0-pressure, gamma)
}

// calculateQualityScore 计算质量分数（基于成功率）
func calculateQualityScore(c provider.Candidate) float64 {
	quality := c.SuccessRate

	// 优先使用最近成功率
	if c.RecentSuccessRate != nil && c.RecentSamples >= 10 {
		quality = *c.RecentSuccessRate
	}

	// 质量低 → 分数高（惩罚）
	// 95% → 0.05, 80% → 0.20, 50% → 0.50
	return 1.0 - quality
}
