package executors

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/ursm"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var tierOrder = [4]int{1, 2, 3, 9}

type Router struct {
	Sticky  *StickyCache
	Limiter *credential.Limiter
	// FpSlots is the credential-level concurrency tracker. When set,
	// loadScore includes FP slot pressure in its P2C selection.
	FpSlots interface {
		Enabled() bool
		Stats(ctx context.Context, credentialID int, limit *int) (slotLimit, used, free *int)
		GetNodeState(ctx context.Context, credentialID int, model string) (*credentialfpslot.NodeState, error)
	}
	// Bandit is the Thompson Sampling bandit scorer for intelligent credential
	// selection. When set, planByTier uses bandit scoring instead of P2C within
	// each tier. Falls back to P2C if Bandit is nil.
	Bandit *credential.BanditScorer
	// BanditFlusher is the async batch writer for Bandit state. When set,
	// the executor calls MarkDirty after recording success/failure events.
	BanditFlusher interface {
		MarkDirty(credentialID string)
	}
	// rrCounter is a round-robin counter for load balancing when multiple
	// candidates have equal routing scores. Prevents all requests from
	// always selecting the first candidate in a sorted list.
	rrCounter atomic.Uint64

	// 新增：状态管理器引用（向后兼容）
	StateManager credentialstate.StateProvider

	// 新增：URSM统一路由状态管理器
	URSM *ursm.Manager

	// 新增：路由评分权重配置（Phase 1）
	LoadScoreWeights LoadScoreWeights
}

func NewRouter(sticky *StickyCache, lim *credential.Limiter) *Router {
	return &Router{
		Sticky:           sticky,
		Limiter:          lim,
		LoadScoreWeights: DefaultLoadScoreWeights(), // Phase 1: 使用默认权重
	}
}

func (r *Router) PlanCandidates(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	candidates = deduplicateCandidates(candidates)
	if len(candidates) == 0 {
		return nil
	}

	// 新增：优先使用URSM路由（如果可用）
	if r.URSM != nil && r.URSM.Enabled() {
		return r.planWithURSM(candidates, stickyCredentialID, policy, egressPreference)
	}

	// 使用状态管理器过滤（如果启用）
	var available []provider.Candidate
	if r.StateManager != nil && r.StateManager.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		available = r.filterAvailableWithStateManager(ctx, candidates)
	} else {
		available = filterAvailable(candidates)
	}

	if len(available) == 0 {
		// Build a per-reason breakdown so the next "all providers failed at
		// the same time" outage can be root-caused from this log line alone.
		reasonCounts := make(map[string]int, 8)
		var sampleReasons []string
		for _, c := range candidates {
			reason := c.UnavailableReason()
			if reason == "" {
				reason = "unknown"
			}
			reasonCounts[reason]++
			if len(sampleReasons) < 5 {
				sampleReasons = append(sampleReasons, fmt.Sprintf(
					"cred=%d prov=%d reason=%s", c.CredentialID, c.ProviderID, reason,
				))
			}
		}

		// 2026-07-04: 单候选者降级逻辑（minimax-m3 model_not_found 修复）
		// 当所有候选者都被过滤，但只有1-2个候选者且原因是瞬态的（cooling, rate_limited, suspicious），
		// 则降级使用该候选者，避免完全失败。这是针对单点候选者场景的容错机制。
		if len(candidates) <= 2 {
			degradedCandidates := r.tryDegradedMode(candidates)
			if len(degradedCandidates) > 0 {
				slog.Warn("router: degraded mode activated, using transiently unavailable candidates",
					"total_candidates", len(candidates),
					"degraded_count", len(degradedCandidates),
					"reasons", reasonCounts,
				)
				return degradedCandidates
			}
		}

		slog.Warn("router: all candidates unavailable",
			"total", len(candidates),
			"reasons", reasonCounts,
			"sample", sampleReasons,
		)
		return nil
	}

	available = r.filterHealthyNodes(available)

	// Round 1: token_plan / code_plan / agent_plan / free — always before PAYG.
	// Round 2: token (按量). Executor skips saturated round-1 creds and falls through.
	round1, round2 := splitByBillingRound(available)
	ordered := r.planByTier(round1, policy)
	if len(round2) > 0 {
		ordered = append(ordered, r.planByTier(round2, policy)...)
	}

	// Note (2026-07-07 audit): an earlier "session-aware" round-robin rotation
	// of the full `ordered` slice lived here. It was added in cf65803f to spread
	// new-session traffic when loadScore values tie, but it rotated ACROSS tiers
	// — which violates tier priority semantics (a tier-3 candidate could be
	// tried before a tier-1 candidate). Per-tier load balancing is already
	// performed inside planByTier (rotateCandidates per tier bucket), so the
	// cross-tier rotation here was both redundant and harmful. Removed to
	// restore correct tier ordering; the per-tier rotation in planByTier still
	// provides even distribution for candidates within the same tier.

	if stickyCredentialID != nil {
		ordered = prioritizeSticky(ordered, *stickyCredentialID)
	}

	if len(egressPreference) > 0 {
		ordered = applyProtocolAffinity(ordered, egressPreference)
	}

	return ordered
}

// deduplicateCandidates keeps one route slot per provider/credential/model.
// Repeated binding rows must not amplify one upstream failure into multiple
// attempts against the same credential or consume its slots repeatedly.
func deduplicateCandidates(candidates []provider.Candidate) []provider.Candidate {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]provider.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := fmt.Sprintf("%d:%d:%s", candidate.ProviderID, candidate.CredentialID, strings.ToLower(candidate.RawModel))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

// planWithURSM 使用URSM路由（新增）
func (r *Router) planWithURSM(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	// 提取model和sessionID
	model := candidates[0].RawModel
	sessionID := "" // TODO: 从context或请求参数中获取

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 调用URSM获取可用节点
	nodes, err := r.URSM.GetAvailableNodes(ctx, model, sessionID)
	if err != nil {
		slog.Warn("ursm get nodes failed, fallback to legacy",
			"error", err,
			"model", model)
		// 回退到旧逻辑
		return r.planLegacy(candidates, stickyCredentialID, policy, egressPreference)
	}

	// 转换RouteNode到Candidate
	result := make([]provider.Candidate, 0, len(nodes))
	for _, node := range nodes {
		// 在原始candidates中找到匹配的Candidate
		for _, cand := range candidates {
			if cand.CredentialID == node.CredentialID && cand.RawModel == node.RawModel {
				result = append(result, cand)
				break
			}
		}
	}

	// 应用sticky偏好
	if stickyCredentialID != nil {
		result = prioritizeSticky(result, *stickyCredentialID)
	}

	// 应用协议偏好
	if len(egressPreference) > 0 {
		result = applyProtocolAffinity(result, egressPreference)
	}

	return result
}

// planLegacy 保留旧逻辑（向后兼容）
func (r *Router) planLegacy(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	// 使用状态管理器过滤（如果启用）
	var available []provider.Candidate
	if r.StateManager != nil && r.StateManager.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		available = r.filterAvailableWithStateManager(ctx, candidates)
	} else {
		available = filterAvailable(candidates)
	}

	if len(available) == 0 {
		// Build a per-reason breakdown so the next "all providers failed at
		// the same time" outage can be root-caused from this log line alone.
		reasonCounts := make(map[string]int, 8)
		var sampleReasons []string
		for _, c := range candidates {
			reason := c.UnavailableReason()
			if reason == "" {
				reason = "unknown"
			}
			reasonCounts[reason]++
			if len(sampleReasons) < 5 {
				sampleReasons = append(sampleReasons, fmt.Sprintf(
					"cred=%d prov=%d reason=%s", c.CredentialID, c.ProviderID, reason,
				))
			}
		}
		slog.Warn("router: all candidates unavailable",
			"total", len(candidates),
			"reasons", reasonCounts,
			"sample", sampleReasons,
		)
		return nil
	}

	available = r.filterHealthyNodes(available)

	// Round 1: token_plan / code_plan / agent_plan / free — always before PAYG.
	// Round 2: token (按量). Executor skips saturated round-1 creds and falls through.
	round1, round2 := splitByBillingRound(available)
	ordered := r.planByTier(round1, policy)
	if len(round2) > 0 {
		ordered = append(ordered, r.planByTier(round2, policy)...)
	}

	if stickyCredentialID != nil {
		ordered = prioritizeSticky(ordered, *stickyCredentialID)
	}

	if len(egressPreference) > 0 {
		ordered = applyProtocolAffinity(ordered, egressPreference)
	}

	return ordered
}

func splitByBillingRound(cands []provider.Candidate) (round1, round2 []provider.Candidate) {
	for _, c := range cands {
		if provider.IsPreferredPlanBilling(c.BillingMode) {
			round1 = append(round1, c)
		} else {
			round2 = append(round2, c)
		}
	}
	return round1, round2
}

func (r *Router) planByTier(candidates []provider.Candidate, policy *provider.Policy) []provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	byTier := make(map[int][]provider.Candidate)
	for _, c := range candidates {
		byTier[c.Tier] = append(byTier[c.Tier], c)
	}

	tiersUsed := 0
	var ordered []provider.Candidate
	for _, tier := range tierOrder {
		bucket := byTier[tier]
		if len(bucket) == 0 {
			continue
		}

		// Hybrid mode: use Bandit if available, fall back to P2C
		var sorted []provider.Candidate
		if r.Bandit != nil {
			// Thompson Sampling Bandit ordering (with pressure factor)
			sorted = r.banditOrder(bucket)
		} else {
			// Legacy P2C ordering (load-aware)
			sorted = p2cOrder(bucket, r)
		}

		// Apply round-robin rotation when multiple candidates exist
		// This prevents always selecting the first candidate when scores are equal
		if len(sorted) > 1 {
			offset := int(r.rrCounter.Add(1) % uint64(len(sorted)))
			slog.Info("ROUND_ROBIN_DEBUG", "counter", r.rrCounter.Load(), "offset", offset, "bucket_size", len(sorted))
			sorted = rotateCandidates(sorted, offset)
		}

		ordered = append(ordered, sorted...)
		tiersUsed++
		if tiersUsed >= policy.TierFallbackMax {
			break
		}
	}

	maxTotal := 12
	if len(ordered) > maxTotal {
		ordered = ordered[:maxTotal]
	}
	return ordered
}

// rotateCandidates circularly shifts the candidate slice by offset positions.
// This ensures fair load distribution when multiple candidates have equal scores.
func rotateCandidates(cands []provider.Candidate, offset int) []provider.Candidate {
	if offset == 0 || len(cands) <= 1 {
		return cands
	}
	offset = offset % len(cands)
	out := make([]provider.Candidate, len(cands))
	for i := range cands {
		out[i] = cands[(i+offset)%len(cands)]
	}
	return out
}

func filterAvailable(cands []provider.Candidate) []provider.Candidate {
	var out []provider.Candidate
	for _, c := range cands {
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
}

// DEPRECATED: filterAvailableWithStateManager 将被 URSM.GetAvailableNodes() 替代
// Replaced by: r.URSM.GetAvailableNodes(ctx, filters)
// Migration date: 2026-07-03
// Status: 等待 Router 适配 URSM 完成后删除此方法
// DO NOT use this method in new code. Use URSM.GetAvailableNodes() instead.
//
// filterAvailableWithStateManager 新增：使用状态管理器优先判断可用性
func (r *Router) filterAvailableWithStateManager(ctx context.Context, cands []provider.Candidate) []provider.Candidate {
	var out []provider.Candidate
	for _, c := range cands {
		// 优先查询状态管理器
		if r.StateManager != nil && r.StateManager.Enabled() {
			available, reason := r.StateManager.IsAvailable(ctx, c.CredentialID, c.RawModel)
			if !available {
				slog.Debug("router: filtered by state manager",
					"credential_id", c.CredentialID,
					"model", c.RawModel,
					"reason", reason)
				continue
			}
		}

		// 回退到原有逻辑
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
}

func (r *Router) filterHealthyNodes(candidates []provider.Candidate) []provider.Candidate {
	if r.FpSlots == nil || !r.FpSlots.Enabled() {
		return candidates
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	healthy := make([]provider.Candidate, 0, len(candidates))
	filtered := make([]provider.Candidate, 0)
	now := time.Now()
	for _, cand := range candidates {
		state, err := r.FpSlots.GetNodeState(ctx, cand.CredentialID, cand.RawModel)
		if err != nil {
			healthy = append(healthy, cand)
			continue
		}
		if state == nil || state.IsUsable(now) {
			healthy = append(healthy, cand)
			continue
		}
		slog.Debug("router: route node filtered out",
			"credential_id", cand.CredentialID,
			"model", cand.RawModel,
			"disabled", state.Disabled,
			"failure_count", state.FailureCount,
			"consecutive_failures", state.ConsecutiveFailureStreak(now),
		)
		filtered = append(filtered, cand)
	}
	if len(healthy) == 0 && len(filtered) > 0 {
		slog.Warn("router: all route nodes filtered by health state, failing open",
			"total", len(candidates),
			"filtered", len(filtered),
		)
		return candidates
	}
	return healthy
}

func p2cOrder(cands []provider.Candidate, r *Router) []provider.Candidate {
	if len(cands) <= 1 {
		return cands
	}

	pool := make([]provider.Candidate, len(cands))
	copy(pool, cands)
	out := make([]provider.Candidate, 0, len(pool))

	ctx := context.Background() // for FpSlots.Stats

	for len(pool) > 0 {
		if len(pool) == 1 {
			out = append(out, pool[0])
			break
		}

		minCost := cheapestCost(pool)
		closePool := make([]provider.Candidate, 0, len(pool))
		for _, c := range pool {
			cost := blendedCost(c)
			if cost == 0 || cost <= minCost*1.10 {
				closePool = append(closePool, c)
			}
		}
		samplePool := closePool
		if len(samplePool) < 2 {
			samplePool = pool
		}

		a, b := randomPair(samplePool)
		scoreA := loadScore(a, r, ctx)
		scoreB := loadScore(b, r, ctx)
		chosen := a
		if scoreB < scoreA {
			chosen = b
		} else if scoreB == scoreA {
			// 2026-06-25: When load scores are equal (e.g., both free local
			// mocks with same routing_score), P2C previously always picked 'a'
			// (the first random sample). This biased load distribution toward
			// whichever candidate happened to be drawn first, causing 83/17
			// splits instead of 50/50. Fix: randomize on equal scores to
			// match the round-robin rotation done at the planByTier level.
			if rand.Intn(2) == 0 {
				chosen = b
			}
		}

		out = append(out, chosen)
		pool = removeCandidate(pool, chosen)
	}
	return out
}

func blendedCost(c provider.Candidate) float64 {
	in := 0.0
	out := 0.0
	if c.PriceInPer1M != nil {
		in = *c.PriceInPer1M
	}
	if c.PriceOutPer1M != nil {
		out = *c.PriceOutPer1M
	}
	return in + out
}

func cheapestCost(pool []provider.Candidate) float64 {
	min := 0.0
	for _, c := range pool {
		cost := blendedCost(c)
		if cost > 0 {
			if min == 0 || cost < min {
				min = cost
			}
		}
	}
	return min
}

func loadScore(c provider.Candidate, r *Router, ctx context.Context) float64 {
	// Phase 1 改进：使用新的评分方法
	return calculateLoadScore(c, r, ctx, r.LoadScoreWeights)
}

func randomPair(pool []provider.Candidate) (provider.Candidate, provider.Candidate) {
	n := len(pool)
	i := rand.Intn(n)
	j := rand.Intn(n - 1)
	if j >= i {
		j++
	}
	return pool[i], pool[j]
}

func removeCandidate(pool []provider.Candidate, target provider.Candidate) []provider.Candidate {
	for i, c := range pool {
		if c.CredentialID == target.CredentialID && c.ProviderID == target.ProviderID {
			return append(pool[:i], pool[i+1:]...)
		}
	}
	return pool
}

func prioritizeSticky(ordered []provider.Candidate, stickyID int) []provider.Candidate {
	var sticky, rest []provider.Candidate
	for _, c := range ordered {
		if c.CredentialID == stickyID {
			sticky = append(sticky, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(sticky, rest...)
}

func applyProtocolAffinity(ordered []provider.Candidate, pref []string) []provider.Candidate {
	if len(pref) == 0 || len(ordered) <= 1 {
		return ordered
	}
	prefIndex := make(map[string]int, len(pref))
	for i, p := range pref {
		prefIndex[p] = i
	}
	defaultRank := len(prefIndex)

	sort.SliceStable(ordered, func(i, j int) bool {
		ri := prefIndex[ordered[i].Protocol]
		if _, ok := prefIndex[ordered[i].Protocol]; !ok {
			ri = defaultRank
		}
		rj := prefIndex[ordered[j].Protocol]
		if _, ok := prefIndex[ordered[j].Protocol]; !ok {
			rj = defaultRank
		}
		if ri != rj {
			return ri < rj
		}
		return ordered[i].SuccessRate > ordered[j].SuccessRate
	})
	return ordered
}

// ScoringWeights defines the weights for composite score calculation
type ScoringWeights struct {
	Price           float64 `json:"price"`
	SessionLoad     float64 `json:"session_load"`
	FailurePenalty  float64 `json:"failure_penalty"`
	DefaultPriceCNY float64 `json:"default_price_cny"`
	DefaultPriceUSD float64 `json:"default_price_usd"`
}

// DefaultScoringWeights returns the default scoring weights
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Price:           10,
		SessionLoad:     5,
		FailurePenalty:  20,
		DefaultPriceCNY: 5.0,
		DefaultPriceUSD: 5.0,
	}
}

// CalculateCompositeScore computes the composite score for a candidate
// Lower score = higher priority. Free models (cost=0) get score=0 (highest priority)
func CalculateCompositeScore(c provider.Candidate, weights ScoringWeights) float64 {
	// Free models get highest priority (score=0)
	cost := blendedCost(c)
	if cost == 0 {
		return 0
	}

	// Start with manual priority (1-99)
	score := float64(c.ManualPriority)

	// Normalize cost based on currency
	var defaultPrice float64
	if c.Currency == "CNY" {
		defaultPrice = weights.DefaultPriceCNY
	} else {
		defaultPrice = weights.DefaultPriceUSD
	}
	if defaultPrice <= 0 {
		defaultPrice = 5.0
	}
	score += (cost / defaultPrice) * weights.Price

	// Session load (0-1)
	if c.ConcurrencyLimit != nil && *c.ConcurrencyLimit > 0 {
		load := float64(c.ActiveSessions) / float64(*c.ConcurrencyLimit)
		if load > 1 {
			load = 1
		}
		score += load * weights.SessionLoad
	}

	// Failure penalty
	score += float64(c.ConsecutiveFailures) * weights.FailurePenalty

	return score
}

// CompareCandidatePriority returns true when a should sort before b.
// Billing round (plan/free before PAYG) takes precedence over composite score.
func CompareCandidatePriority(a, b provider.Candidate) bool {
	ra, rb := provider.BillingRound(a.BillingMode), provider.BillingRound(b.BillingMode)
	if ra != rb {
		return ra < rb
	}
	if a.CompositeScore != b.CompositeScore {
		return a.CompositeScore < b.CompositeScore
	}
	if a.ManualPriority != b.ManualPriority {
		return a.ManualPriority < b.ManualPriority
	}
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	return a.CredentialID < b.CredentialID
}

// SortByCompositeScore sorts candidates by billing round then composite score (ascending).
func SortByCompositeScore(candidates []provider.Candidate, weights ScoringWeights) []provider.Candidate {
	for i := range candidates {
		candidates[i].CompositeScore = CalculateCompositeScore(candidates[i], weights)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return CompareCandidatePriority(candidates[i], candidates[j])
	})

	return candidates
}

// banditOrder orders candidates using Thompson Sampling bandit algorithm.
// This provides intelligent credential selection based on historical performance.
// Falls back to P2C if any step fails.
func (r *Router) banditOrder(cands []provider.Candidate) []provider.Candidate {
	if len(cands) <= 1 || r.Bandit == nil {
		return cands
	}

	ctx := context.Background()

	// Score each candidate using Bandit + pressure factor
	type scoredCandidate struct {
		cand  provider.Candidate
		score float64
	}
	scored := make([]scoredCandidate, 0, len(cands))

	for _, c := range cands {
		// Get bandit score (0-1, higher is better)
		credID := fmt.Sprintf("%d", c.CredentialID)
		banditScore := r.Bandit.Sample(credID)

		// Apply pressure factor to avoid overloading high-performing credentials
		pressureFactor := 1.0
		if r.FpSlots != nil && r.FpSlots.Enabled() {
			if limit, used, _ := r.FpSlots.Stats(ctx, c.CredentialID, c.ConcurrencyLimit); used != nil && limit != nil && *limit > 0 {
				pressure := float64(*used) / float64(*limit)
				if pressure > 1.0 {
					pressure = 1.0
				}
				// pressureFactor: 1.0 when empty, 0.0 when saturated
				pressureFactor = 1.0 - pressure
			}
		}

		// Final score: bandit × pressure
		// If credential is saturated (pressureFactor=0), score becomes 0
		finalScore := banditScore * pressureFactor

		scored = append(scored, scoredCandidate{
			cand:  c,
			score: finalScore,
		})
	}

	// Sort by score descending (higher is better)
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// Tie-breaker: credential ID
		return scored[i].cand.CredentialID < scored[j].cand.CredentialID
	})

	// Extract sorted candidates
	result := make([]provider.Candidate, len(scored))
	for i, sc := range scored {
		result[i] = sc.cand
	}

	return result
}

// tryDegradedMode 尝试在单候选者场景下启用降级模式。
// 当唯一的候选者因为瞬态原因（cooling, rate_limited, suspicious）被过滤时，
// 降级使用该候选者而不是返回 model_not_found，避免完全失败。
//
// 这是针对 2026-07-03 minimax-m3 model_not_found 问题的修复：
// 当只有1个候选者且因瞬态原因不可用时，系统会进入完全失败状态，
// 即使该候选者可能在几秒后恢复。降级模式允许在这种情况下继续使用该候选者。
//
// 2026-07-04: 单候选者降级逻辑
func (r *Router) tryDegradedMode(candidates []provider.Candidate) []provider.Candidate {
	var degradedCandidates []provider.Candidate

	for _, c := range candidates {
		reason := c.UnavailableReason()
		if isTransientUnavailableReason(reason) {
			slog.Info("router: degraded mode candidate accepted",
				"credential_id", c.CredentialID,
				"provider_id", c.ProviderID,
				"model", c.RawModel,
				"reason", reason,
			)
			degradedCandidates = append(degradedCandidates, c)
		}
	}

	return degradedCandidates
}

// isTransientUnavailableReason 判断不可用原因是否为瞬态的。
// 瞬态原因包括：
// - availability:cooling - 冷却期，通常几分钟后恢复
// - availability:rate_limited - 速率限制，通常几秒到几分钟后恢复
// - availability:suspended - 临时暂停，可能很快恢复
//
// 永久原因（不应降级使用）：
// - availability:auth_failed - 认证失败，需要人工修复
// - quota:balance_exhausted - 余额耗尽，需要充值
// - lifecycle:disabled - 已禁用，需要人工启用
//
// 2026-07-04: 单候选者降级逻辑
func isTransientUnavailableReason(reason string) bool {
	switch reason {
	case "availability:cooling",
		"availability:rate_limited",
		"availability:suspended":
		return true
	default:
		return false
	}
}
