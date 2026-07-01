// Package credential - Thompson Sampling Bandit for intelligent credential selection
package credential

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// BanditScorer 实现 Thompson Sampling 的凭据评分器
// 参考 freellmapi 的 services/scoring.ts
type BanditScorer struct {
	mu     sync.RWMutex
	rng    *rand.Rand
	scores map[string]*BanditScore // credentialID -> score
}

// BanditScore 单个凭据的 bandit 评分数据
type BanditScore struct {
	// Beta 分布参数（Thompson Sampling 核心）
	Alpha float64 // 成功次数 + 1 (先验)
	Beta  float64 // 失败次数 + 1 (先验)

	// 性能指标
	TotalRequests   int64
	SuccessRequests int64
	FailureRequests int64

	// 速度指标
	TotalLatencyMs int64 // 累计延迟
	TTFPMs         int64 // Time to first packet (streaming)

	// 智能指标 (可选，从 benchmark 数据导入)
	IntelligenceRank int // 1-100, 越小越聪明

	// 429 惩罚
	RateLimitHits    int       // 429 次数
	LastRateLimitHit time.Time // 最后一次 429 时间
	RateLimitPenalty float64   // 当前惩罚值 (0-10)

	// 配额保护
	QuotaRemaining  *int64 // 剩余配额 (如果已知)
	QuotaTotal      *int64 // 总配额
	LastQuotaUpdate time.Time

	LastScored time.Time
	LastSample float64 // 最后一次采样值 (debug 用)
}

// NewBanditScorer 创建新的 Bandit 评分器
func NewBanditScorer() *BanditScorer {
	return &BanditScorer{
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
		scores: make(map[string]*BanditScore),
	}
}

// GetScore 获取凭据的评分数据，如不存在则创建默认值
func (b *BanditScorer) GetScore(credID string) *BanditScore {
	b.mu.RLock()
	score, exists := b.scores[credID]
	b.mu.RUnlock()

	if exists {
		return score
	}

	// 创建新评分（Uniform 先验: Alpha=1, Beta=1）
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock
	if score, exists := b.scores[credID]; exists {
		return score
	}

	score = &BanditScore{
		Alpha:            1.0,
		Beta:             1.0,
		IntelligenceRank: 50, // 默认中等智能
		RateLimitPenalty: 0,
	}
	b.scores[credID] = score
	return score
}

// RecordSuccess 记录成功请求
func (b *BanditScorer) RecordSuccess(credID string, latencyMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	score := b.getOrCreateScoreLocked(credID)
	score.Alpha += 1.0
	score.TotalRequests++
	score.SuccessRequests++
	score.TotalLatencyMs += latencyMs
	score.LastScored = time.Now()
}

// RecordFailure 记录失败请求
func (b *BanditScorer) RecordFailure(credID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	score := b.getOrCreateScoreLocked(credID)
	score.Beta += 1.0
	score.TotalRequests++
	score.FailureRequests++
	score.LastScored = time.Now()
}

// RecordRateLimitHit 记录 429 错误
func (b *BanditScorer) RecordRateLimitHit(credID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	score := b.getOrCreateScoreLocked(credID)
	now := time.Now()

	// 惩罚衰减：每 2 分钟衰减 1 点
	decayInterval := 2 * time.Minute
	if !score.LastRateLimitHit.IsZero() {
		elapsed := now.Sub(score.LastRateLimitHit)
		decaySteps := float64(elapsed) / float64(decayInterval)
		score.RateLimitPenalty = math.Max(0, score.RateLimitPenalty-decaySteps)
	}

	// 增加惩罚（每次 +3，上限 10）
	const penaltyPerHit = 3.0
	const maxPenalty = 10.0
	score.RateLimitPenalty = math.Min(score.RateLimitPenalty+penaltyPerHit, maxPenalty)
	score.RateLimitHits++
	score.LastRateLimitHit = now
}

// UpdateQuota 更新配额信息（从 rate-limit headers 解析）
func (b *BanditScorer) UpdateQuota(credID string, remaining, total int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	score := b.getOrCreateScoreLocked(credID)
	score.QuotaRemaining = &remaining
	score.QuotaTotal = &total
	score.LastQuotaUpdate = time.Now()
}

// Sample 使用 Thompson Sampling 采样凭据得分
// 返回 0-1 之间的综合得分，越高越好
func (b *BanditScorer) Sample(credID string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	score := b.getOrCreateScoreLocked(credID)

	// 1. Thompson Sampling: 从 Beta 分布采样可靠性
	reliability := b.sampleBeta(score.Alpha, score.Beta)

	// 2. 速度得分: 基于平均延迟的饱和曲线
	speed := b.speedScore(score)

	// 3. 智能得分: 归一化 rank (1-100 -> 1.0-0.0)
	intelligence := b.intelligenceScore(score)

	// 4. 配额保护因子: headroom factor
	headroom := b.headroomFactor(score)

	// 5. 429 惩罚因子
	rateLimitFactor := b.rateLimitFactor(score)

	// 综合得分（参考 freellmapi 的 combineScore）
	// 默认权重: reliability=0.4, speed=0.3, intelligence=0.3
	const (
		wReliability  = 0.4
		wSpeed        = 0.3
		wIntelligence = 0.3
	)

	combined := reliability*wReliability + speed*wSpeed + intelligence*wIntelligence
	combined = combined * headroom * rateLimitFactor

	score.LastSample = combined
	score.LastScored = time.Now()

	return combined
}

// sampleBeta 从 Beta(α, β) 分布采样
func (b *BanditScorer) sampleBeta(alpha, beta float64) float64 {
	// 使用 Gamma 分布实现 Beta 分布采样
	// Beta(α, β) = Gamma(α, 1) / (Gamma(α, 1) + Gamma(β, 1))
	x := b.sampleGamma(alpha, 1.0)
	y := b.sampleGamma(beta, 1.0)
	if x+y == 0 {
		return 0.5 // 退化情况
	}
	return x / (x + y)
}

// sampleGamma 从 Gamma(shape, scale) 分布采样
// 使用 Marsaglia and Tsang's method (shape >= 1)
func (b *BanditScorer) sampleGamma(shape, scale float64) float64 {
	if shape < 1.0 {
		// shape < 1: 使用 rejection method
		return b.sampleGamma(shape+1.0, scale) * math.Pow(b.rng.Float64(), 1.0/shape)
	}

	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)

	for {
		var x, v float64
		for {
			x = b.rng.NormFloat64()
			v = 1.0 + c*x
			if v > 0 {
				break
			}
		}

		v = v * v * v
		u := b.rng.Float64()

		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v * scale
		}

		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v * scale
		}
	}
}

// speedScore 计算速度得分 (饱和曲线: 快速增长后趋于平缓)
// 参考 freellmapi 的 saturating throughput curve
func (b *BanditScorer) speedScore(score *BanditScore) float64 {
	if score.TotalRequests == 0 {
		return 0.5 // 默认中等
	}

	avgLatencyMs := float64(score.TotalLatencyMs) / float64(score.TotalRequests)

	// 饱和曲线: score = 1 - (latency / (latency + k))
	// k=500ms 时，500ms 得分 0.5，1000ms 得分 0.33
	const k = 500.0
	speedScore := 1.0 - (avgLatencyMs / (avgLatencyMs + k))

	// 限制在 [0, 1]
	if speedScore < 0 {
		return 0
	}
	if speedScore > 1 {
		return 1
	}
	return speedScore
}

// intelligenceScore 计算智能得分 (归一化 rank)
func (b *BanditScorer) intelligenceScore(score *BanditScore) float64 {
	// rank 1 (最聪明) -> 1.0
	// rank 100 (最笨) -> 0.0
	if score.IntelligenceRank <= 0 {
		return 0.5 // 未知
	}
	return 1.0 - float64(score.IntelligenceRank-1)/99.0
}

// headroomFactor 配额保护因子
// 当配额即将耗尽时，降低选择概率
func (b *BanditScorer) headroomFactor(score *BanditScore) float64 {
	if score.QuotaRemaining == nil || score.QuotaTotal == nil || *score.QuotaTotal == 0 {
		return 1.0 // 配额未知，不降权
	}

	remaining := float64(*score.QuotaRemaining)
	total := float64(*score.QuotaTotal)
	usage := 1.0 - (remaining / total)

	// 当使用率超过 80% 时开始降权
	if usage < 0.8 {
		return 1.0
	}

	// 线性降权: 80% -> 1.0, 100% -> 0.1
	return 1.0 - (usage-0.8)*0.9/0.2
}

// rateLimitFactor 429 惩罚因子
func (b *BanditScorer) rateLimitFactor(score *BanditScore) float64 {
	// penalty 0 -> 1.0
	// penalty 10 -> 0.1
	const maxPenalty = 10.0
	return 1.0 - (score.RateLimitPenalty / maxPenalty * 0.9)
}

// getOrCreateScoreLocked 获取或创建评分（调用方必须持有锁）
func (b *BanditScorer) getOrCreateScoreLocked(credID string) *BanditScore {
	score, exists := b.scores[credID]
	if !exists {
		score = &BanditScore{
			Alpha:            1.0,
			Beta:             1.0,
			IntelligenceRank: 50,
			RateLimitPenalty: 0,
		}
		b.scores[credID] = score
	}
	return score
}

// Reset 重置凭据的评分（用于测试或手动重置）
func (b *BanditScorer) Reset(credID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.scores, credID)
}

// ResetAll 重置所有评分
func (b *BanditScorer) ResetAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scores = make(map[string]*BanditScore)
}

// GetAllScores 获取所有凭据的评分数据（用于监控/调试）
func (b *BanditScorer) GetAllScores() map[string]*BanditScore {
	b.mu.RLock()
	defer b.mu.RUnlock()

	result := make(map[string]*BanditScore, len(b.scores))
	for id, score := range b.scores {
		// 返回副本
		scoreCopy := *score
		result[id] = &scoreCopy
	}
	return result
}
