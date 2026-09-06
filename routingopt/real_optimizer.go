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

	// ml is the optional P2.5 ONNX re-ranker (nil = disabled; all ML paths
	// short-circuit and RecommendModel output equals the rule-engine order).
	ml *MLReranker
	// ab optionally splits traffic: treatment gets the optimizer, control
	// gets baseline routing (nil = 100% treatment, pre-A/B behaviour).
	ab *ABGate
	// confidence adjusts classification confidence from historical per-task
	// accuracy (PostClassify hook).
	confidence *ConfidenceAdjuster
}

// WithMLReranker attaches the P2.5 ONNX ML re-ranker. Returns the receiver
// so it can be chained after NewRealOptimizer. A nil reranker is a no-op.
func (o *RealOptimizer) WithMLReranker(r *MLReranker) *RealOptimizer {
	o.ml = r
	return o
}

// WithABGate attaches the traffic-split gate (A/B testing). nil = always
// treatment. Implements the ABTestEnabled/ABTestPercentage flags semantics.
func (o *RealOptimizer) WithABGate(g *ABGate) *RealOptimizer {
	o.ab = g
	return o
}

// EvaluateAB reports whether this request is in the treatment group.
// Part of the optional A/B contract the autoroute bridge type-asserts on.
func (o *RealOptimizer) EvaluateAB(sessionID string, apiKeyID int) bool {
	if o.ab == nil {
		return true
	}
	return o.ab.Treatment(sessionID, apiKeyID)
}

// MLDiagnostics renders the P2.5 ML re-ranker state for the admin endpoint
// (nil when ML is disabled). Read-only and allocation-light enough for
// on-demand admin polling.
func (o *RealOptimizer) MLDiagnostics() map[string]any {
	if o.ml == nil {
		return nil
	}
	diag := map[string]any{
		"enabled": o.ml.Enabled(),
		"stats":   o.ml.Stats.Snapshot(),
		"ab_test": o.ab.Snapshot(),
	}
	if sel := o.ml.Selector(); sel != nil {
		if m := sel.Manifest(); m != nil {
			diag["manifest"] = map[string]any{
				"schema_version": m.SchemaVersion,
				"model_file":     m.ModelFile,
				"label_classes":  m.LabelClasses,
				"num_inputs":     m.NumInputs(),
			}
		}
	}
	return diag
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
		confidence:  NewConfidenceAdjuster(pool),
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

// PostClassify adjusts confidence after classification using historical
// per-task-type routing accuracy (learning from runtime behaviour):
//   - 低准确率任务类型: 下调置信度 → 更早触发 LLM fallback 重分类
//   - 高准确率任务类型: 小幅上调 → 保持 heuristic 快路径
//   - 样本不足或读取失败: 原样返回（baseline 行为）
func (o *RealOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	return o.confidence.PostClassify(ctx, taskType, confidence)
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

	out, err := o.recommender.Recommend(ctx, cands, routingCtx)
	if err != nil {
		return nil, err
	}
	// P2.5: apply the ONNX ML re-ranker last so it adjusts (never replaces)
	// the multi-objective rule-engine order. No-op when disabled/unavailable.
	if o.ml.Enabled() && routingCtx.Features != nil {
		out, _ = o.ml.Rerank(ctx, out, *routingCtx.Features)
	}
	return out, nil
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
