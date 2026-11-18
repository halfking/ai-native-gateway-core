// Package modelbudget implements the gateway half of Phase 2.4 economic
// matching: given a task's LLM budget (USD, the same currency the agent
// side's MaxCostPerTask gate uses), estimate a request's model cost and
// degrade to a cheaper model in the same family when the estimate exceeds
// the budget.
//
// Contract with the agent side (acc-go router): budgets are USD per task,
// and an unknown price never counts as over budget — a model the pricing
// source can't price estimates to 0, which fits any budget, so it passes
// through instead of being rejected. Both sides enforce the identical
// rule ("unknown price ≠ over budget"), keeping the merged economics
// closed under one interpretation.
//
// Wiring note: this package is deliberately self-contained — it does not
// touch domains/streaming/auto_route (resident-session territory). The
// request path integration is: read the task payload's max_cost_per_task
// field (injected by the acc-go orchestrator), call Degrader.Select, and
// route to Decision.Model. PricingSource is satisfied by
// approval.NewCostEstimator() as-is (identical Estimate signature), so
// the resident session can swap in the approval pricing table without an
// adapter.
package modelbudget

// PricingSource answers "what does one LLM call cost, USD". A model the
// source cannot price returns 0 — the caller-side rule treats that as
// "unknown, never over budget". approval.CostEstimator satisfies this
// interface unchanged.
type PricingSource interface {
	Estimate(model string, inputTokens, outputTokens int) float64
}

// modelPricing is USD per 1K tokens — the same unit the approval domain's
// pricing table uses.
type modelPricing struct {
	Input  float64
	Output float64
}

// Estimator is the default self-contained pricing source. Public list
// prices, aligned with the approval domain's table where both cover a
// model. Immutable after construction — safe for concurrent use without
// locking.
type Estimator struct {
	pricing map[string]modelPricing
}

// NewEstimator returns the default pricing table.
func NewEstimator() *Estimator {
	return &Estimator{pricing: map[string]modelPricing{
		// OpenAI
		"gpt-4":         {Input: 0.03, Output: 0.06},
		"gpt-4-turbo":   {Input: 0.01, Output: 0.03},
		"gpt-4o":        {Input: 0.005, Output: 0.015},
		"gpt-4o-mini":   {Input: 0.00015, Output: 0.0006},
		"gpt-3.5-turbo": {Input: 0.0005, Output: 0.0015},
		// Anthropic
		"claude-3-opus":     {Input: 0.015, Output: 0.075},
		"claude-3-5-sonnet": {Input: 0.003, Output: 0.015},
		"claude-3-sonnet":   {Input: 0.003, Output: 0.015},
		"claude-3-haiku":    {Input: 0.00025, Output: 0.00125},
		// Google
		"gemini-1.5-pro":   {Input: 0.0035, Output: 0.0105},
		"gemini-1.5-flash": {Input: 0.000075, Output: 0.0003},
	}}
}

// Estimate prices one call in USD. Unknown models price at 0 (unknown ≠
// over budget).
func (e *Estimator) Estimate(model string, inputTokens, outputTokens int) float64 {
	p, ok := e.pricing[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)/1000*p.Input + float64(outputTokens)/1000*p.Output
}

// Decision is the outcome of one budget-aware selection.
type Decision struct {
	Requested     string  // model the caller asked for
	Model         string  // final pick (== Requested unless degraded)
	EstimatedCost float64 // USD estimate of the FINAL pick
	Degraded      bool    // true when a cheaper family member was picked
	FitsBudget    bool    // false = even the pick exceeds budget (caller decides reject vs proceed)
	Reason        string  // human-readable why
}

// Degrader walks per-family degradation ladders (expensive → cheap). A
// model is only ever degraded WITHIN its own family: switching vendor
// changes behavior and is not an economic decision this package makes.
type Degrader struct {
	pricing PricingSource
	ladders [][]string
}

// defaultLadders: family chains ordered expensive → cheap. Every member
// must exist in the default Estimator's table (a ladder step the pricing
// source can't price would estimate 0 and "fit" any budget — silently
// defeating degradation).
var defaultLadders = [][]string{
	{"gpt-4", "gpt-4-turbo", "gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
	{"claude-3-opus", "claude-3-sonnet", "claude-3-haiku"},
	{"gemini-1.5-pro", "gemini-1.5-flash"},
}

// NewDegrader builds a degrader over the given pricing source and the
// default family ladders.
func NewDegrader(pricing PricingSource) *Degrader {
	if pricing == nil {
		pricing = NewEstimator()
	}
	return &Degrader{pricing: pricing, ladders: defaultLadders}
}

// NewDegraderWithLadders overrides the ladders (caller-owned chains,
// expensive → cheap; custom pricing tables usually need this).
func NewDegraderWithLadders(pricing PricingSource, ladders [][]string) *Degrader {
	if pricing == nil {
		pricing = NewEstimator()
	}
	return &Degrader{pricing: pricing, ladders: ladders}
}

// Select picks the model for one call: the requested model when it fits
// the budget (or no budget / unknown pricing), else the first cheaper
// family member that fits, else the family's cheapest with FitsBudget
// false so the caller can decide reject-vs-proceed.
func (d *Degrader) Select(requested string, inputTokens, outputTokens int, budgetUSD float64) Decision {
	dec := Decision{Requested: requested, Model: requested}
	dec.EstimatedCost = d.pricing.Estimate(requested, inputTokens, outputTokens)

	if budgetUSD <= 0 {
		dec.FitsBudget = true
		dec.Reason = "no budget constraint"
		return dec
	}
	// Unknown pricing estimates 0 — the same "unknown price ≠ over budget"
	// rule the agent side's budget gate enforces.
	if dec.EstimatedCost <= budgetUSD {
		dec.FitsBudget = true
		if dec.EstimatedCost == 0 {
			dec.Reason = "pricing unknown — passed through (unknown ≠ over budget)"
		} else {
			dec.Reason = "requested model fits budget"
		}
		return dec
	}

	// Over budget: walk the family ladder DOWN from the requested model.
	ladder, pos := d.ladderFor(requested)
	if ladder == nil {
		dec.FitsBudget = false
		dec.Reason = "over budget and no degradation ladder for this model"
		return dec
	}
	if pos == len(ladder)-1 {
		// Already the family's cheapest — nothing to degrade to.
		dec.FitsBudget = false
		dec.Reason = "over budget and already the family's cheapest model"
		return dec
	}
	for _, m := range ladder[pos+1:] {
		cost := d.pricing.Estimate(m, inputTokens, outputTokens)
		if cost <= budgetUSD {
			dec.Model = m
			dec.EstimatedCost = cost
			dec.Degraded = true
			dec.FitsBudget = true
			dec.Reason = "degraded within family: requested model exceeds budget"
			return dec
		}
	}
	// No family member fits: return the cheapest so the caller can decide
	// reject-vs-proceed with an honest price attached.
	cheapest := ladder[len(ladder)-1]
	dec.Model = cheapest
	dec.EstimatedCost = d.pricing.Estimate(cheapest, inputTokens, outputTokens)
	dec.Degraded = true
	dec.FitsBudget = false
	dec.Reason = "over budget and no family member fits — cheapest returned for caller decision"
	return dec
}

// ladderFor locates the requested model's family ladder and its position
// in it.
func (d *Degrader) ladderFor(model string) ([]string, int) {
	for _, ladder := range d.ladders {
		for i, m := range ladder {
			if m == model {
				return ladder, i
			}
		}
	}
	return nil, -1
}
