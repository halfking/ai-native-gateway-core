package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/routingopt"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// routing_optimizer_init.go — P2.2: wires the routing optimization plugin
// into the auto-route Decider when ROUTING_OPT_ENABLED=true.
//
// P2.5: additionally attaches the ONNX ML re-ranker when ROUTING_ML_ENABLED=true.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
//         docs/ml/p2.5-go-onnx-inference.md
// Failure mode: construction problems (nil pool, disabled flag, missing
// model/runtime) degrade to baseline routing — never block gateway startup.

// buildRoutingOptimizer constructs the P2.2 optimizer from feature flags.
// Returns nil when the plugin is disabled or no DB pool is available; the
// caller then simply skips decider.SetOptimizer and routing behaviour is
// byte-identical to the pre-P2.2 baseline.
func buildRoutingOptimizer(pool *pgxpool.Pool) autoroute.RoutingOptimizer {
	flags := settings.GetRoutingOptFlags()
	if !flags.Enabled {
		slog.Info("autoroute: routing optimizer disabled (ROUTING_OPT_ENABLED=false)")
		return nil
	}
	if pool == nil {
		slog.Warn("autoroute: routing optimizer enabled but no DB pool, running baseline")
		return nil
	}
	optimizer := routingopt.NewRealOptimizer(pool)
	slog.Info("autoroute: routing optimizer enabled",
		"classification_enhancement", flags.EnableClassificationEnhancement,
		"model_recommendation", flags.EnableModelRecommendation,
		"feedback_integration", flags.EnableFeedbackIntegration,
		"adaptive_learning", flags.EnableAdaptiveLearning,
		"exploration_rate", flags.ExplorationRate)

	// P2.5: optional ONNX ML re-ranker. Any failure degrades to the
	// rule-engine order with a Warn — startup and routing continue.
	optimizer = attachMLReranker(optimizer)
	return optimizer
}

// attachMLReranker builds and attaches the P2.5 ONNX re-ranker when
// ROUTING_ML_ENABLED=true. Returns the optimizer unchanged on any failure.
func attachMLReranker(optimizer *routingopt.RealOptimizer) *routingopt.RealOptimizer {
	mlFlags := settings.GetRoutingMLFlags()
	if !mlFlags.Enabled {
		slog.Info("routingopt: ML re-ranker disabled (ROUTING_ML_ENABLED=false)")
		return optimizer
	}
	if mlFlags.ManifestPath == "" {
		slog.Warn("routingopt: ML enabled but ROUTING_ML_MANIFEST_PATH unset, skipping")
		return optimizer
	}
	selector, err := routingopt.NewMLSelector(context.Background(), routingopt.MLSelectorConfig{
		ManifestPath:      mlFlags.ManifestPath,
		ORTLibraryPath:    mlFlags.ORTLibraryPath,
		IntraOpNumThreads: mlFlags.IntraOpNumThreads,
	})
	if err != nil {
		slog.Warn("routingopt: ML re-ranker unavailable, rule-engine ordering stays active",
			"err", err)
		return optimizer
	}
	labels := selector.Manifest().LabelClasses
	slog.Info("routingopt: ML re-ranker enabled",
		"manifest", mlFlags.ManifestPath,
		"labels", labels,
		"min_confidence", mlFlags.MinConfidence)
	return optimizer.WithMLReranker(routingopt.NewMLReranker(selector, mlFlags.MinConfidence))
}
