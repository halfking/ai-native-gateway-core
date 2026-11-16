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
// 2026-11-16（auto 模式可用性轮）：AUTO_ON_MESSAGES / AUTO_ON_RESPONSES 的
// 默认值由 false 改为 **true**。
//
// 理由：auto 模式此前**只存在于 chat completions 一个协议面**。用
// Anthropic 协议（Claude Code 一类客户端）或 Responses 协议（Codex 一类）
// 发 model="auto" 时，协议面直接跳过 auto 解析 ⇒ "auto" 被当成一个真实模型名
// 去路由 ⇒ 零候选 ⇒ 客户端看到「模型 auto 不可用」。同一个请求换协议就断，
// 这不是"另一个协议不支持 auto"，这是 auto 模式**没有做完**。
//
// 三个协议面共用同一个 Decider（auto_route.go / auto_route_nonchat.go 的
// maybeResolveAuto / maybeResolveAutoForMessages / maybeResolveAutoForResponses
// 都是同一套信号提取 + DecideWithFeatureFlags），所以协议面之间不存在
// "只有 messages 面分类不准"的风险。
//
// AUTO_ON_EMBEDDINGS 仍默认关闭：embedding 请求没有"任务类型"可言，
// auto 路由对它的语义没有定义（保持原状）。
//
// Deprecated: 用 URSM_V2_MODE 替代。
func Load() *Flags {
	return &Flags{
		UseSimplifiedScoring: envBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:       envBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation: envBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:       envBool("AUTO_USE_48H_FALLBACK", false),
		AutoOnMessages:       envBool("AUTO_ON_MESSAGES", true),
		AutoOnResponses:      envBool("AUTO_ON_RESPONSES", true),
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
