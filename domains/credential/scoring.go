// Package credential - Scoring strategies and presets for credential selection
package credential

import (
	"math"
	"time"
)

// RoutingStrategy 路由策略名称
type RoutingStrategy string

const (
	StrategyBalanced RoutingStrategy = "balanced" // 均衡：可靠性、速度、智能各占 1/3
	StrategySmartest RoutingStrategy = "smartest" // 最聪明：优先智能，其次可靠性
	StrategyFastest  RoutingStrategy = "fastest"  // 最快：优先速度，其次可靠性
	StrategyReliable RoutingStrategy = "reliable" // 最可靠：优先可靠性，其次速度
	StrategyCustom   RoutingStrategy = "custom"   // 自定义权重
)

// RoutingWeights 路由权重配置
type RoutingWeights struct {
	Reliability  float64 // 可靠性权重 (0-1)
	Speed        float64 // 速度权重 (0-1)
	Intelligence float64 // 智能权重 (0-1)
}

// Normalize 归一化权重，使其总和为 1
func (w *RoutingWeights) Normalize() {
	total := w.Reliability + w.Speed + w.Intelligence
	if total == 0 {
		// 避免除以 0，使用均衡策略
		w.Reliability = 1.0 / 3.0
		w.Speed = 1.0 / 3.0
		w.Intelligence = 1.0 / 3.0
		return
	}
	w.Reliability /= total
	w.Speed /= total
	w.Intelligence /= total
}

// GetPresetWeights 获取预设策略的权重
func GetPresetWeights(strategy RoutingStrategy) RoutingWeights {
	switch strategy {
	case StrategyBalanced:
		return RoutingWeights{
			Reliability:  0.4,
			Speed:        0.3,
			Intelligence: 0.3,
		}
	case StrategySmartest:
		return RoutingWeights{
			Reliability:  0.2,
			Speed:        0.1,
			Intelligence: 0.7,
		}
	case StrategyFastest:
		return RoutingWeights{
			Reliability:  0.3,
			Speed:        0.6,
			Intelligence: 0.1,
		}
	case StrategyReliable:
		return RoutingWeights{
			Reliability:  0.7,
			Speed:        0.2,
			Intelligence: 0.1,
		}
	default:
		// 默认均衡策略
		return RoutingWeights{
			Reliability:  0.4,
			Speed:        0.3,
			Intelligence: 0.3,
		}
	}
}

// ScorerWithWeights 带权重的评分器
type ScorerWithWeights struct {
	Bandit  *BanditScorer
	Weights RoutingWeights
}

// NewScorerWithWeights 创建带权重的评分器
func NewScorerWithWeights(strategy RoutingStrategy) *ScorerWithWeights {
	return &ScorerWithWeights{
		Bandit:  NewBanditScorer(),
		Weights: GetPresetWeights(strategy),
	}
}

// SampleWithWeights 使用自定义权重采样
func (s *ScorerWithWeights) SampleWithWeights(credID string) float64 {
	s.Bandit.mu.Lock()
	defer s.Bandit.mu.Unlock()

	score := s.Bandit.getOrCreateScoreLocked(credID)

	// 1. 可靠性：Thompson Sampling
	reliability := s.Bandit.sampleBeta(score.Alpha, score.Beta)

	// 2. 速度
	speed := s.Bandit.speedScore(score)

	// 3. 智能
	intelligence := s.Bandit.intelligenceScore(score)

	// 4. 保护因子
	headroom := s.Bandit.headroomFactor(score)
	rateLimitFactor := s.Bandit.rateLimitFactor(score)

	// 综合得分
	combined := reliability*s.Weights.Reliability +
		speed*s.Weights.Speed +
		intelligence*s.Weights.Intelligence

	combined = combined * headroom * rateLimitFactor

	score.LastSample = combined
	score.LastScored = time.Now()

	return combined
}

// ExpectedReliability 计算期望可靠性（不采样，直接用期望值）
func ExpectedReliability(alpha, beta float64) float64 {
	// Beta 分布的期望值 = α / (α + β)
	return alpha / (alpha + beta)
}

// ReliabilityPosterior 返回可靠性的后验分布参数
func ReliabilityPosterior(successes, failures int64) (alpha, beta float64) {
	// Uniform 先验 Beta(1, 1) + 观测数据
	return float64(successes) + 1.0, float64(failures) + 1.0
}

// ConfidenceInterval 计算可靠性的 95% 置信区间
func ConfidenceInterval(alpha, beta float64) (lower, upper float64) {
	// 使用 Beta 分布的分位数近似
	// 简化版本：使用正态近似
	mean := alpha / (alpha + beta)
	variance := (alpha * beta) / ((alpha + beta) * (alpha + beta) * (alpha + beta + 1))
	stddev := math.Sqrt(variance)

	// 95% CI: mean ± 1.96 * stddev
	lower = math.Max(0, mean-1.96*stddev)
	upper = math.Min(1, mean+1.96*stddev)
	return
}

// ThompsonSamplingDecay 衰减历史数据权重（避免过时数据主导）
// 参数:
//   - alpha, beta: 当前后验参数
//   - decayFactor: 衰减因子 (0-1)，越小衰减越强
//
// 返回: 衰减后的 alpha, beta
func ThompsonSamplingDecay(alpha, beta, decayFactor float64) (newAlpha, newBeta float64) {
	if decayFactor <= 0 || decayFactor >= 1 {
		return alpha, beta
	}

	// 保留先验 Beta(1, 1)，衰减观测数据
	priorAlpha := 1.0
	priorBeta := 1.0

	observedAlpha := alpha - priorAlpha
	observedBeta := beta - priorBeta

	newAlpha = priorAlpha + observedAlpha*decayFactor
	newBeta = priorBeta + observedBeta*decayFactor

	return newAlpha, newBeta
}

// BenchmarkIntelligenceRank 根据模型名称推断智能排名（简化版）
// 返回 1-100，越小越聪明
func BenchmarkIntelligenceRank(modelName string) int {
	// 简化映射，实际应从数据库或配置读取
	// 参考 LMSYS Chatbot Arena 排行榜
	// 注意：按模式长度从长到短排序，避免短模式误匹配
	rankings := []struct {
		pattern string
		rank    int
	}{
		{"claude-3.5-sonnet", 1},
		{"gemini-2.0-flash", 6},
		{"gemini-1.5-pro", 4},
		{"llama-3.1-405b", 7},
		{"llama-3.3-70b", 12},
		{"mistral-large", 15},
		{"claude-3-sonnet", 8},
		{"claude-3-haiku", 20},
		{"claude-3-opus", 2},
		{"qwen-2.5-72b", 18},
		{"gpt-4-turbo", 3},
		{"deepseek-v3", 9},
		{"gemini-pro", 10},
		{"gpt-4o", 1},
		{"gpt-4", 5},
	}

	modelLower := toLower(modelName)

	// 按顺序匹配（长模式优先）
	for _, entry := range rankings {
		if contains(modelLower, entry.pattern) {
			return entry.rank
		}
	}

	// 默认中等智能
	return 50
}

// containsIgnoreCase 不区分大小写的字符串包含检查
func containsIgnoreCase(s, substr string) bool {
	sLower := toLower(s)
	substrLower := toLower(substr)
	return contains(sLower, substrLower)
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + ('a' - 'A')
		} else {
			result[i] = c
		}
	}
	return string(result)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0
}

func indexOfSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// ScoreSnapshot 评分快照（用于日志/监控）
type ScoreSnapshot struct {
	CredentialID    string  `json:"credential_id"`
	Reliability     float64 `json:"reliability"`
	Speed           float64 `json:"speed"`
	Intelligence    float64 `json:"intelligence"`
	Headroom        float64 `json:"headroom"`
	RateLimitFactor float64 `json:"rate_limit_factor"`
	CombinedScore   float64 `json:"combined_score"`
	TotalRequests   int64   `json:"total_requests"`
	SuccessRate     float64 `json:"success_rate"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	RateLimitHits   int     `json:"rate_limit_hits"`
	QuotaRemaining  *int64  `json:"quota_remaining,omitempty"`
}

// SnapshotScore 生成评分快照
func (b *BanditScorer) SnapshotScore(credID string) ScoreSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	score := b.getOrCreateScoreLocked(credID)

	reliability := ExpectedReliability(score.Alpha, score.Beta)
	speed := b.speedScore(score)
	intelligence := b.intelligenceScore(score)
	headroom := b.headroomFactor(score)
	rateLimitFactor := b.rateLimitFactor(score)

	var avgLatency float64
	if score.TotalRequests > 0 {
		avgLatency = float64(score.TotalLatencyMs) / float64(score.TotalRequests)
	}

	var successRate float64
	if score.TotalRequests > 0 {
		successRate = float64(score.SuccessRequests) / float64(score.TotalRequests)
	}

	// 使用默认权重计算综合得分
	weights := GetPresetWeights(StrategyBalanced)
	combined := reliability*weights.Reliability +
		speed*weights.Speed +
		intelligence*weights.Intelligence
	combined = combined * headroom * rateLimitFactor

	return ScoreSnapshot{
		CredentialID:    credID,
		Reliability:     reliability,
		Speed:           speed,
		Intelligence:    intelligence,
		Headroom:        headroom,
		RateLimitFactor: rateLimitFactor,
		CombinedScore:   combined,
		TotalRequests:   score.TotalRequests,
		SuccessRate:     successRate,
		AvgLatencyMs:    avgLatency,
		RateLimitHits:   score.RateLimitHits,
		QuotaRemaining:  score.QuotaRemaining,
	}
}
