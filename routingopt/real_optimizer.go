package routingopt

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// RealOptimizer: 集成所有4个模块的完整优化器
// =============================================================================

// RealOptimizer implements RoutingOptimizer by integrating all 4 core modules:
//   - ClassificationEnhancer: PreClassify feature enrichment
//   - ModelRecommender: RecommendModel multi-objective optimization
//   - FeedbackIntegrator: RecordFeedback logging and human annotation integration
//   - AdaptiveLearner: GetStats performance reporting
//
// This optimizer replaces DefaultOptimizer when ROUTING_OPT_ENABLED=true.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
type RealOptimizer struct {
	enhancer    *ClassificationEnhancer
	recommender *ModelRecommender
	integrator  *FeedbackIntegrator
	learner     *AdaptiveLearner
}

// NewRealOptimizer constructs a real optimizer with all modules wired.
//
// Usage:
//
//	pool := pgxpool.Connect(...)
//	optimizer := routingopt.NewRealOptimizer(pool)
//	decider.SetOptimizer(optimizer)
func NewRealOptimizer(pool *pgxpool.Pool) *RealOptimizer {
	enhancer := NewClassificationEnhancer(pool)
	recommender := NewModelRecommender(pool)
	integrator := NewFeedbackIntegrator(pool, enhancer)
	learner := NewAdaptiveLearner(pool, integrator)

	return &RealOptimizer{
		enhancer:    enhancer,
		recommender: recommender,
		integrator:  integrator,
		learner:     learner,
	}
}

// PreClassify enriches classification signals before classification.
// Delegates to ClassificationEnhancer.Enhance().
//
// Parameter signals: interface{} (*autoroute.ClassificationSignals expected)
// Returns: *EnhancedSignals (with GetOriginal() method)
func (o *RealOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	// Read per-request identity injected by autoroute.Decider (WithRequestMeta).
	meta := RequestMetaFrom(ctx)
	userID := ""
	if meta.UserID > 0 {
		userID = strconv.Itoa(meta.UserID)
	}
	return o.enhancer.Enhance(ctx, signals, userID, meta.ClientType)
}

// PostClassify adjusts confidence after classification.
//
// Week 1: no-op (返回原始置信度)
// Week 2: 根据历史准确率调整置信度
//   - 高准确率任务类型: 提升置信度
//   - 低准确率任务类型: 降低置信度
//   - 人工纠正过的任务类型: 大幅调整
func (o *RealOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	// Week 1: pass through unchanged
	// Week 2: adjust based on historical accuracy
	return confidence, nil
}

// RecommendModel re-ranks candidates using multi-objective optimization.
// Delegates to ModelRecommender.Recommend().
//
// Parameter candidates: interface{} ([]ModelCandidate expected)
// Parameter context: interface{} (RoutingContext expected)
// Returns: interface{} ([]ModelCandidate)
func (o *RealOptimizer) RecommendModel(ctx context.Context, candidates interface{}, routingContext interface{}) (interface{}, error) {
	// Type-assert candidates
	cands, ok := candidates.([]ModelCandidate)
	if !ok {
		// Invalid type: return unchanged
		return candidates, nil
	}

	// Type-assert routing context
	routingCtx, ok := routingContext.(RoutingContext)
	if !ok {
		// Invalid type: use empty context
		routingCtx = RoutingContext{}
	}

	return o.recommender.Recommend(ctx, cands, routingCtx)
}

// RecordFeedback records routing feedback asynchronously.
// Delegates to FeedbackIntegrator.RecordFeedback().
//
// Parameter feedback: interface{} (RoutingFeedback expected)
func (o *RealOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	// Type-assert feedback
	fb, ok := feedback.(RoutingFeedback)
	if !ok {
		// Invalid type: ignore
		return nil
	}

	return o.integrator.RecordFeedback(ctx, fb)
}

// GetStats returns optimizer performance statistics.
// Delegates to AdaptiveLearner.GetStats().
//
// Returns: interface{} (*OptimizerStats)
func (o *RealOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return o.learner.GetStats(ctx)
}
