// protocol_normalize.go — 协议命名收口与「按模型推荐协议」的单一事实源。
//
// 背景（2026-09-23 vapeur 事故复盘 + 协议命名审计）：
//
//	同一「协议」概念在仓内存在多套拼写：catalog/出站枚举（openai-completions
//	等 5 值，provider_catalog.protocol CHECK 约束）、IR 协议（openai-chat 等，
//	internal/ir/types.go）、metrics/方言下划线 token（openai_chat 等）、旧
//	domains/provider 四值枚举（openai/anthropic/azure/custom）。providers 表的
//	protocol 列没有 CHECK 约束，用户把 type 写成 "openai-response"（单数）会
//	静默入库并落入执行器 default 分支，行为与配置不符。
//
// 本文件提供两个入口：
//
//	NormalizeProviderProtocol —— 把用户输入的协议值归一到 catalog 五值枚举；
//	RecommendedProtocolForModel / RecommendedProtocolForBaseURL —— 按大厂
//	规范推荐默认出站协议（OpenAI 新模型 → openai-responses、Anthropic →
//	anthropic-messages、Gemini → gemini-generate，其余 → openai-completions）。
//	推荐基线见 docs/vendor-formats/README.md 协议摘要与 openai-responses.md
//	（"适用: gpt-5、o-series 等新模型"）。
package catalog

import (
	"fmt"
	"strings"
)

// CanonicalProtocols 返回 provider_catalog.protocol CHECK 约束的完整枚举。
func CanonicalProtocols() []string {
	out := make([]string, len(Protocols))
	copy(out, Protocols)
	return out
}

// providerProtocolAliases 把常见拼写/别名归一到 canonical 枚举。
// key 统一为小写、'_' 折叠为 '-'（见 normalizeProtocolKey）。
var providerProtocolAliases = map[string]string{
	// openai-completions（chat）家族
	"openai":                  ProtocolOpenAICompletions,
	"chat":                    ProtocolOpenAICompletions,
	"openai-chat":             ProtocolOpenAICompletions,
	"openai-completion":       ProtocolOpenAICompletions,
	"openai-chatcompletion":   ProtocolOpenAICompletions,
	"openai-chat-completion":  ProtocolOpenAICompletions,
	"chat-completions":        ProtocolOpenAICompletions,
	"chatcompletions":         ProtocolOpenAICompletions,
	"openai-chat-completions": ProtocolOpenAICompletions,

	// openai-responses 家族 —— "openai-response"（单数）是 2026-09-23 vapeur
	// 事故中出现的确切错误拼写。
	"openai-response":     ProtocolOpenAIResponses,
	"response":            ProtocolOpenAIResponses,
	"responses":           ProtocolOpenAIResponses,
	"openai-response-api": ProtocolOpenAIResponses,

	// anthropic 家族
	"anthropic":         ProtocolAnthropicMessages,
	"anthropic-message": ProtocolAnthropicMessages,
	"claude":            ProtocolAnthropicMessages,
	"claude-messages":   ProtocolAnthropicMessages,

	// gemini 家族
	"gemini":        ProtocolGeminiGenerate,
	"google-gemini": ProtocolGeminiGenerate,

	// ollama 家族
	"ollama": ProtocolOllamaNative,
}

// normalizeProtocolKey 把原始协议串折叠为别名查表形态：trim、小写、'_'→'-'。
func normalizeProtocolKey(raw string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "_", "-")
}

// NormalizeProviderProtocol 把用户输入的协议值归一到 canonical 枚举。
// 已是 canonical 的输入原样返回；已知别名做映射；其余返回错误（信息里
// 列出全部合法值，调用方可直接作为 400 响应体）。
func NormalizeProviderProtocol(raw string) (string, error) {
	key := normalizeProtocolKey(raw)
	if key == "" {
		return "", fmt.Errorf("protocol is required (one of: %s)", strings.Join(Protocols, ", "))
	}
	for _, canonical := range Protocols {
		if key == canonical {
			return canonical, nil
		}
	}
	if mapped, ok := providerProtocolAliases[key]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("unknown protocol %q (one of: %s)", raw, strings.Join(Protocols, ", "))
}

// RecommendedProtocolForModel 按模型名推断该 LLM 的推荐出站协议。
//
// 规则基线（docs/vendor-formats/）：
//   - OpenAI 新一代模型（gpt-5*、o1/o3/o4 系列、codex-*）→ openai-responses
//     （openai-responses.md："适用: gpt-5、o-series 等新模型"）
//   - Anthropic Claude → anthropic-messages
//   - Google Gemini → gemini-generate
//   - 其余（DeepSeek/Qwen/GLM/MiniMax/Kimi/Grok/Mistral/LLaMA 等OpenAI 兼容
//     阵营）→ openai-completions
//
// 返回值 ok=false 表示没有家族命中，调用方应回退 openai-completions。
//
// 注意：推荐值只作为「未显式配置时的默认」；运行时是否走原生 Responses
// 传输仍由凭据的 SupportsNativeResponses 能力闸门决定——第三方中转的
// gpt-5 大多只暴露 /chat/completions，不能凭模型名强行切换线格式。
func RecommendedProtocolForModel(model string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ProtocolOpenAICompletions, false
	case strings.HasPrefix(m, "gpt-5"),
		strings.HasPrefix(m, "codex"),
		isOSeriesModel(m):
		return ProtocolOpenAIResponses, true
	case strings.HasPrefix(m, "claude"):
		return ProtocolAnthropicMessages, true
	case strings.HasPrefix(m, "gemini"):
		return ProtocolGeminiGenerate, true
	default:
		return ProtocolOpenAICompletions, false
	}
}

// isOSeriesModel 匹配 OpenAI o-series（o1 / o1-mini / o3 / o3-mini /
// o4-mini ...）。整名相等或 "oN-" 前缀，避免 "ollama"、"openai" 之类
// 意外命中。
func isOSeriesModel(m string) bool {
	for _, series := range []string{"o1", "o3", "o4"} {
		if m == series || strings.HasPrefix(m, series+"-") {
			return true
		}
	}
	return false
}

// RecommendedProtocolForBaseURL 按供应商官方 API 域名推断推荐出站协议。
// 命中官方端点时 ok=true；第三方聚合/中转（vapeur、openrouter 等）返回
// ok=false，由调用方回退 openai-completions 或让运营显式指定。
func RecommendedProtocolForBaseURL(baseURL string) (string, bool) {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case u == "":
		return ProtocolOpenAICompletions, false
	case strings.Contains(u, "anthropic.com"):
		return ProtocolAnthropicMessages, true
	case strings.Contains(u, "generativelanguage.googleapis.com"),
		strings.Contains(u, "aiplatform.googleapis.com"):
		return ProtocolGeminiGenerate, true
	case strings.Contains(u, "api.openai.com"):
		// OpenAI 官方端点：模型族由 RecommendedProtocolForModel 决定更准，
		// 这里没有模型上下文，保守返回 chat 兼容形态。
		return ProtocolOpenAICompletions, true
	case strings.Contains(u, "api.ollama.com"), strings.HasSuffix(u, ":11434"):
		return ProtocolOllamaNative, true
	default:
		return ProtocolOpenAICompletions, false
	}
}
