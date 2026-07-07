package executors

import (
	"context"
	"log/slog"
	"math/rand"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ScoringWeights 定义路由评分的权重配置
type ScoringWeights struct {
	ConcurrencyWeight float64 // 全局并发压力权重
	IdentityWeight    float64 // 单 identity 压力权重
	LatencyWeight     float64 // 响应延迟权重
	QualityWeight     float64 // 成功率权重
}

// DefaultScoringWeights 返回默认权重配置
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		ConcurrencyWeight: 0.4,
		IdentityWeight:    0.1,
		LatencyWeight:     0.3,
		QualityWeight:     0.2,
	}
}

// calculateLoadScore 计算凭据的综合负载分数
// 分数越低越好（越可能被选中）
func calculateLoadScore(c provider.Candidate, r *Router, ctx context.Context, weights ScoringWeights) float64 {
	concurrencyScore := calculateConcurrencyScore(c, r, ctx)
	identityScore := calculateIdentityScore(c, r)
	latencyScore := calculateLatencyScore(c)
	qualityScore := calculateQualityScore(c)

	composite :=
		concurrencyScore*weights.ConcurrencyWeight +
			identityScore*weights.IdentityWeight +
			latencyScore*weights.LatencyWeight +
			qualityScore*weights.QualityWeight

	// DEBUG: 采样日志（10%）
	if rand.Float64() < 0.1 {
		slog.Info("LOAD_SCORE_V2",
			"credential_id", c.CredentialID,
			"concurrency_score", concurrencyScore,
			"identity_score", identityScore,
			"latency_score", latencyScore,
			"quality_score", qualityScore,
			"composite", composite,
		)
	}

	return composite
}

// calculateConcurrencyScore 计算全局并发压力分数
// 返回 0.0-1.0，值越大表示压力越大
func calculateConcurrencyScore(c provider.Candidate, r *Router, ctx context.Context) float64 {
	if r.FpSlots == nil || !r.FpSlots.Enabled() {
		return 0.5 // 默认中等压力
	}

	limit, used, _ := r.FpSlots.Stats(ctx, c.CredentialID, c.ConcurrencyLimit)
	if used == nil || limit == nil || *limit == 0 {
		return 0.5
	}

	pressure := float64(*used) / float64(*limit)
	if pressure > 1.0 {
		pressure = 1.0 // 饱和限制
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
func calculateLatencyScore(c provider.Candidate) float64 {
	latency := float64(c.P95LatencyMs)
	if latency < 100 {
		return 0.0 // 极快，无惩罚
	}

	// 饱和曲线: score = latency / (latency + k)
	// k=1000: 1000ms→0.5, 2000ms→0.67, 5000ms→0.83
	const k = 1000.0
	score := latency / (latency + k)

	if score > 1.0 {
		return 1.0
	}
	return score
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
