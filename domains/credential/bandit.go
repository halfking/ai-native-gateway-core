// Package credential - Thompson Sampling Bandit for intelligent credential selection
package credential

import (
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// BanditScorer 实现 Thompson Sampling 的凭据评分器
// 参考 freellmapi 的 services/scoring.ts
//
// 并发模型:
//   - scores map 由 b.mu 保护。
//   - 指向 *BanditScore 的指针永远不会泄漏到锁之外;读取者通过
//     GetScore (返回值副本) 或 SnapshotScore 获取快照,因此请求处理
//     goroutine 上的字段写入不会与后台 flusher/worker 的读取竞争。
//   - 采样使用 math/rand/v2 的顶层函数,该函数并发安全且无锁竞争,
//     因此 Sample 不再需要写锁。
type BanditScorer struct {
	mu     sync.RWMutex
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
		scores: make(map[string]*BanditScore),
	}
}

// GetScore 返回凭据评分的快照副本 (值类型,非指针)。
//
// 返回值副本是有意为之的:BanditScore 字段会被请求处理 goroutine
// (RecordSuccess/RecordFailure/RecordRateLimitHit) 在写锁下原地修改。
// 若返回 *BanditScore 指针,调用方在锁外读取字段会与上述写入发生数据竞争。
// 调用方应直接读取返回的副本,不应保留对内部状态的引用。
func (b *BanditScorer) GetScore(credID string) BanditScore {
	b.mu.RLock()
	if score, exists := b.scores[credID]; exists {
		snap := b.snapshotScore(score) // copy while holding RLock
		b.mu.RUnlock()
		return snap
	}
	b.mu.RUnlock()

	// 创建新评分（Uniform 先验: Alpha=1, Beta=1）
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock
	if score, exists := b.scores[credID]; exists {
		return b.snapshotScore(score)
	}

	score := &BanditScore{
		Alpha:            1.0,
		Beta:             1.0,
		IntelligenceRank: 50, // 默认中等智能
		RateLimitPenalty: 0,
	}
	b.scores[credID] = score
	return b.snapshotScore(score)
}

// snapshotScore 返回 *BanditScore 的值副本。调用方应持有至少 RLock,
// 这样副本反映某个一致时刻的状态,不会与并发的 RecordX 写入竞争。
func (b *BanditScorer) snapshotScore(score *BanditScore) BanditScore {
	return *score
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

// SetIntelligenceRank 更新凭据的智能排名 (1-100,越小越聪明)。
// 提供此 setter 是为了通过线程安全的方式修改单个字段,
// 而不是通过返回的快照副本回写 (那是数据竞争陷阱)。
func (b *BanditScorer) SetIntelligenceRank(credID string, rank int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getOrCreateScoreLocked(credID).IntelligenceRank = rank
}

// Sample 使用 Thompson Sampling 采样凭据得分
// 返回 0-1 之间的综合得分，越高越好
//
// 并发说明:此函数是路由热路径。它在整个读取+计算期间持有 RLock,
// 因此与 RecordX (持写锁) 互斥,但允许多个 Sample 并发执行。
// 采样使用 math/rand/v2 的顶层函数 (并发安全),不需要实例 RNG 锁。
// 不再原地写 LastSample/LastScored —— 它们是 debug 遥测且无功能读取者,
// LastScored 已由 RecordSuccess/RecordFailure 在写锁下维护。
func (b *BanditScorer) Sample(credID string) float64 {
	b.mu.RLock()
	score, ok := b.scores[credID]
	if !ok {
		// 稀有路径:首次见到该凭据。释放 RLock,用写锁初始化,再重试只读采样。
		b.mu.RUnlock()
		b.mu.Lock()
		score = b.getOrCreateScoreLocked(credID)
		b.mu.Unlock()
		b.mu.RLock()
		// 重新读取:虽然 score 指针稳定 (getOrCreateScoreLocked 复用现有),
		// 但严格起见重新查表,确保与并发 Reset 的交互一致。
		score, _ = b.scores[credID]
		// 极端情况:在 Unlock 与 RLock 之间发生并发 Reset 删除了该 key。
		// 此时返回中性分 (Uniform 先验的期望 0.5),避免 nil 解引用。
		if score == nil {
			b.mu.RUnlock()
			return 0.5
		}
	}
	defer b.mu.RUnlock()

	// 1. Thompson Sampling: 从 Beta 分布采样可靠性
	reliability := sampleBeta(score.Alpha, score.Beta)

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

	return combined
}

// sampleBeta 从 Beta(α, β) 分布采样
func sampleBeta(alpha, beta float64) float64 {
	// 使用 Gamma 分布实现 Beta 分布采样
	// Beta(α, β) = Gamma(α, 1) / (Gamma(α, 1) + Gamma(β, 1))
	x := sampleGamma(alpha, 1.0)
	y := sampleGamma(beta, 1.0)
	if x+y == 0 {
		return 0.5 // 退化情况
	}
	return x / (x + y)
}

// sampleGamma 从 Gamma(shape, scale) 分布采样
// 使用 Marsaglia and Tsang's method (shape >= 1)
//
// 使用 math/rand/v2 的顶层函数 (并发安全),无需实例状态或锁。
func sampleGamma(shape, scale float64) float64 {
	if shape < 1.0 {
		// shape < 1: 使用 rejection method
		return sampleGamma(shape+1.0, scale) * math.Pow(rand.Float64(), 1.0/shape)
	}

	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)

	for {
		var x, v float64
		for {
			x = rand.NormFloat64()
			v = 1.0 + c*x
			if v > 0 {
				break
			}
		}

		v = v * v * v
		u := rand.Float64()

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
		// 返回副本,并从原子字段物化 debug 值。
		snap := b.snapshotScore(score)
		result[id] = &snap
	}
	return result
}
