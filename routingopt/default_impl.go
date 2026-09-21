package routingopt

import (
	"context"
	"time"
)

// DefaultOptimizer is a no-op implementation of RoutingOptimizer.
//
// All methods pass through inputs unchanged, making the plugin transparent:
//   - PreClassify wraps the original signals without enhancements
//   - PostClassify returns the original confidence
//   - RecommendModel returns the original candidate list
//   - RecordFeedback discards feedback (no persistence)
//   - GetStats returns empty statistics
//
// This implementation is used in Week 1 to verify the plugin framework without
// changing routing behavior. Real optimization logic (user affinity, multi-objective
// scoring, human annotation integration) is implemented in Week 2.
//
// Usage:
//
//	decider.SetOptimizer(routingopt.NewDefaultOptimizer())
//
// Zero overhead: when the Decider's optimizer field is nil, all plugin hooks
// short-circuit with a single nil check. When set to DefaultOptimizer, the
// overhead is one nil check + one interface method call that immediately returns.
type DefaultOptimizer struct{}

// NewDefaultOptimizer constructs a no-op optimizer.
func NewDefaultOptimizer() *DefaultOptimizer {
	return &DefaultOptimizer{}
}

// PreClassify wraps the input signals without adding enhancements.
// Returns EnhancedSignals{Original: signals} with nil affinities/context.
//
// Parameter signals: interface{} (*autoroute.ClassificationSignals expected)
func (o *DefaultOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	return &EnhancedSignals{
		Original:       signals,
		UserAffinities: nil, // no affinity learning in no-op mode
		SessionMode:    "unknown",
		TimeContext:    TimeContext{},
	}, nil
}

// PostClassify returns the original confidence unchanged.
//
// Parameter taskType: string (autoroute.TaskType like "code", "chat")
func (o *DefaultOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	return confidence, nil
}

// RecommendModel returns the original candidate list unchanged.
// No multi-objective re-ranking, no ε-greedy exploration, no fallback chain.
//
// Parameter candidates: interface{} ([]ModelCandidate expected)
// Parameter context: interface{} (RoutingContext expected)
func (o *DefaultOptimizer) RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error) {
	return candidates, nil
}

// RecordFeedback discards the feedback (no persistence in no-op mode).
// Real implementation (Week 2) persists to routing_feedback_log table.
//
// Parameter feedback: interface{} (RoutingFeedback expected)
func (o *DefaultOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	// no-op: do not persist feedback
	return nil
}

// GetStats returns empty statistics.
// Real implementation (Week 2) computes accuracy from routing_optimization_metrics.
func (o *DefaultOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return &OptimizerStats{
		OverallAccuracy:      0.0,
		ParameterVersion:     0,
		LastUpdated:          time.Time{},
		HumanAnnotationsUsed: 0,
	}, nil
}
