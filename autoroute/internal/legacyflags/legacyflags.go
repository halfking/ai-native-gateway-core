// Package legacyflags 收纳 deprecated 的 autoroute env(设计稿 Decision 6)。
//
// 这些 flag 在 URSM v2 authoritative 单源方案下不再需要。保留仅为
// URSM_V2_MODE=off 时的回退路径。新代码一律读 URSM_V2_MODE, 不要读这些 flag。
//
// Deprecated: 用 URSM_V2_MODE 替代。
package legacyflags

import (
	"os"
	"strconv"
)

// Flags 包含 8 个 deprecated autoroute env 的解析结果。
// UseChannelQualityRouting 不在此处(它是已审计的稳定特性, 保留在 autoroute 包)。
//
// Deprecated: 用 URSM_V2_MODE 替代。
type Flags struct {
	UseSimplifiedScoring bool
	UseHotTop3Pool       bool
	UseCacheRevalidation bool
	Use48hFallback       bool
	AutoOnMessages       bool
	AutoOnResponses      bool
	AutoOnEmbeddings     bool
	AutoEmbeddingRoute   bool
}

// Load 从环境变量读取 8 个 deprecated flag。未设/非法值默认 false。
//
// Deprecated: 用 URSM_V2_MODE 替代。
func Load() *Flags {
	return &Flags{
		UseSimplifiedScoring: envBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:       envBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation: envBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:       envBool("AUTO_USE_48H_FALLBACK", false),
		AutoOnMessages:       envBool("AUTO_ON_MESSAGES", false),
		AutoOnResponses:      envBool("AUTO_ON_RESPONSES", false),
		AutoOnEmbeddings:     envBool("AUTO_ON_EMBEDDINGS", false),
		AutoEmbeddingRoute:   envBool("AUTO_EMBEDDING_ROUTE", false),
	}
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
