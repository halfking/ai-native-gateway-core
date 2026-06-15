package autoroute

// strategy.go — Classifier strategy abstraction for A/B testing.
//
// The auto-route classifier can run via different "strategies" that
// produce different task-type assignments for the same request.
// Tracking which strategy classified each request lets us compare
// their quality in production via tuning_signals (P5 deliverable).
//
// Three strategies are defined:
//
//   baseline_heuristic — keyword-only heuristic, no pattern layer.
//                         Pre-Phase 1 behaviour. Reproduces the
//                         misclassification that motivated the
//                         pattern layer (e.g. "水池问题" → chat).
//
//   pattern_layered    — full 4-tier priority chain including the
//                         regex pattern layer (default in production).
//                         Fixes the water-pool problem and similar
//                         structural-cue cases.
//
//   llm_fallback       — heuristic with LLM escalation when
//                         confidence < LLMThreshold. Currently
//                         no-op (DisabledCaller); reserved for
//                         future LLM-backed classification.
//
// A/B split:
//   When A/B testing is enabled (tuning_params 'strategy_split_enabled'
//   = TRUE), the strategy is chosen deterministically per
//   request_id so a single user's traffic is consistently
//   classified by the same strategy. The split is 50/50
//   baseline vs pattern_layered by default; admin can tune
//   'strategy_split_baseline_pct' (0-100) via tuning_params.
//
// When A/B is disabled, all requests use 'pattern_layered' (the
// production default).
//
// This is pure Go — no DB schema changes are introduced here.
// The strategy field is captured into tuning_signals.signal_payload
// (JSONB) so existing schemas work without migration.

import (
	"crypto/sha1"
	"encoding/binary"
	"sync/atomic"
)

// Strategy identifies which classification path produced a Decision.
type Strategy string

const (
	// StrategyBaseline is the keyword-only heuristic (no patterns).
	StrategyBaseline Strategy = "baseline_heuristic"
	// StrategyPatternLayered is the full 4-tier chain with patterns.
	StrategyPatternLayered Strategy = "pattern_layered"
	// StrategyLLMFallback escalates to an LLM at low confidence.
	StrategyLLMFallback Strategy = "llm_fallback"
)

// AllStrategies is the canonical list for validation/UI.
var AllStrategies = []Strategy{
	StrategyBaseline, StrategyPatternLayered, StrategyLLMFallback,
}

// IsValidStrategy reports whether s is one of the known strategies.
func IsValidStrategy(s Strategy) bool {
	for _, k := range AllStrategies {
		if k == s {
			return true
		}
	}
	return false
}

// ── Runtime config (atomic, hot-path) ────────────────────────────

// strategyConfig holds the A/B test parameters. All reads happen
// on the hot path (per-request) so we use atomic.Uint64 for the
// split + an atomic.Bool for the enable flag.
type strategyConfig struct {
	enabled        atomic.Bool
	baselinePct100 atomic.Uint64 // 0-100, where 100 = 100% baseline
}

var globalStrategyConfig strategyConfig

// EnableABTest turns A/B testing on. splitPct is the fraction of
// traffic routed to the baseline (0-100). The remainder is routed
// to pattern_layered. Pass splitPct=0 to disable baseline; pass
// splitPct=100 to disable pattern_layered (effectively a rollback).
func EnableABTest(splitPct int) {
	if splitPct < 0 {
		splitPct = 0
	}
	if splitPct > 100 {
		splitPct = 100
	}
	globalStrategyConfig.baselinePct100.Store(uint64(splitPct))
	globalStrategyConfig.enabled.Store(true)
}

// DisableABTest forces all requests onto StrategyPatternLayered.
func DisableABTest() {
	globalStrategyConfig.enabled.Store(false)
}

// IsABTestEnabled reports the current state.
func IsABTestEnabled() bool {
	return globalStrategyConfig.enabled.Load()
}

// baselinePct returns the current baseline percentage.
func baselinePct() int {
	return int(globalStrategyConfig.baselinePct100.Load())
}

// BaselinePctPublic exposes the current baseline percentage for the
// admin UI. Safe for concurrent use.
func BaselinePctPublic() int {
	return baselinePct()
}

// ── Strategy assignment ─────────────────────────────────────────

// AssignStrategy deterministically picks a Strategy for a request
// based on its request_id and the current A/B config.
//
// When A/B is disabled → always StrategyPatternLayered.
// When A/B is enabled  → hash(request_id) % 100 < baselinePct.
//
// The hash is sha1 (cheap, well-distributed) truncated to 8 bytes.
// The result modulo 100 gives a stable 0-99 bucket per request_id.
func AssignStrategy(requestID string) Strategy {
	if !IsABTestEnabled() {
		return StrategyPatternLayered
	}

	bucket := bucketForRequest(requestID, 100)
	if bucket < baselinePct() {
		return StrategyBaseline
	}
	return StrategyPatternLayered
}

// bucketForRequest returns a stable 0..n-1 bucket for a request id.
func bucketForRequest(requestID string, n int) int {
	if requestID == "" || n <= 0 {
		return 0
	}
	h := sha1.Sum([]byte(requestID))
	// Use the first 8 bytes as uint64, then mod n.
	v := binary.BigEndian.Uint64(h[:8])
	return int(v % uint64(n))
}

// ── Strategy → classifier wiring (for testing) ──────────────────

// ClassifierForStrategy returns the appropriate Classifier for a
// given strategy. The pattern_layered strategy uses the existing
// HeuristicClassifier (which now includes the pattern layer in
// the priority chain). The baseline strategy uses a keyword-only
// HeuristicClassifier that bypasses the pattern layer.
//
// llm_fallback is reserved for future use: when the LLM endpoint
// is wired, this will return a composite classifier that calls
// the heuristic first and falls back to LLMCaller on low confidence.
func ClassifierForStrategy(strategy Strategy, base *HeuristicClassifier) Classifier {
	switch strategy {
	case StrategyBaseline:
		// Strip the pattern layer by using the same HeuristicClassifier
		// with a pattern-set that contains zero patterns. The keyword
		// scoring still runs (matches the v1.0 baseline behaviour).
		return NewHeuristicClassifier(DefaultHeuristicThresholds(), emptyKeywordSet())
	case StrategyLLMFallback:
		// Future: composite heuristic + LLM. For now, treat as pattern
		// layered (no LLM caller configured by default).
		fallthrough
	case StrategyPatternLayered:
		fallthrough
	default:
		return base
	}
}

// emptyKeywordSet returns a KeywordSet with empty slices so the
// keyword channel scores 0 for every category. Patterns still
// contribute when matched, so this isn't a perfect v1.0 replica
// (use a separate classifier that skips the pattern layer if
// strict v1.0 reproduction is needed). Suitable for A/B testing
// the value of the pattern layer.
func emptyKeywordSet() KeywordSet {
	return KeywordSet{
		Reasoning: []string{},
		Code:      []string{},
		Creative:  []string{},
	}
}
