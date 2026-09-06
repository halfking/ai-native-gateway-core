package settings

import (
	"os"
	"strconv"
	"strings"
)

// RoutingOptFeatureFlags controls P2.2 routing optimization plugin behavior.
//
// All flags default to false/disabled to ensure zero impact on existing routing
// until explicitly enabled. The plugin is injected into autoroute.Decider via
// SetOptimizer() when ROUTING_OPT_ENABLED=true.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §6
type RoutingOptFeatureFlags struct {
	// Enabled is the master switch for the P2.2 plugin. When false, Decider.optimizer
	// remains nil and all plugin hooks short-circuit (zero overhead).
	//
	// Default: false (plugin disabled)
	Enabled bool `env:"ROUTING_OPT_ENABLED" default:"false"`

	// EnableClassificationEnhancement enables PreClassify hook (user affinity,
	// session mode detection, time context). When false, PreClassify returns
	// unmodified signals.
	//
	// Default: true (enabled when plugin is enabled)
	// Requires: Enabled=true
	EnableClassificationEnhancement bool `env:"ROUTING_OPT_CLASSIFICATION_ENHANCEMENT" default:"true"`

	// EnableModelRecommendation enables RecommendModel hook (multi-objective
	// scoring, ε-greedy exploration, fallback chain). When false, RecommendModel
	// returns the original candidate list.
	//
	// Default: true (enabled when plugin is enabled)
	// Requires: Enabled=true
	EnableModelRecommendation bool `env:"ROUTING_OPT_MODEL_RECOMMENDATION" default:"true"`

	// EnableFeedbackIntegration enables RecordFeedback persistence to
	// routing_feedback_log table. When false, feedback is discarded.
	//
	// Default: true (enabled when plugin is enabled)
	// Requires: Enabled=true
	EnableFeedbackIntegration bool `env:"ROUTING_OPT_FEEDBACK_INTEGRATION" default:"true"`

	// EnableAdaptiveLearning enables online parameter optimization (adaptive
	// threshold tuning based on rolling accuracy). Disabled initially to allow
	// Week 1-3 implementation to stabilize before enabling learning.
	//
	// Default: false (disabled even when plugin is enabled)
	// Requires: Enabled=true, EnableFeedbackIntegration=true
	// Recommended: enable in Week 8 after 100% traffic rollout
	EnableAdaptiveLearning bool `env:"ROUTING_OPT_ADAPTIVE_LEARNING" default:"false"`

	// MaxPluginLatencyMs is the timeout for each plugin hook (PreClassify,
	// PostClassify, RecommendModel). Hooks exceeding this timeout are logged
	// and return baseline behavior (no optimization).
	//
	// Default: 10ms (P99 target per design doc)
	// Range: 1-100ms
	MaxPluginLatencyMs int `env:"ROUTING_OPT_MAX_PLUGIN_LATENCY_MS" default:"10"`

	// ExplorationRate is the ε parameter for ε-greedy exploration in RecommendModel.
	// A fraction of requests (0.05 = 5%) randomly sample from the top-5 candidates
	// instead of always choosing the top-1. Exploration ensures the plugin discovers
	// new high-quality models and avoids over-exploiting current favorites.
	//
	// Default: 0.05 (5% exploration, 95% exploitation)
	// Range: 0.0-0.2 (0% = pure exploitation, 20% = high exploration)
	ExplorationRate float64 `env:"ROUTING_OPT_EXPLORATION_RATE" default:"0.05"`

	// ABTestEnabled enables A/B testing mode: a fraction of requests use the plugin
	// (treatment), the rest use baseline routing (control). Metrics are collected
	// for both groups to validate plugin effectiveness before full rollout.
	//
	// Default: false (A/B test disabled, all requests use plugin when Enabled=true)
	// Recommended: enable during Week 5 (production 10% rollout)
	ABTestEnabled bool `env:"ROUTING_OPT_AB_TEST_ENABLED" default:"false"`

	// ABTestPercentage is the fraction of requests assigned to the treatment group
	// (plugin enabled) when ABTestEnabled=true. The remaining requests use baseline
	// routing (plugin hooks disabled).
	//
	// Default: 0.1 (10% treatment, 90% control)
	// Range: 0.01-0.5 (1%-50%)
	// Recommended rollout: 10% → 25% → 50% → 100% (disable A/B test at 100%)
	ABTestPercentage float64 `env:"ROUTING_OPT_AB_TEST_PERCENTAGE" default:"0.1"`
}

// GetRoutingOptFlags returns the routing optimization feature flags, read
// from environment variables (ROUTING_OPT_*) with the documented defaults.
// Invalid values fall back to the default so a typo can never disable the
// baseline safety properties (e.g. exploration-rate clamping happens in the
// recommender regardless).
//
// Usage:
//
//	flags := settings.GetRoutingOptFlags()
//	if flags.Enabled {
//	    decider.SetOptimizer(routingopt.NewRealOptimizer(pool))
//	}
func GetRoutingOptFlags() *RoutingOptFeatureFlags {
	return &RoutingOptFeatureFlags{
		Enabled:                         envBool("ROUTING_OPT_ENABLED", false),
		EnableClassificationEnhancement: envBool("ROUTING_OPT_CLASSIFICATION_ENHANCEMENT", true),
		EnableModelRecommendation:       envBool("ROUTING_OPT_MODEL_RECOMMENDATION", true),
		EnableFeedbackIntegration:       envBool("ROUTING_OPT_FEEDBACK_INTEGRATION", true),
		EnableAdaptiveLearning:          envBool("ROUTING_OPT_ADAPTIVE_LEARNING", false), // disabled until Week 8
		MaxPluginLatencyMs:              envInt("ROUTING_OPT_MAX_PLUGIN_LATENCY_MS", 10),
		ExplorationRate:                 envFloat("ROUTING_OPT_EXPLORATION_RATE", 0.05),
		ABTestEnabled:                   envBool("ROUTING_OPT_AB_TEST_ENABLED", false),
		ABTestPercentage:                envFloat("ROUTING_OPT_AB_TEST_PERCENTAGE", 0.1),
	}
}

// envBool parses a boolean env var; empty or invalid → defaultValue.
func envBool(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return defaultValue
	}
	return v
}

// envInt parses an integer env var; empty or invalid → defaultValue.
func envInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return v
}

// envFloat parses a float env var; empty or invalid → defaultValue.
func envFloat(key string, defaultValue float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return defaultValue
	}
	return v
}
