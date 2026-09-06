// Package routingopt implements the P2.2 routing optimization plugin.
//
// The plugin enhances AUTO model routing with:
//   - User affinity learning (historical task type preferences)
//   - Session mode detection (IDE/CLI/Web)
//   - Multi-objective optimization (quality/cost/latency/availability)
//   - ε-greedy exploration (5% exploration rate)
//   - Human annotation feedback integration (P2.1 data with 2x weight)
//   - Online parameter learning (adaptive threshold tuning)
//
// The plugin is injected into autoroute.Decider via SetOptimizer() and is
// disabled by default (ROUTING_OPT_ENABLED=false). When disabled, the Decider
// operates unchanged (optimizer field remains nil, all hooks short-circuit).
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
package routingopt

import (
	"context"
	"time"
)

// RoutingOptimizer is the core plugin interface for P2.2 routing optimization.
// Injected into autoroute.Decider via SetOptimizer(). All methods are called
// within the Decider.Decide() hot path, so implementations must be fast
// (target: P99 < 10ms per method) and never block on slow I/O.
//
// Error handling: plugin errors do not block the routing pipeline. The Decider
// logs warnings and falls back to the baseline behavior (signals/confidence/
// candidates unchanged).
//
// Note: To avoid circular import (autoroute → routingopt → autoroute), all
// parameters and return types use interface{} instead of concrete autoroute types.
// Real implementations should type-assert to the expected types:
//   - signals: *autoroute.ClassificationSignals
//   - taskType: autoroute.TaskType (string)
//   - candidates: []ModelCandidate
type RoutingOptimizer interface {
	// PreClassify enriches classification signals before the heuristic classifier runs.
	// Returns EnhancedSignals containing the original signals plus optional enhancements:
	//   - UserAffinities: learned task type distribution from user history
	//   - SessionMode: IDE/CLI/Web detection from User-Agent
	//   - TimeContext: peak/off-peak hour indicator
	//
	// Parameter signals: *autoroute.ClassificationSignals
	// Returns: *EnhancedSignals (with GetOriginal() method), error
	PreClassify(ctx context.Context, signals interface{}) (interface{}, error)

	// PostClassify adjusts the classifier confidence after classification.
	// Can boost/penalize confidence based on:
	//   - Historical accuracy for this task type
	//   - User correction patterns (from P2.1 human annotations)
	//   - Session drift detection (confidence decay over time)
	//
	// Parameter taskType: autoroute.TaskType (string like "code", "chat")
	// Returns the adjusted confidence [0.0, 1.0]. If PostClassify returns an error,
	// the Decider logs a warning and uses the original confidence.
	PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error)

	// RecommendModel re-ranks or filters the candidate list using multi-objective
	// optimization and exploration strategies:
	//   - Multi-objective scoring: w1*Quality - w2*Cost + w3*Latency + w4*Availability
	//   - Dynamic weight adjustment (profile/time/task type)
	//   - ε-greedy exploration (5% of requests randomly sample from top-5)
	//   - Multi-level fallback chain (pre-compute top-3 for automatic failover)
	//   - Circuit breaker protection (exclude providers with failure rate > 20%)
	//
	// Parameter candidates: []ModelCandidate
	// Parameter context: RoutingContext
	// Returns: []ModelCandidate (optimized), error
	RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error)

	// RecordFeedback asynchronously records routing feedback for learning:
	//   - Request outcome (success/failure)
	//   - Latency and cost metrics
	//   - Human corrections (from P2.1 training_human_annotations)
	//
	// Persists to routing_feedback_log table for 5-minute aggregation into
	// routing_optimization_metrics. This method is fire-and-forget: errors are
	// logged but do not affect the request path.
	//
	// Parameter feedback: RoutingFeedback
	RecordFeedback(ctx context.Context, feedback interface{}) error

	// GetStats returns current optimizer statistics for admin API:
	//   - Overall accuracy (weighted: human annotations × 2)
	//   - Parameter version and last update timestamp
	//   - Human annotation utilization rate
	//
	// Used by GET /api/admin/routing-opt/stats (Day 11 implementation).
	// Returns: *OptimizerStats, error
	GetStats(ctx context.Context) (interface{}, error)
}

// EnhancedSignals wraps the original classification signals with plugin-computed enhancements.
//
// To avoid circular import, Original is interface{} instead of *autoroute.ClassificationSignals.
// Real implementations should type-assert: signals.(*autoroute.ClassificationSignals)
type EnhancedSignals struct {
	// Original signals (required, extracted by Decider)
	// Type: *autoroute.ClassificationSignals
	Original interface{}

	// UserAffinities: learned task type distribution [0.0, 1.0] from user's
	// last 100 requests. Cached in Redis (TTL: 1 hour, refreshed on cache miss).
	// Empty map = no historical data or cache miss.
	// Key type: string (autoroute.TaskType values: "code", "chat", "reasoning", etc.)
	UserAffinities map[string]float64

	// SessionMode: detected from User-Agent header
	//   "ide"     : Cursor, Claude Code, Windsurf, VSCode extensions
	//   "cli"     : curl, httpie, custom scripts
	//   "web"     : browser-based clients
	//   "unknown" : unrecognized or missing User-Agent
	SessionMode string

	// TimeContext: current request time characteristics
	TimeContext TimeContext
}

// GetOriginal returns the original signals for Decider extraction.
// Implements the interface expected by autoroute.Decider PreClassify hook.
func (e *EnhancedSignals) GetOriginal() interface{} {
	return e.Original
}

// ModelCandidate wraps a candidate model for multi-objective scoring.
// Extracted from autoroute.ScoredCandidate by the plugin.
type ModelCandidate struct {
	CanonicalName string  // e.g. "claude-sonnet-4"
	CredentialID  int     // binding ID
	RawModel      string  // provider-specific model name
	Score         float64 // composite score from Index.Recommend()
	Provider      string  // "openai", "anthropic", etc.

	// Multi-objective dimensions (used by ModelRecommender):
	Cost         float64 // normalized [0, 1], lower is better
	Latency      float64 // normalized [0, 1], lower is better
	Availability float64 // success rate [0, 1], higher is better
}

// RoutingContext provides request-scoped metadata for RecommendModel.
//
// To avoid circular import, TaskType and Profile are strings instead of autoroute types.
type RoutingContext struct {
	TaskType    string // autoroute.TaskType: "code", "chat", "reasoning", etc.
	Profile     string // autoroute.Profile: "smart", "speed_first", "quality_first", "cost_first"
	UserID      int    // API key ID (0 = unauthenticated)
	SessionID   string // X-Gw-Session-Id (empty = no session)
	TimeContext TimeContext
}

// TimeContext captures time-of-day and day-of-week for dynamic weight adjustment.
type TimeContext struct {
	IsPeakHour bool // 9am-6pm Mon-Fri in user's timezone
	Hour       int  // 0-23 (UTC)
	Weekday    int  // 0=Sunday, 1=Monday, ..., 6=Saturday
}

// RoutingFeedback records the outcome of a routing decision for learning.
//
// To avoid circular import, TaskType is string instead of autoroute.TaskType.
type RoutingFeedback struct {
	RequestID         string // unique request identifier
	TaskType          string // autoroute.TaskType: classified task type
	PredictedProvider string // plugin's recommended provider
	ActualProvider    string // actually used provider (may differ due to failover)
	IsSuccess         bool   // request succeeded (2xx response)
	Latency           time.Duration
	Cost              float64 // request cost in USD (computed from token usage)

	// HumanCorrection: optional correction from P2.1 training_human_annotations.
	// When a human annotator marks the AUTO prediction as incorrect, this field
	// contains the correct provider. The feedback is weighted ×2 in accuracy
	// calculations (ground truth).
	HumanCorrection *string
}

// OptimizerStats summarizes the plugin's performance for admin API.
type OptimizerStats struct {
	OverallAccuracy      float64   // weighted accuracy (human annotations × 2)
	ParameterVersion     int       // optimization_state.id
	LastUpdated          time.Time // optimization_state.created_at
	HumanAnnotationsUsed int       // count of P2.1 annotations integrated
}
