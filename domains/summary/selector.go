// Package summary — V2-P5 (2026-07-24)
// Selector picks the best model for a summary kind (title / summary /
// turn_one_liner) from a tenant-scoped catalog. Score = w1·availability
// + w2·cost-efficiency + w3·context-window + w4·latency-inverse, all
// per-call normalized so the four terms stay in [0, 1] regardless of
// the actual cost / latency spread in the catalog.
//
// Designed to be cheap to call: the catalog is expected to be small
// (≤ a few dozen candidates) and Select does O(n) work over it. There
// is no caching here — callers may memoize Candidate lists upstream
// if they call Select in a hot path.
package summary

import (
	"context"
	"fmt"
	"math"
	"time"
)

// SummaryKind scopes the catalog query. Different kinds may have
// different "best" answers: a title needs only a few hundred tokens of
// context but should be very cheap; a session summary wants a 32k
// window and decent reasoning quality.
type SummaryKind string

const (
	SummaryKindTitle        SummaryKind = "title"
	SummaryKindSummary      SummaryKind = "summary"
	SummaryKindTurnOneLiner SummaryKind = "turn_one_liner"
)

// Candidate is a single (model, cost, latency, availability, ctx) tuple.
// All fields are required; pass zero values if the dimension is unknown,
// but the score will be lower for that dimension.
type Candidate struct {
	Model     string
	Cost      float64       // USD per 1k tokens
	Latency   time.Duration // typical end-to-end latency for a summary call
	Available float64       // [0, 1] — observed availability over a recent window
	Ctx       int           // max context window in tokens
}

// Catalog is the read interface the Selector depends on. It is kept
// abstract so callers can plug in a DB-backed or in-memory implementation.
type Catalog interface {
	Candidates(ctx context.Context, tenantID string, kind SummaryKind) ([]Candidate, error)
}

// Weights control the four score components. All components are summed
// (no softmax); the total weight is not required to equal 1 but values
// near 1 are recommended.
type Weights struct {
	W1Availability   float64
	W2CostEfficiency float64
	W3ContextWindow  float64
	W4LatencyInv     float64
}

// DefaultWeights — 0.35/0.35/0.15/0.15: prefer models that are both
// available and cheap; context and latency are secondary concerns.
func DefaultWeights() Weights {
	return Weights{0.35, 0.35, 0.15, 0.15}
}

// Selector wraps a Catalog + Weights. Construct with NewSelector.
type Selector struct {
	cat Catalog
	w   Weights
}

func NewSelector(cat Catalog, w Weights) *Selector { return &Selector{cat: cat, w: w} }

// Select returns the highest-scoring candidate for the given (tenant,
// kind). Returns an error if the catalog lookup fails or if the
// catalog is empty — callers should treat an empty catalog as
// "no model available" rather than a silent fallback.
func (s *Selector) Select(ctx context.Context, tenantID string, kind SummaryKind) (Candidate, error) {
	cands, err := s.cat.Candidates(ctx, tenantID, kind)
	if err != nil {
		return Candidate{}, err
	}
	if len(cands) == 0 {
		return Candidate{}, fmt.Errorf("summary: no candidates for tenant=%q kind=%q", tenantID, kind)
	}

	var minCost, maxCost = math.Inf(1), 0.0
	var maxLatency time.Duration
	for _, c := range cands {
		if c.Cost < minCost {
			minCost = c.Cost
		}
		if c.Cost > maxCost {
			maxCost = c.Cost
		}
		if c.Latency > maxLatency {
			maxLatency = c.Latency
		}
	}
	costRange := maxCost - minCost
	if costRange == 0 {
		// All candidates have the same cost: cost-efficiency term is
		// 0 (or 1 if minCost is also 0). Use 1 so the term doesn't
		// unfairly penalize the lone tier.
		costRange = 1
	}

	best := cands[0]
	bestScore := math.Inf(-1)
	for _, c := range cands {
		cost := 1.0 - (c.Cost-minCost)/costRange
		if cost < 0 {
			cost = 0
		}
		ctxN := math.Min(1.0, float64(c.Ctx)/32768.0)
		var lat float64
		if maxLatency > 0 {
			lat = 1.0 - float64(c.Latency)/float64(maxLatency)
		}
		if lat < 0 {
			lat = 0
		}
		score := s.w.W1Availability*c.Available +
			s.w.W2CostEfficiency*cost +
			s.w.W3ContextWindow*ctxN +
			s.w.W4LatencyInv*lat
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return best, nil
}
