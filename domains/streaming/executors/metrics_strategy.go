// metrics_strategy.go - GW-03/GW-04 路由策略 shadow diff 的 Prometheus 指标。
//
// 仅观测，不影响线上路由。labels 刻意低基数：
//   - strategy: p2c|cost-optimized|cache-optimized|context-aware|headroom
//   - outcome: agreed|disagreed
//
// 不使用 model/tenant_id/credential_id/request_id 等高基数维度（GW-00 门禁）。
package executors

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

var shadowStrategyOutcomes = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_routing_shadow_strategy_outcomes_total",
		Help: "Routing shadow strategy diff outcomes (observed only, does not change selection). " +
			"strategy=the shadow strategy name; outcome=agreed|disagreed vs the actual P2C/bandit winner.",
	},
	[]string{"strategy", "outcome"},
)

func recordShadowStrategyOutcome(strategy, outcome string) {
	shadowStrategyOutcomes.WithLabelValues(strategy, outcome).Inc()
}

// unknownPriceShadow (P1-C-② shadow, audit 2026-10-28 §4): counts what the
// proposed "unknown price → pool median" policy WOULD change, without
// changing any score. Today an unknown-price Round-2 candidate eats the max
// penalty (1.0); the proposal replaces it with the pool's median blended
// price. outcome=similar ≈ median within ±0.15 of the current penalty band
// (policy would barely move it); outcome=materially-lower = the candidate
// would be un-punished (the systematic Round-2 distortion the audit names —
// 46.8% of bindings carry no price and currently sort as worst); outcome=
// material-free-shadow events are the deployment-side signal to review.
// No candidate field changes: pure observation, flip the weight env later
// only after reading the rate.
var unknownPriceShadow = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_routing_unknown_price_shadow_total",
		Help: "Shadow observations of the proposed unknown-price→pool-median policy (no score change). outcome=similar|materially_lower.",
	},
	[]string{"outcome"},
)

// observeUnknownPriceShadow records the would-be effect of the median policy
// for one unknown-price candidate against the pool median blended price
// (USD after FX fold; 0 when the pool has no priced Round-2 rows ⇒ no-op).
func observeUnknownPriceShadow(poolMedianBlended float64) {
	if poolMedianBlended <= 0 {
		return // no priced peers ⇒ the policy has no opinion either
	}
	medianPenalty := poolMedianBlended / 30.0 // LLM_GATEWAY_ROUTING_COST_CAP default
	if medianPenalty > 1.0 {
		medianPenalty = 1.0
	}
	// Current penalty for unknown price is exactly 1.0.
	switch {
	case medianPenalty > 0.85:
		unknownPriceShadow.WithLabelValues("similar").Inc()
	default:
		unknownPriceShadow.WithLabelValues("materially_lower").Inc()
	}
}

// observeUnknownPricePool runs the P1-C-② shadow once per planCandidates:
// median blended price over priced Round-2 candidates (CNY folded via the
// scoring FX), then one observation per unknown-price Round-2 candidate.
func observeUnknownPricePool(cands []provider.Candidate) {
	prices := make([]float64, 0, len(cands))
	unknown := make([]provider.Candidate, 0)
	for _, c := range cands {
		if provider.BillingRound(c.BillingMode) != 2 {
			continue
		}
		if c.PriceInPer1M == nil || c.PriceOutPer1M == nil {
			unknown = append(unknown, c)
			continue
		}
		blended := *c.PriceInPer1M + *c.PriceOutPer1M
		if blended <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Currency), "CNY") {
			blended /= settings.CNYScoreFX()
		}
		prices = append(prices, blended)
	}
	if len(unknown) == 0 || len(prices) == 0 {
		return
	}
	sort.Float64s(prices)
	median := prices[len(prices)/2]
	for i := 0; i < len(unknown); i++ {
		observeUnknownPriceShadow(median)
	}
	_ = unknown // per-observation fan-out above; the slice itself is only counted
}
