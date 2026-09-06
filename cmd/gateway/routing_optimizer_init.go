package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
func buildRoutingOptimizer(pool *pgxpool.Pool) *routingopt.RealOptimizer {
	flags := settings.GetRoutingOptFlags()
	if !flags.Enabled {
		slog.Info("autoroute: routing optimizer disabled (ROUTING_OPT_ENABLED=false)")
		return nil
	}
	if pool == nil {
		slog.Warn("autoroute: routing optimizer enabled but no DB pool, running baseline")
		return nil
	}
	// Map env flags onto optimizer gates — before 2026-09-07 the sub-flags
	// were only logged; the hooks ran unconditionally regardless of them.
	hookTimeout := time.Duration(flags.MaxPluginLatencyMs) * time.Millisecond
	if hookTimeout <= 0 || flags.MaxPluginLatencyMs > 100 {
		hookTimeout = 10 * time.Millisecond // documented default / range clamp
	}
	opts := routingopt.Options{
		EnableClassificationEnhancement: flags.EnableClassificationEnhancement,
		EnableModelRecommendation:       flags.EnableModelRecommendation,
		EnableFeedbackIntegration:       flags.EnableFeedbackIntegration,
		LoadUserAffinity:                false, // EnhancedSignals 无下游消费者前保持关闭（Week 2）
		HookTimeout:                     hookTimeout,
	}
	optimizer := routingopt.NewRealOptimizerWithOptions(pool, opts)
	slog.Info("autoroute: routing optimizer enabled",
		"classification_enhancement", flags.EnableClassificationEnhancement,
		"model_recommendation", flags.EnableModelRecommendation,
		"feedback_integration", flags.EnableFeedbackIntegration,
		"adaptive_learning", flags.EnableAdaptiveLearning,
		"max_plugin_latency_ms", flags.MaxPluginLatencyMs,
		"exploration_rate", flags.ExplorationRate)

	// P2.5: optional ONNX ML re-ranker. Any failure degrades to the
	// rule-engine order with a Warn — startup and routing continue.
	attachMLReranker(optimizer)

	// A/B testing (flags existed since P2.2; wired in P2.5): treatment
	// requests use the optimizer, control requests get baseline routing.
	if flags.ABTestEnabled {
		optimizer.WithABGate(routingopt.NewABGate(flags.ABTestPercentage))
		slog.Info("routingopt: A/B test enabled",
			"treatment_pct", flags.ABTestPercentage*100)
	}

	setRoutingOptML(optimizer)

	// P2.2 adaptive learning: background parameter checkpoint / anomaly
	// detection loop, gated by ROUTING_OPT_ADAPTIVE_LEARNING (default false).
	startAdaptiveMaintenance(optimizer, flags.EnableAdaptiveLearning)
	return optimizer
}

// adaptiveMaintenanceInterval is the cadence of the online-learning loop
// (design doc §2.4: background worker every 5 minutes).
const adaptiveMaintenanceInterval = 5 * time.Minute

// startAdaptiveMaintenance launches the periodic AdaptParameters /
// DetectAnomalies loop when the flag is on. The first pass is deliberately
// deferred by one full interval so gateway startup never competes with the
// boot-time DB migration/refresh window; the process lifetime is the loop's
// lifetime (same semantics as the other bg workers).
func startAdaptiveMaintenance(optimizer *routingopt.RealOptimizer, enabled bool) {
	if !enabled {
		slog.Info("routingopt: adaptive learning disabled (ROUTING_OPT_ADAPTIVE_LEARNING=false)")
		return
	}
	go func() {
		ticker := time.NewTicker(adaptiveMaintenanceInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), adaptiveMaintenanceInterval)
			optimizer.RunAdaptiveMaintenance(ctx)
			cancel()
		}
	}()
	slog.Info("routingopt: adaptive learning enabled",
		"interval", adaptiveMaintenanceInterval)
}

// attachMLReranker builds and attaches the P2.5 ONNX re-ranker when
// ROUTING_ML_ENABLED=true. Returns the optimizer unchanged on any failure.
func attachMLReranker(optimizer *routingopt.RealOptimizer) {
	mlFlags := settings.GetRoutingMLFlags()
	if !mlFlags.Enabled {
		slog.Info("routingopt: ML re-ranker disabled (ROUTING_ML_ENABLED=false)")
		return
	}
	if mlFlags.ManifestPath == "" {
		slog.Warn("routingopt: ML enabled but ROUTING_ML_MANIFEST_PATH unset, skipping")
		return
	}
	cfg := routingopt.MLSelectorConfig{
		ManifestPath:      mlFlags.ManifestPath,
		ORTLibraryPath:    mlFlags.ORTLibraryPath,
		IntraOpNumThreads: mlFlags.IntraOpNumThreads,
	}
	selector, err := routingopt.NewMLSelector(context.Background(), cfg)
	if err != nil {
		slog.Warn("routingopt: ML re-ranker unavailable, rule-engine ordering stays active",
			"err", err)
		return
	}
	reranker := routingopt.NewMLReranker(selector, mlFlags.MinConfidence)
	optimizer.WithMLReranker(reranker)
	labels := selector.Manifest().LabelClasses
	slog.Info("routingopt: ML re-ranker enabled",
		"manifest", mlFlags.ManifestPath,
		"labels", labels,
		"min_confidence", mlFlags.MinConfidence)

	// P2.5: model hot reload — poll manifest+model, atomically swap sessions.
	if mlFlags.ReloadSeconds > 0 {
		reranker.StartAutoReload(context.Background(), cfg,
			time.Duration(mlFlags.ReloadSeconds)*time.Second)
	}
}
