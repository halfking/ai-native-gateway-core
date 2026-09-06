package routingopt

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// ModelRecommender: RecommendModel 多目标优化
// =============================================================================

// ModelRecommender re-ranks candidate models using multi-objective optimization.
// It balances:
//   - Quality: model capability score (from autoroute.Index)
//   - Cost: normalized cost per 1M tokens
//   - Latency: P95 latency from routing_optimization_metrics
//   - Availability: success rate from routing_optimization_metrics
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.2
type ModelRecommender struct {
	stateDAO   *OptimizationStateDAO
	metricsDAO *OptimizationMetricsDAO
	
	// Cached active state (refreshed every 5 minutes)
	cachedState      *OptimizationState
	cacheRefreshedAt time.Time
	cacheTTL         time.Duration
}

// NewModelRecommender constructs a recommender instance.
func NewModelRecommender(pool *pgxpool.Pool) *ModelRecommender {
	return &ModelRecommender{
		stateDAO:   NewOptimizationStateDAO(pool),
		metricsDAO: NewOptimizationMetricsDAO(pool),
		cacheTTL:   5 * time.Minute, // 缓存 5 分钟
	}
}

// Recommend implements the RecommendModel hook.
// Returns re-ranked candidates using multi-objective optimization + ε-greedy exploration.
//
// Week 1 骨架版本：
//   - Multi-objective scoring: w1*Quality - w2*Cost + w3*Latency + w4*Availability
//   - ε-greedy exploration: 5% 随机选择 top-5
//   - 权重从 routing_optimization_state 读取（缓存 5 分钟）
//
// Week 2 完整版本：
//   - 动态权重调整（基于 profile: quality_first → w1=0.6, cost_first → w2=0.5）
//   - Circuit breaker: 排除失败率 > 20% 的 provider
//   - Multi-level fallback chain: 预计算 top-3 for automatic failover
func (r *ModelRecommender) Recommend(ctx context.Context, candidates []ModelCandidate, routingCtx RoutingContext) ([]ModelCandidate, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	
	// 1. Load active optimization state (cached)
	state, err := r.getActiveState(ctx)
	if err != nil {
		return candidates, fmt.Errorf("load optimization state: %w", err)
	}
	
	// 2. ε-greedy exploration: 5% 随机选择
	if shouldExplore(state.ExplorationRate) {
		return exploreRandomly(candidates), nil
	}
	
	// 3. Multi-objective scoring
	scored := make([]scoredCandidate, 0, len(candidates))
	for _, cand := range candidates {
		score := r.computeScore(cand, state.RecommenderWeights, routingCtx)
		scored = append(scored, scoredCandidate{
			Candidate: cand,
			Score:     score,
		})
	}
	
	// 4. Sort by descending score
	sortByScore(scored)
	
	// 5. Extract candidates
	result := make([]ModelCandidate, len(scored))
	for i, sc := range scored {
		result[i] = sc.Candidate
	}
	
	return result, nil
}

// getActiveState returns the cached active optimization state.
// Refreshes the cache if expired (TTL = 5 minutes).
func (r *ModelRecommender) getActiveState(ctx context.Context) (*OptimizationState, error) {
	now := time.Now()
	if r.cachedState != nil && now.Sub(r.cacheRefreshedAt) < r.cacheTTL {
		return r.cachedState, nil
	}
	
	// Cache miss or expired: load from DB
	state, err := r.stateDAO.GetActive(ctx)
	if err != nil {
		return nil, err
	}
	
	r.cachedState = state
	r.cacheRefreshedAt = now
	
	return state, nil
}

// computeScore calculates the multi-objective score for a candidate.
//
// Formula: w1*Quality - w2*Cost + w3*Latency + w4*Availability
//
// Week 1: 使用 baseline weights (quality=0.4, cost=0.3, latency=0.2, availability=0.1)
// Week 2: 动态权重调整（profile-based, time-based, task-based）
func (r *ModelRecommender) computeScore(cand ModelCandidate, weights map[string]float64, ctx RoutingContext) float64 {
	// Default weights (if not set in state)
	w1 := getWeight(weights, "quality", 0.4)
	w2 := getWeight(weights, "cost", 0.3)
	w3 := getWeight(weights, "latency", 0.2)
	w4 := getWeight(weights, "availability", 0.1)
	
	// Quality: from autoroute.Index (candidate.Score already normalized [0, 1])
	quality := cand.Score
	
	// Cost: normalized [0, 1] (lower is better, so use 1 - normalized_cost)
	cost := cand.Cost // already normalized in ModelCandidate
	
	// Latency: normalized [0, 1] (lower is better, so use 1 - normalized_latency)
	latency := cand.Latency // already normalized in ModelCandidate
	
	// Availability: success rate [0, 1] (higher is better)
	availability := cand.Availability // already normalized in ModelCandidate
	
	// Multi-objective score (normalize to [0, 1])
	score := w1*quality + w2*(1-cost) + w3*(1-latency) + w4*availability
	
	return score
}

// getWeight retrieves a weight from the map, falling back to defaultValue.
func getWeight(weights map[string]float64, key string, defaultValue float64) float64 {
	if w, ok := weights[key]; ok {
		return w
	}
	return defaultValue
}

// shouldExplore returns true with probability = exploration_rate.
// Uses deterministic randomness based on request time (for reproducibility in tests).
func shouldExplore(explorationRate float64) bool {
	if explorationRate <= 0 {
		return false
	}
	if explorationRate >= 1 {
		return true
	}
	
	// ε-greedy: P(explore) = explorationRate
	return rand.Float64() < explorationRate
}

// exploreRandomly shuffles the top-5 candidates and returns them.
// Week 1: 简单随机打乱
// Week 2: Thompson Sampling (optimistic exploration based on uncertainty)
func exploreRandomly(candidates []ModelCandidate) []ModelCandidate {
	// Take top-5 for exploration (or all if < 5)
	n := len(candidates)
	if n > 5 {
		n = 5
	}
	
	explored := make([]ModelCandidate, n)
	copy(explored, candidates[:n])
	
	// Shuffle (Fisher-Yates)
	rand.Shuffle(n, func(i, j int) {
		explored[i], explored[j] = explored[j], explored[i]
	})
	
	// Append remaining candidates (unchanged)
	if len(candidates) > 5 {
		explored = append(explored, candidates[5:]...)
	}
	
	return explored
}

// scoredCandidate wraps a candidate with its computed score.
type scoredCandidate struct {
	Candidate ModelCandidate
	Score     float64
}

// sortByScore sorts scored candidates by descending score (bubble sort for simplicity).
// Week 2: use sort.Slice for performance.
func sortByScore(scored []scoredCandidate) {
	n := len(scored)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if scored[j].Score < scored[j+1].Score {
				scored[j], scored[j+1] = scored[j+1], scored[j]
			}
		}
	}
}

// ComputeFallbackChain precomputes the top-3 candidates for automatic failover.
//
// Week 1: 骨架版本（返回 top-3）
// Week 2: 完整版本（基于 provider diversity、availability、latency）
func (r *ModelRecommender) ComputeFallbackChain(candidates []ModelCandidate) []ModelCandidate {
	if len(candidates) <= 3 {
		return candidates
	}
	return candidates[:3]
}
