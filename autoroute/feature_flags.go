package autoroute

import (
	"context"
	"os"
	"strconv"
)

// FeatureFlags controls selective rollout of the V2 autoroute path.
type FeatureFlags struct {
	// UseSimplifiedScoring enables the 2-dim score (intent/price + correction).
	UseSimplifiedScoring bool

	// UseHotTop3Pool enables seeding candidates from the 48h hot top-3 canonicals.
	UseHotTop3Pool bool

	// UseCacheRevalidation enables live availability revalidation for session cache hits.
	UseCacheRevalidation bool

	// Use48hFallback enables the 48h fallback candidate selection.
	Use48hFallback bool

	// UseChannelQualityRouting enables the 4-dim score
	// (intent 0.4 + price 0.2 + channel quality 0.3 + reliability 0.1)
	// and the preferred/fallback pool stratification.
	//
	// Business rule (CHANNEL_QUALITY_ROUTING_DESIGN.md):
	//   "Reliable resources (e.g. Minimax official) get prioritised;
	//    free+unreliable (e.g. NVIDIA NIM) should be skipped while
	//    the main channel is not full, unless no errors occur."
	//
	// When enabled:
	//   - Candidates split into preferred (ChannelQuality >= 50)
	//     and fallback (< 50) pools.
	//   - Fallback candidates get composite *= 0.5 demotion when
	//     the preferred pool has free slots.
	//   - Demotion relaxes to 0.85 when preferred pool is fully
	//     saturated (PressureRatio >= 0.95 across all preferred).
	UseChannelQualityRouting bool

	// EnableV2Logic is the umbrella switch. When true, all V2 features are enabled.
	EnableV2Logic bool
}

// DefaultFeatureFlags returns the default flags. New behavior is off by default.
func DefaultFeatureFlags() *FeatureFlags {
	return &FeatureFlags{
		UseSimplifiedScoring:     false,
		UseHotTop3Pool:           false,
		UseCacheRevalidation:     false,
		Use48hFallback:           false,
		UseChannelQualityRouting: false,
		EnableV2Logic:            false,
	}
}

// LoadFeatureFlagsFromEnv loads feature flags from environment variables.
func LoadFeatureFlagsFromEnv() *FeatureFlags {
	flags := &FeatureFlags{
		UseSimplifiedScoring:     getEnvBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:           getEnvBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation:     getEnvBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:           getEnvBool("AUTO_USE_48H_FALLBACK", false),
		UseChannelQualityRouting: getEnvBool("AUTO_USE_CHANNEL_QUALITY_ROUTING", false),
		EnableV2Logic:            getEnvBool("AUTO_ENABLE_V2", false),
	}

	if flags.EnableV2Logic {
		flags.UseSimplifiedScoring = true
		flags.UseHotTop3Pool = true
		flags.UseCacheRevalidation = true
		flags.Use48hFallback = true
		// NOTE: UseChannelQualityRouting is NOT auto-enabled by V2 umbrella.
		// The 4-dim formula is a separate experiment; enable explicitly.
	}

	return flags
}

func getEnvBool(key string, defaultValue bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return defaultValue
	}
	return b
}

var globalFeatureFlags *FeatureFlags

// InitFeatureFlags initializes the global feature flags instance.
func InitFeatureFlags() {
	globalFeatureFlags = LoadFeatureFlagsFromEnv()
}

// GetFeatureFlags returns the global feature flags.
func GetFeatureFlags() *FeatureFlags {
	if globalFeatureFlags == nil {
		return DefaultFeatureFlags()
	}
	return globalFeatureFlags
}

// DecideWithFeatureFlags routes requests based on enabled sub-features.
// If no V2 sub-feature is enabled, it falls back to the legacy path.
func (d *Decider) DecideWithFeatureFlags(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	flags := GetFeatureFlags()
	if !flags.UseSimplifiedScoring && !flags.UseHotTop3Pool && !flags.UseCacheRevalidation && !flags.Use48hFallback && !flags.UseChannelQualityRouting {
		return d.Decide(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	}
	return d.DecideV2(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
}
