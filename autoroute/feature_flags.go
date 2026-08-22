package autoroute

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/autoroute/internal/legacyflags"
)

// FeatureFlags controls selective rollout of the V2 autoroute path.
type FeatureFlags struct {
	// UseSimplifiedScoring enables the 2-dim score (intent/price + correction).
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	UseSimplifiedScoring bool

	// UseHotTop3Pool enables seeding candidates from the 48h hot top-3 canonicals.
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	UseHotTop3Pool bool

	// UseCacheRevalidation enables live availability revalidation for session cache hits.
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	UseCacheRevalidation bool

	// Use48hFallback enables the 48h fallback candidate selection.
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
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

	UseExplicitDefault bool
	UseComplexityScore bool

	// AutoOnMessages gates model=auto on non-chat endpoints (22 章 §22.2).
	// Default off; independent of the other AutoOn* flags.
	// Off → those endpoints ignore model="auto" (no rewrite, upstream sees "auto").
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	AutoOnMessages bool
	// AutoOnResponses gates model=auto on non-chat endpoints (22 章 §22.2).
	// Default off; independent of the other AutoOn* flags.
	// Off → those endpoints ignore model="auto" (no rewrite, upstream sees "auto").
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	AutoOnResponses bool
	// AutoOnEmbeddings gates model=auto on non-chat endpoints (22 章 §22.2).
	// Default off; independent of the other AutoOn* flags.
	// Off → those endpoints ignore model="auto" (no rewrite, upstream sees "auto").
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	AutoOnEmbeddings bool
	// AutoEmbeddingRoute enables the M3 embedding shadow path. Default off.
	//
	// Deprecated: 用 URSM_V2_MODE 替代。详见 autoroute/internal/legacyflags。
	AutoEmbeddingRoute bool

	// UseStandardIQGate enables the RT-1 MinStandardIQ hard gate: candidates
	// whose standard IQ (Artificial Analysis Intelligence Index, 0-100) is
	// known and below MinStandardIQ are excluded before scoring. Default off;
	// when off, routing decisions are byte-identical to the pre-RT-1 path.
	// Per-tenant policy (routing_policy.weights_json min_standard_iq) takes
	// precedence over this global flag.
	UseStandardIQGate bool
	// MinStandardIQ is the global gate threshold. 0 (default) disables the
	// gate even when UseStandardIQGate is on.
	MinStandardIQ float64

	// UsePopularityWeight enables RT-3: candidate PopularityScore (from
	// credential_model_bindings.routing_tier) and featured_models (from
	// routing_policy) feed an additive ordering weight on top of the
	// composite. Default off; when off, composites and ordering are
	// byte-identical to the pre-RT-3 path.
	UsePopularityWeight bool
	// PopularityWeight is the max additive points for popularity (applied as
	// weight * PopularityScore/100). Default 5 — large enough to break ties,
	// small enough not to outweigh the scoring dimensions.
	PopularityWeight float64
	// FeaturedBonus is the additive points for models listed in
	// routing_policy.featured_models. Default 5.
	FeaturedBonus float64
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
		UseExplicitDefault:       false,
		UseComplexityScore:       false,
		AutoOnMessages:           false,
		AutoOnResponses:          false,
		AutoOnEmbeddings:         false,
		AutoEmbeddingRoute:       false,
		// RT-1 MinStandardIQ gate: default off (hard requirement — flag-off
		// routing decisions must be byte-identical).
		UseStandardIQGate: false,
		MinStandardIQ:     0,
		// RT-3 popularity/featured weighting: default off (same requirement).
		UsePopularityWeight: false,
		PopularityWeight:    5,
		FeaturedBonus:       5,
	}
}

// LoadFeatureFlagsFromEnv loads feature flags from environment variables.
//
// 8 个 deprecated flag (UseSimplifiedScoring 等) 委托给 legacyflags.Load()
// (设计稿 Decision 6)。新代码应读 URSM_V2_MODE。
//
// UseChannelQualityRouting 默认 true(全量启动), 不下沉。
func LoadFeatureFlagsFromEnv() *FeatureFlags {
	lf := legacyflags.Load()
	flags := &FeatureFlags{
		UseSimplifiedScoring: lf.UseSimplifiedScoring,
		UseHotTop3Pool:       lf.UseHotTop3Pool,
		UseCacheRevalidation: lf.UseCacheRevalidation,
		Use48hFallback:       lf.Use48hFallback,
		// CHANNEL_QUALITY_ROUTING: 默认 true（全量启动）
		UseChannelQualityRouting: getEnvBool("AUTO_USE_CHANNEL_QUALITY_ROUTING", true),
		EnableV2Logic:            getEnvBool("AUTO_ENABLE_V2", false),
		UseExplicitDefault:       getEnvBool("AUTO_USE_EXPLICIT_DEFAULT", false),
		UseComplexityScore:       getEnvBool("AUTO_USE_COMPLEXITY_SCORE", false),
		AutoOnMessages:           lf.AutoOnMessages,
		AutoOnResponses:          lf.AutoOnResponses,
		AutoOnEmbeddings:         lf.AutoOnEmbeddings,
		AutoEmbeddingRoute:       lf.AutoEmbeddingRoute,
		// RT-1 MinStandardIQ gate: AUTO_USE_STANDARD_IQ_GATE (default off) +
		// AUTO_MIN_STANDARD_IQ (default 0 = inactive even when the gate is on).
		UseStandardIQGate: getEnvBool("AUTO_USE_STANDARD_IQ_GATE", false),
		MinStandardIQ:     getEnvFloat("AUTO_MIN_STANDARD_IQ", 0),
		// RT-3: AUTO_USE_POPULARITY_WEIGHT (default off) +
		// AUTO_POPULARITY_WEIGHT / AUTO_FEATURED_BONUS (defaults 5 / 5).
		UsePopularityWeight: getEnvBool("AUTO_USE_POPULARITY_WEIGHT", false),
		PopularityWeight:    getEnvFloat("AUTO_POPULARITY_WEIGHT", 5),
		FeaturedBonus:       getEnvFloat("AUTO_FEATURED_BONUS", 5),
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

// getEnvFloat reads a float env var, falling back to defaultValue when unset
// or unparseable.
func getEnvFloat(key string, defaultValue float64) float64 {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultValue
	}
	return f
}

// 2026-07-27 concurrency fix: globalFeatureFlags is read on the hot path
// (every autoroute request) and written by InitFeatureFlags /
// SetGlobalFeatureFlagsForTest. Store it in an atomic.Pointer so the
// reader/writer are synchronised without a mutex on the hot path.
var globalFeatureFlags atomic.Pointer[FeatureFlags]

// InitFeatureFlags initializes the global feature flags instance.
func InitFeatureFlags() {
	globalFeatureFlags.Store(LoadFeatureFlagsFromEnv())
}

// GetFeatureFlags returns the global feature flags.
func GetFeatureFlags() *FeatureFlags {
	if f := globalFeatureFlags.Load(); f != nil {
		return f
	}
	return DefaultFeatureFlags()
}

// SetGlobalFeatureFlagsForTest overrides the global flags for tests.
// Pass the previous value (from GetFeatureFlags) to restore in defer.
func SetGlobalFeatureFlagsForTest(f *FeatureFlags) { globalFeatureFlags.Store(f) }

func activeFeatureNames(flags *FeatureFlags) []string {
	if flags == nil {
		return nil
	}
	features := make([]string, 0, 5)
	if flags.UseSimplifiedScoring {
		features = append(features, "simplified_scoring")
	}
	if flags.UseHotTop3Pool {
		features = append(features, "hot_top3_pool")
	}
	if flags.UseCacheRevalidation {
		features = append(features, "cache_revalidation")
	}
	if flags.Use48hFallback {
		features = append(features, "fallback_48h")
	}
	if flags.UseChannelQualityRouting {
		features = append(features, "channel_quality_routing")
	}
	if flags.AutoEmbeddingRoute {
		features = append(features, "embedding_shadow")
	}
	if flags.UseStandardIQGate && flags.MinStandardIQ > 0 {
		features = append(features, "standard_iq_gate")
	}
	return features
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
