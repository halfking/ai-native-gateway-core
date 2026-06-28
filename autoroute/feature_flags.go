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

// DefaultFeatureFlags returns the default flags.
//
// 注意（2026-06-28 起）：UseChannelQualityRouting 默认开启
// （"全量启动，没有灰度"）。如需在某个部署实例上回退到旧的 2 维公式，
// 显式设置环境变量 AUTO_USE_CHANNEL_QUALITY_ROUTING=false。
//
// 其它 V2 flag 仍保持默认关闭（它们各自独立 opt-in），原因：
//   - 多数 V2 子功能仍在迭代中
//   - UseChannelQualityRouting 是已通过审计的稳定特性
func DefaultFeatureFlags() *FeatureFlags {
	return &FeatureFlags{
		UseSimplifiedScoring: false,
		UseHotTop3Pool:       false,
		UseCacheRevalidation: false,
		Use48hFallback:       false,
		// CHANNEL_QUALITY_ROUTING: 2026-06-28 起默认开启。
		// Opt-out：环境变量 AUTO_USE_CHANNEL_QUALITY_ROUTING=false。
		UseChannelQualityRouting: true,
		EnableV2Logic:            false,
	}
}

// LoadFeatureFlagsFromEnv loads feature flags from environment variables.
//
// UseChannelQualityRouting 的默认值是 true（与 DefaultFeatureFlags 一致），
// 表示"全量启动，没有灰度"。Opt-out：环境变量
// AUTO_USE_CHANNEL_QUALITY_ROUTING=false。
func LoadFeatureFlagsFromEnv() *FeatureFlags {
	flags := &FeatureFlags{
		UseSimplifiedScoring: getEnvBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:       getEnvBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation: getEnvBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:       getEnvBool("AUTO_USE_48H_FALLBACK", false),
		// CHANNEL_QUALITY_ROUTING: 默认 true（全量启动）
		UseChannelQualityRouting: getEnvBool("AUTO_USE_CHANNEL_QUALITY_ROUTING", true),
		EnableV2Logic:            getEnvBool("AUTO_ENABLE_V2", false),
	}

	if flags.EnableV2Logic {
		flags.UseSimplifiedScoring = true
		flags.UseHotTop3Pool = true
		flags.UseCacheRevalidation = true
		flags.Use48hFallback = true
		// NOTE: UseChannelQualityRouting 不受 EnableV2Logic 影响，
		// 它已经是默认开启；这里不需要再设为 true。
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
