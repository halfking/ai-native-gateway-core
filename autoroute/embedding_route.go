package autoroute

// embedding_route.go — model=auto for embeddings (22 章 §22.2).
//
// Embedding requests have no system/user prompt and no task taxonomy, so
// they bypass the 8-task classifier. RecommendByModality selects the best
// candidate for a given modality (currently "embedding") using a simplified
// 3-dim score: stability (success_rate) + speed (p95 latency) + price.
//
// Used by EmbeddingsHandler when the client sends model="auto".

import (
	"sort"
)

// RecommendByModality returns the top-N candidates whose Modality matches
// `modality` (e.g. "embedding"), scored by a 3-dim embedding-appropriate
// formula and sorted best-first. Only routable candidates
// (UnavailableReason == "") are considered.
//
// Scoring (each 0-100, weighted sum):
//
//	stability: success_rate × 100              (weight 0.5)
//	speed     : (1 - p95/cohortWorstP95) × 100 (weight 0.3)
//	price     : inverted normalized blended price (weight 0.2)
//
// Candidates with unknown success_rate/latency get a neutral 50 in that
// channel (not penalised). Free models (price=0) score 100 on price.
// Returns nil if no routable candidate matches the modality.
//
// This is deliberately simpler than Recommend()'s 8-dim composite:
// embeddings don't have context-fit, task-match, version-recency or
// strength-match signals that matter for chat models.
func (idx *Index) RecommendByModality(modality string, topN int) []ScoredCandidate {
	idx.mu.RLock()
	all := idx.entries
	idx.mu.RUnlock()

	if topN <= 0 {
		topN = 3
	}

	pool := make([]Candidate, 0, len(all))
	for i := range all {
		c := all[i]
		if c.UnavailableReason != "" {
			continue
		}
		if c.Modality != modality {
			continue
		}
		pool = append(pool, c)
	}
	if len(pool) == 0 {
		return nil
	}

	var worstP95 int
	var prices []float64
	for _, c := range pool {
		if c.P95LatencyMs > worstP95 {
			worstP95 = c.P95LatencyMs
		}
		blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if blended > 0 {
			prices = append(prices, blended)
		}
	}
	priceP75 := percentileFloat(prices, 0.75)

	scored := make([]ScoredCandidate, 0, len(pool))
	for _, c := range pool {
		stab := scoreStability(c)
		speed := scoreSpeed(c, CostContext{SpeedP95: float64(worstP95)})
		price := scorePrice(c, CostContext{PriceP75: priceP75})
		scored = append(scored, ScoredCandidate{
			Candidate: c,
			Breakdown: ScoringBreakdown{
				StabilityScore: stab,
				SpeedScore:     speed,
				PriceScore:     price,
				Composite:      stab*0.5 + speed*0.3 + price*0.2,
			},
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
	})
	if len(scored) > topN {
		scored = scored[:topN]
	}
	return scored
}

// percentileFloat returns the p-th percentile (0-1) of an UNSORTED slice.
// Empty/nil → 0. Separate from index.go's sort-then-percentile helper
// because prices may be empty when all candidates are free.
func percentileFloat(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	rank := int(float64(len(cp))*p + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(cp) {
		rank = len(cp)
	}
	return cp[rank-1]
}
