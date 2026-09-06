package routingopt

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
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

	// Cached active state (refreshed every 5 minutes). Guarded by cacheMu —
	// Recommend runs on concurrent request goroutines. The DB refresh runs
	// OUTSIDE cacheMu (double-checked locking): holding the lock across the
	// query serialized every concurrent Decide() behind one slow DB round
	// trip (2026-09-07 audit P1).
	cacheMu          sync.Mutex
	cachedState      *OptimizationState
	cacheRefreshedAt time.Time
	cacheTTL         time.Duration

	// Negative cache: a failing GetActive (missing table, migration not yet
	// applied) is remembered for negCacheTTL so the hot path neither hammers
	// the DB once per request nor floods the log with identical warnings.
	lastErr    error
	negUntil   time.Time
	negCacheTTL time.Duration
}

// NewModelRecommender constructs a recommender instance.
func NewModelRecommender(pool *pgxpool.Pool) *ModelRecommender {
	return &ModelRecommender{
		stateDAO:    NewOptimizationStateDAO(pool),
		metricsDAO:  NewOptimizationMetricsDAO(pool),
		cacheTTL:    5 * time.Minute, // 缓存 5 分钟
		negCacheTTL: 30 * time.Second,
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

	// 2. ε-greedy exploration — rate is clamped to the documented [0, 0.2]
	// envelope: a corrupt routing_optimization_state row (e.g. exploration_rate
	// = 0.8) must not silently shuffle most production traffic
	// (2026-09-07 audit P1; the DB value is the runtime lever, the env var
	// only documents the default).
	if shouldExplore(clampExplorationRate(state.ExplorationRate)) {
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

// SetActiveStateForTest injects an optimization state without a DB pool.
// The injected state also primes the cache so Recommend never touches the DAO.
func (r *ModelRecommender) SetActiveStateForTest(state *OptimizationState) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.cachedState = state
	r.cacheRefreshedAt = time.Now()
}

// getActiveState returns the cached active optimization state.
// Refreshes the cache if expired (TTL = 5 minutes). The DB query executes
// OUTSIDE the mutex: at most one stale read per refresh window per
// in-flight request instead of serializing all of them (concurrent misses
// may each query once — bounded by the 5-minute TTL, acceptable vs. the
// head-of-line blocking the lock-inlined query caused). Failures are
// negatively cached for 30s so a missing table degrades to baseline
// routing without a per-request DB hit + warning flood. Safe for
// concurrent use.
func (r *ModelRecommender) getActiveState(ctx context.Context) (*OptimizationState, error) {
	now := time.Now()

	r.cacheMu.Lock()
	if r.cachedState != nil && now.Sub(r.cacheRefreshedAt) < r.cacheTTL {
		r.cacheMu.Unlock()
		return r.cachedState, nil
	}
	if r.lastErr != nil && now.Before(r.negUntil) {
		err := r.lastErr
		r.cacheMu.Unlock()
		return nil, err
	}
	r.cacheMu.Unlock()

	state, err := r.stateDAO.GetActive(ctx)

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if err != nil {
		r.lastErr = err
		r.negUntil = time.Now().Add(r.negCacheTTL)
		return nil, err
	}
	r.lastErr = nil
	r.negUntil = time.Time{}
	r.cachedState = state
	r.cacheRefreshedAt = time.Now()
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

	// Quality: from autoroute.Index. Composite arrives on a 0-100 scale from
	// the bridge; normalise defensively so quality (0.4 weight) cannot drown
	// the other [0,1] dimensions (audit finding: 0.4×90 = 36 vs ≤0.1 others).
	quality := cand.Score
	if quality > 1 {
		quality = quality / 100
	}

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

// maxExplorationRate is the documented upper bound for ε-greedy exploration
// (settings.RoutingOptFeatureFlags.ExplorationRate: "Range: 0.0-0.2").
const maxExplorationRate = 0.2

// clampExplorationRate confines the runtime exploration rate to [0, 0.2].
func clampExplorationRate(rate float64) float64 {
	if rate < 0 {
		return 0
	}
	if rate > maxExplorationRate {
		return maxExplorationRate
	}
	return rate
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

// sortByScore sorts scored candidates by descending score.
func sortByScore(scored []scoredCandidate) {
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
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
