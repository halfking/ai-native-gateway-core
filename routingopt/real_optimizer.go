package routingopt

import (
	"context"
	"log/slog"
	"strconv"
	"time"

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

	// opts gates each hook per the ROUTING_OPT_* sub-flags and bounds hook
	// latency (2026-09-07 audit P1: flags were logged but never consumed).
	opts Options
}

// WithMLReranker attaches the P2.5 ONNX ML re-ranker. Returns the receiver
// so it can be chained after NewRealOptimizer. A nil reranker is a no-op.
func (o *RealOptimizer) WithMLReranker(r *MLReranker) *RealOptimizer {
	o.ml = r
	return o
}

// =============================================================================
// 装配/关闭桥接（P2.2 Track A 接线用，纯 passthrough，不含业务逻辑）
// =============================================================================

// WithAffinityCache attaches the Redis-backed user-affinity read cache
// (P2.2 Track A) to the enhancer. nil cache = 直查 DB（清除缓存）。
// Returns the receiver for chaining.
func (o *RealOptimizer) WithAffinityCache(c *AffinityCache) *RealOptimizer {
	if o != nil && o.enhancer != nil {
		o.enhancer.SetAffinityCache(c)
	}
	return o
}

// FlushFeedback drains the async feedback batch queue (P2.2 Track B).
// cmd/gateway 优雅关闭在 DB pool 关闭前调用（约 5s 超时 ctx）；同步路径
// / 未接线时是安全 no-op。
func (o *RealOptimizer) FlushFeedback(ctx context.Context) {
	if o == nil {
		return
	}
	o.integrator.Flush(ctx)
}

// FeedbackBatch exposes the async batch writer for Track C counter wiring
// (routingopt.AttachFeedbackCounters(batch.Counters))。batch 未建（同步
// 路径 / nil pool）时返回 nil，调用方需给零值计数器函数兜底。
func (o *RealOptimizer) FeedbackBatch() *FeedbackBatchWriter {
	if o == nil {
		return nil
	}
	return o.integrator.FeedbackBatch()
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
// Hook gates default to the documented flag semantics (all sub-hooks on,
// 10ms hook timeout, no affinity DB query) — see DefaultOptions.
//
// Usage:
//
//	pool := pgxpool.Connect(...)
//	optimizer := routingopt.NewRealOptimizer(pool)
//	decider.SetOptimizer(optimizer)
func NewRealOptimizer(pool *pgxpool.Pool) *RealOptimizer {
	return NewRealOptimizerWithOptions(pool, DefaultOptions())
}

// NewRealOptimizerWithOptions constructs a real optimizer with explicit
// feature-flag gates. cmd/gateway maps settings.GetRoutingOptFlags() onto
// Options so ROUTING_OPT_* sub-flags actually short-circuit their hooks
// (2026-09-07 audit: the flags were logged but never consumed).
func NewRealOptimizerWithOptions(pool *pgxpool.Pool, opts Options) *RealOptimizer {
	enhancer := NewClassificationEnhancer(pool)
	enhancer.loadAffinity = opts.LoadUserAffinity
	recommender := NewModelRecommender(pool)
	integrator := NewFeedbackIntegrator(pool, enhancer)
	// Track B escape hatch：ControlledSyncFallback=true 时 Integrator 走旧的
	// 同步 INSERT 路径（默认 false=异步批量）。不接线该 Option 就只是死字段。
	integrator.SetControlledSyncFallback(opts.ControlledSyncFallback)
	learner := NewAdaptiveLearner(pool, integrator)

	return &RealOptimizer{
		enhancer:    enhancer,
		recommender: recommender,
		integrator:  integrator,
		learner:     learner,
		confidence:  NewConfidenceAdjuster(pool),
		opts:        opts,
	}
}

// PreClassify enriches classification signals before classification.
// Delegates to ClassificationEnhancer.Enhance().
//
// Parameter signals: interface{} (*autoroute.ClassificationSignals expected)
// Returns: *EnhancedSignals (with GetOriginal() method)
func (o *RealOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	defer recordHookLatency("preclassify", time.Now()) // P2.2 Track C: hook latency (short-circuits included)
	if !o.opts.EnableClassificationEnhancement {
		// Flag off: return the original signals untouched. decision.go only
		// unwraps values exposing GetOriginal(), so a plain pass-through
		// keeps the request on baseline signals with zero DB work.
		return signals, nil
	}
	ctx, cancel := o.opts.hookContext(ctx)
	defer cancel()

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
	defer recordHookLatency("postclassify", time.Now()) // P2.2 Track C: hook latency
	return o.confidence.PostClassify(ctx, taskType, confidence)
}

// RecommendModel re-ranks candidates using multi-objective optimization.
// Delegates to ModelRecommender.Recommend().
//
// Parameter candidates: interface{} ([]ModelCandidate expected)
// Parameter context: interface{} (RoutingContext expected)
// Returns: interface{} ([]ModelCandidate)
func (o *RealOptimizer) RecommendModel(ctx context.Context, candidates interface{}, routingContext interface{}) (interface{}, error) {
	defer recordHookLatency("recommend", time.Now()) // P2.2 Track C: hook latency
	if !o.opts.EnableModelRecommendation {
		// Flag off: baseline ranking passes through untouched.
		return candidates, nil
	}

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

	ctx, cancel := o.opts.hookContext(ctx)
	defer cancel()
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
	if !o.opts.EnableFeedbackIntegration {
		// Flag off: discard feedback (documented semantics of
		// ROUTING_OPT_FEEDBACK_INTEGRATION=false).
		return nil
	}

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

// RunAdaptiveMaintenance runs one online-learning pass: parameter adaptation
// (checkpoint / hold / anomaly) followed by anomaly detection. Called by the
// cmd/gateway background worker on the ROUTING_OPT_ADAPTIVE_LEARNING cadence;
// errors are logged, never propagated — a failed pass simply defers learning
// to the next tick.
func (o *RealOptimizer) RunAdaptiveMaintenance(ctx context.Context) {
	if err := o.learner.AdaptParameters(ctx); err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "routingopt: adaptive pass failed", "err", err)
		}
		return
	}
	anomalies, err := o.learner.DetectAnomalies(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "routingopt: anomaly detection failed", "err", err)
		}
		return
	}
	for _, a := range anomalies {
		slog.WarnContext(ctx, "routingopt: routing anomaly",
			"type", a.Type, "severity", a.Severity, "description", a.Description,
			"metrics", a.Metrics)
	}
	// P2.2 Track C: 每轮维护后顺手刷新加权准确率 gauge。纯可观测性，
	// best-effort：失败不影响维护流程（ctx 取消时 GetStats 自会报错跳过）。
	if stats, serr := o.learner.GetStats(ctx); serr == nil {
		SetWeightedAccuracy(stats.OverallAccuracy)
	}
}
