package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/routingopt"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// routing_optimizer_init.go — P2.2: wires the routing optimization plugin
// into the auto-route Decider when ROUTING_OPT_ENABLED=true.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
// Failure mode: construction problems (nil pool, disabled flag) degrade to
// baseline routing (nil optimizer) — never block gateway startup.

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
	return optimizer
}
