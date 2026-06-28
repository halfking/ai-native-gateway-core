package autoroute

import (
	"context"
	"os"
	"strconv"
)

// FeatureFlags 控制新旧逻辑的切换
type FeatureFlags struct {
	// UseSimplifiedScoring 是否启用简化评分（2 维 + 校正）
	// 环境变量：AUTO_USE_SIMPLIFIED_SCORING=true
	UseSimplifiedScoring bool

	// UseHotTop3Pool 是否启用 48h 热门 Top 3 池
	// 环境变量：AUTO_USE_HOT_TOP3_POOL=true
	UseHotTop3Pool bool

	// UseCacheRevalidation 是否启用会话缓存可用性重校验
	// 环境变量：AUTO_USE_CACHE_REVALIDATION=true
	UseCacheRevalidation bool

	// Use48hFallback 是否启用 48h 回退机制
	// 环境变量：AUTO_USE_48H_FALLBACK=true
	Use48hFallback bool

	// EnableV2Logic 是否完全启用 V2 逻辑（以上所有 flag 的总开关）
	// 环境变量：AUTO_ENABLE_V2=true
	EnableV2Logic bool
}

// DefaultFeatureFlags 返回默认的 feature flags（所有新逻辑默认关闭）
func DefaultFeatureFlags() *FeatureFlags {
	return &FeatureFlags{
		UseSimplifiedScoring:  false,
		UseHotTop3Pool:        false,
		UseCacheRevalidation:  false,
		Use48hFallback:        false,
		EnableV2Logic:         false,
	}
}

// LoadFeatureFlagsFromEnv 从环境变量加载 feature flags
func LoadFeatureFlagsFromEnv() *FeatureFlags {
	flags := &FeatureFlags{
		UseSimplifiedScoring:  getEnvBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:        getEnvBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation:  getEnvBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:        getEnvBool("AUTO_USE_48H_FALLBACK", false),
		EnableV2Logic:         getEnvBool("AUTO_ENABLE_V2", false),
	}

	// 如果总开关开启，启用所有子功能
	if flags.EnableV2Logic {
		flags.UseSimplifiedScoring = true
		flags.UseHotTop3Pool = true
		flags.UseCacheRevalidation = true
		flags.Use48hFallback = true
	}

	return flags
}

// getEnvBool 从环境变量读取布尔值
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

// globalFeatureFlags 是全局 feature flags 实例
var globalFeatureFlags *FeatureFlags

// InitFeatureFlags 初始化全局 feature flags（应在启动时调用一次）
func InitFeatureFlags() {
	globalFeatureFlags = LoadFeatureFlagsFromEnv()
}

// GetFeatureFlags 获取全局 feature flags
func GetFeatureFlags() *FeatureFlags {
	if globalFeatureFlags == nil {
		return DefaultFeatureFlags()
	}
	return globalFeatureFlags
}

// DecideWithFeatureFlags 是 Decide 的包装器，根据 feature flags 选择逻辑
func (d *Decider) DecideWithFeatureFlags(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	flags := GetFeatureFlags()

	if flags.EnableV2Logic {
		// 完全使用 V2 逻辑
		return d.DecideV2(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	}

	// 使用原有逻辑，但可以选择性启用部分新功能
	// （当前简化：要么全用新逻辑，要么全用旧逻辑）
	return d.Decide(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
}
