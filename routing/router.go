package routing

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"

	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var tierOrder = [4]int{1, 2, 3, 9}

type Router struct {
	Sticky  *StickyCache
	Limiter *limiter.Limiter
	// FpSlots is the credential-level concurrency tracker. When set,
	// loadScore includes FP slot pressure in its P2C selection.
	FpSlots interface {
		Enabled() bool
		Stats(ctx context.Context, credentialID int, limit *int) (slotLimit, used, free *int)
	}
}

func NewRouter(sticky *StickyCache, lim *limiter.Limiter) *Router {
	return &Router{Sticky: sticky, Limiter: lim}
}

func (r *Router) PlanCandidates(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	available := filterAvailable(candidates)
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
		ordered = append(ordered, p2cOrder(bucket, r)...)
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

func filterAvailable(cands []provider.Candidate) []provider.Candidate {
	var out []provider.Candidate
	for _, c := range cands {
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
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
		chosen := a
		if loadScore(b, r, ctx) < loadScore(a, r, ctx) {
			chosen = b
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
	w := c.Weight
	if w <= 0 {
		w = 1
	}
	quality := c.SuccessRate
	if quality <= 0 {
		quality = 0.9
	}
	if quality < 0.2 {
		quality = 0.2
	}

	latencyPenalty := float64(c.P95LatencyMs) / 1000.0
	if latencyPenalty < 1.0 {
		latencyPenalty = 1.0
	}

	// Per-identity in-flight from limiter (legacy)
	inFlight := 0
	if r.Limiter != nil {
		if cred := r.Limiter.Credential(c.ProviderID, c.CredentialID); cred != nil {
			inFlight = cred.Used()
		}
	}

	// Global credential-level concurrency from FpSlots (load-aware routing)
	// This is the key addition: we now factor in the credential's total
	// concurrent usage across all identities, not just this request's identity.
	fpUsed := 0
	fpLimit := 5 // default
	if r.FpSlots != nil && r.FpSlots.Enabled() {
		if limit, used, _ := r.FpSlots.Stats(ctx, c.CredentialID, c.ConcurrencyLimit); used != nil {
			fpUsed = *used
			if limit != nil {
				fpLimit = *limit
			}
		}
	}

	// Pressure ratio: how full is this credential's concurrency pool?
	// 0.0 = empty, 1.0 = saturated
	pressure := float64(fpUsed) / float64(fpLimit)
	if pressure > 1.0 {
		pressure = 1.0
	}

	// Weighted score: higher pressure → higher score → less likely to be chosen
	// Legacy inFlight is kept for backward compat (per-identity fairness)
	// New pressure term dominates when fpUsed >> inFlight
	return (float64(inFlight) + pressure*float64(fpLimit)) * latencyPenalty / (float64(w) * quality)
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
