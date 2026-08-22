// Package catalog 定义 provider_catalog 表的数据模型、校验器、幂等 seed
// 生成器和 protocol 常量。
//
// 这是 omni-ref2 GW-01 的交付：provider catalog 不写成 Go 常量块，
// 也不把 credential secret 放入 catalog。DB 的 provider_catalog 表是
// runtime 事实源；本包提供：
//   - CatalogEntry：与 sql/objects/tables/provider_catalog.sql DDL 对齐的结构
//   - Validate：重复 key / 非法 HTTPS / 未知 protocol / 缺失元数据 / secret 扫描
//   - GenerateSeed：幂等 INSERT ... ON CONFLICT (code) DO UPDATE
//   - Protocol 常量：替换 executor 里的字符串字面量比较
//
// 设计约束（README §5 P1 / 矩阵 GW-01）：
//   - 认证 secret 只从环境或 secret store 解析；本包不持有任何 secret。
//   - 校验失败必须在导入前 fail，不允许半正确 catalog 进入 DB。
//   - seed generator 可重复执行（ON CONFLICT (code) DO UPDATE）。
package catalog

// Protocol 是 provider_catalog.protocol 列的合法取值。
// 与 sql/objects/tables/provider_catalog.sql:34 的 CHECK 约束一致。
// 这些常量供 executor/router 替换字符串字面量比较用（本轮仅建常量）。
const (
	ProtocolOpenAICompletions = "openai-completions"
	ProtocolOpenAIResponses   = "openai-responses"
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolGeminiGenerate    = "gemini-generate"
	ProtocolOllamaNative      = "ollama-native"
)

// Protocols 是 protocol CHECK 约束的完整集合，供 Validate 用。
var Protocols = []string{
	ProtocolOpenAICompletions,
	ProtocolOpenAIResponses,
	ProtocolAnthropicMessages,
	ProtocolGeminiGenerate,
	ProtocolOllamaNative,
}

// Tier 合法取值（provider_catalog.sql:35 tier CHECK）。
const (
	Tier1     = "tier1"
	Tier2     = "tier2"
	TierLocal = "local"
	TierRestr = "restricted"
)

var Tiers = []string{Tier1, Tier2, TierLocal, TierRestr}

// Kind 合法取值（provider_catalog.sql:33 kind CHECK）。
const (
	KindCloud = "cloud"
	KindLocal = "local"
)

var Kinds = []string{KindCloud, KindLocal}

// Category 合法取值（provider_catalog.sql:31 category CHECK）。
const (
	CategoryOfficial        = "official"
	CategoryOfficialProxy   = "official_proxy"
	CategoryThirdPartyRelay = "third_party_relay"
	CategoryAggregator      = "aggregator"
	CategorySelfHost        = "self_host"
)

var Categories = []string{
	CategoryOfficial, CategoryOfficialProxy, CategoryThirdPartyRelay,
	CategoryAggregator, CategorySelfHost,
}

// DiscoveryStrategy 合法取值（provider_catalog.sql:32 discovery_strategy CHECK）。
const (
	DiscoveryAuto     = "auto"
	DiscoveryManifest = "manifest"
	DiscoveryHybrid   = "hybrid"
)

var DiscoveryStrategies = []string{DiscoveryAuto, DiscoveryManifest, DiscoveryHybrid}
