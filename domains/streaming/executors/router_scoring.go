package executors

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// LoadScoreWeights 定义路由评分的权重配置（Phase 1）
type LoadScoreWeights struct {
	ConcurrencyWeight float64 // 全局并发压力权重
	IdentityWeight    float64 // 单 identity 压力权重
	LatencyWeight     float64 // 响应延迟权重
	QualityWeight     float64 // 成功率权重
}

// DefaultLoadScoreWeights 返回默认权重配置
func DefaultLoadScoreWeights() LoadScoreWeights {
	return LoadScoreWeights{
		ConcurrencyWeight: 0.4,
		IdentityWeight:    0.1,
		LatencyWeight:     0.3,
		QualityWeight:     0.2,
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
	latencyScore := calculateLatencyScore(c)
	qualityScore := calculateQualityScore(c)

	headroom := calculateHeadroom(c)
	headroomWeight := envFloat("LLM_GATEWAY_ROUTING_W_HEADROOM", 0.05)
	latencyPenalty := 1.0 - latencyScore
	headroomPenalty := 1.0 - headroom
	composite :=
		concurrencyScore*weights.ConcurrencyWeight +
			identityScore*weights.IdentityWeight +
			latencyPenalty*weights.LatencyWeight +
			qualityScore*weights.QualityWeight +
			headroomPenalty*headroomWeight // 高 headroom → 低惩罚 → 更易被选中

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
			"composite", composite,
		)
	}

	return composite
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
func calculateLatencyScore(c provider.Candidate) float64 {
	pressure := candidatePressure(c) // 复用 headroom 计算
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
	case observed < 800: return 1.00
	case observed < 1500: return lerp(observed, 800, 1500, 1.00, 0.85)
	case observed < 3000: return lerp(observed, 1500, 3000, 0.85, 0.65)
	case observed < 10000: return lerp(observed, 3000, 10000, 0.65, 0.30)
	case observed < 30000: return lerp(observed, 10000, 30000, 0.30, 0.05)
	default: return 0.0 // hard block (> block threshold 默认 30s)
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
func candidatePressure(c provider.Candidate) float64 {
	if c.ConcurrencyLimit == nil || *c.ConcurrencyLimit <= 0 {
		return 0.5 // 无限流: 假设中等
	}
	// 从全局 limiter 取实时 (Limiter 在 Router 上下文, 这里简化)
	// P1: 估计 pressure, 实际在 planByTier 时取
	return 0.5
}

// calculateHeadroom bonus (P2-#5 §3.1): 奖励并发富裕的 candidate
func calculateHeadroom(c provider.Candidate) float64 {
	gamma := envFloat("LLM_GATEWAY_HEADROOM_GAMMA", 1.0)
	pressure := candidatePressure(c)
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
