// Package paramreg 是网关请求参数的唯一注册表。
//
// 背景（docs/参数全量兼容/01-审计基线与研究结论.md）：
// IR 的三段式架构（parse → IR → serialize）本身正确，未知字段会进
// InternalRequest.Extensions。但还原侧被三道门禁锁死：
//
//  1. serialize_openai.go / serialize_anthropic.go / serialize_responses.go
//     只在 SourceProtocol == 目标协议时还原 —— 跨协议路由（本网关的核心场景，
//     如 Claude Code → DeepSeek）一个字段都还原不了。
//  2. serialize_gemini.go 完全没有还原代码。
//  3. ir_converter.go 叠加 ClientCatalogCode != UpstreamCatalogCode 的二次门禁 ——
//     等价于"网关不转发时才还原"，自相矛盾。
//
// 门禁的原始动机（避免把 A 厂商私有字段泄漏给 B 厂商上游）是对的，手段是错的。
// paramreg 的做法是按**字段**判定而非按整包判定：
//
//   - 注册表未登记的字段（未知/前向兼容）→ 无条件跨协议透传
//   - 登记为某方言私有的字段 → 仅目标方言匹配时还原，否则裁剪并上报 loss
//   - 登记为可翻译的字段 → 查映射函数转换
//   - 登记为某方言明确拒绝的字段 → 裁剪（优先级最高）
//
// 这样既满足 Claude Code 的 CLAUDE_CODE_EXTRA_BODY 任意字段透传，
// 又不会把 Anthropic 私有的 context_management 泄漏进 Mistral 的
// additionalProperties:false body（会硬 400）。
package paramreg

// Dialect 标识参数的方言归属。
//
// 注意 Dialect 与"协议"不是一回事：DeepSeek 和 GLM 都说 openai-chat 协议，
// 但各有互不相认的私有参数，所以是两个方言。IR 现有的 SourceProtocol 只能
// 区分 4 种协议，粒度不够，因此这里独立建模。
type Dialect string

const (
	// DialectUnknown 表示调用方未能确定方言。决策时按"最宽松"处理，
	// 即只裁剪明确 RejectedBy 的字段，其余放行 —— 宁可多传也不要静默丢。
	DialectUnknown Dialect = ""

	// ─── 四种一等协议方言 ───

	DialectOpenAIChat Dialect = "openai_chat"
	DialectResponses  Dialect = "openai_responses"
	DialectAnthropic  Dialect = "anthropic"
	DialectGemini     Dialect = "gemini"

	// ─── OpenAI 兼容形态的厂商方言 ───

	DialectDeepSeek   Dialect = "deepseek"
	DialectQwen       Dialect = "qwen"
	DialectGLM        Dialect = "glm"
	DialectMiniMax    Dialect = "minimax"
	DialectKimi       Dialect = "kimi"
	DialectArk        Dialect = "ark" // 火山引擎 Ark / 豆包
	DialectGrok       Dialect = "grok"
	DialectMistral    Dialect = "mistral"
	DialectOpenRouter Dialect = "openrouter"
	DialectVLLM       Dialect = "vllm"
	DialectOllama     Dialect = "ollama"
)

// AllDialects 是全部已建模方言，供测试与 admin 展示使用。
var AllDialects = []Dialect{
	DialectOpenAIChat, DialectResponses, DialectAnthropic, DialectGemini,
	DialectDeepSeek, DialectQwen, DialectGLM, DialectMiniMax, DialectKimi,
	DialectArk, DialectGrok, DialectMistral, DialectOpenRouter, DialectVLLM,
	DialectOllama,
}

// baseProtocol 把厂商方言映射回其底层线格式。
//
// 用途：判断某厂商方言是否认识另一个方言的"协议级"字段。例如 DeepSeek
// 说 openai-chat，所以它认识 logit_bias 这类 OpenAI Chat 标准字段。
var baseProtocol = map[Dialect]Dialect{
	DialectDeepSeek:   DialectOpenAIChat,
	DialectQwen:       DialectOpenAIChat,
	DialectGLM:        DialectOpenAIChat,
	DialectMiniMax:    DialectOpenAIChat,
	DialectKimi:       DialectOpenAIChat,
	DialectArk:        DialectOpenAIChat,
	DialectGrok:       DialectOpenAIChat,
	DialectMistral:    DialectOpenAIChat,
	DialectOpenRouter: DialectOpenAIChat,
	DialectVLLM:       DialectOpenAIChat,
	DialectOllama:     DialectOpenAIChat,
}

// BaseProtocol 返回方言的底层线格式。一等协议方言返回自身。
func BaseProtocol(d Dialect) Dialect {
	if base, ok := baseProtocol[d]; ok {
		return base
	}
	return d
}

// IsOpenAIShaped 报告方言是否使用 OpenAI Chat Completions 线格式。
func IsOpenAIShaped(d Dialect) bool {
	return BaseProtocol(d) == DialectOpenAIChat
}

// catalogToDialect 把 provider catalog code 映射到方言。
//
// catalog code 取值来自 provider_catalog 表 / provider.Candidate.CatalogCode，
// 已知别名一并登记（如 zhipu 与 glm 是同一家）。
var catalogToDialect = map[string]Dialect{
	"openai":     DialectOpenAIChat,
	"azure":      DialectOpenAIChat,
	"anthropic":  DialectAnthropic,
	"gemini":     DialectGemini,
	"google":     DialectGemini,
	"vertex":     DialectGemini,
	"deepseek":   DialectDeepSeek,
	"qwen":       DialectQwen,
	"dashscope":  DialectQwen,
	"aliyun":     DialectQwen,
	"zhipu":      DialectGLM,
	"glm":        DialectGLM,
	"bigmodel":   DialectGLM,
	"zai":        DialectGLM,
	"minimax":    DialectMiniMax,
	"moonshot":   DialectKimi,
	"kimi":       DialectKimi,
	"volcengine": DialectArk,
	"volcano":    DialectArk,
	"ark":        DialectArk,
	"doubao":     DialectArk,
	"xai":        DialectGrok,
	"grok":       DialectGrok,
	"mistral":    DialectMistral,
	"openrouter": DialectOpenRouter,
	"vllm":       DialectVLLM,
	"ollama":     DialectOllama,
}

// DialectForCatalogCode 把 provider catalog code 解析为方言。
//
// 未登记的 catalog code 返回 DialectUnknown，决策时走最宽松路径（放行），
// 这是有意的：新接入的厂商不该因为没登记就丢参数。
func DialectForCatalogCode(code string) Dialect {
	if code == "" {
		return DialectUnknown
	}
	if d, ok := catalogToDialect[normalizeKey(code)]; ok {
		return d
	}
	return DialectUnknown
}

// protocolToDialect 把 IR 的 SourceProtocol 常量映射到方言。
//
// 与 internal/ir 的 Protocol* 常量保持一致，但这里用字面量避免
// paramreg → ir 的反向依赖（ir 需要 import paramreg）。
var protocolToDialect = map[string]Dialect{
	"openai-chat":        DialectOpenAIChat,
	"openai-responses":   DialectResponses,
	"anthropic-messages": DialectAnthropic,
	"gemini-generate":    DialectGemini,
}

// DialectForProtocol 把协议标识解析为方言。
func DialectForProtocol(protocol string) Dialect {
	if d, ok := protocolToDialect[protocol]; ok {
		return d
	}
	return DialectUnknown
}

// Resolve 综合 catalog code 与协议推导方言。
//
// catalog code 优先（粒度更细，能区分 openai-chat 形态下的 DeepSeek 与 GLM），
// 无法识别时回退到协议。
func Resolve(catalogCode, protocol string) Dialect {
	if d := DialectForCatalogCode(catalogCode); d != DialectUnknown {
		return d
	}
	return DialectForProtocol(protocol)
}
