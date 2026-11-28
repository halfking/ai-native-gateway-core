package settings

import (
	"os"
	"strconv"
	"strings"
)

// P4FeatureFlags controls the r0924 supplier-protocol-optimization P4
// (endpoint selector + Ollama native) rollout.
//
// FF_ENDPOINT_SELECTOR defaults to true since A6 (2026-11-28 owner 拍板):
// the selector only rewrites (BaseURL, Protocol) for providers with explicit
// provider_endpoint_protocols rows; everyone else keeps the legacy primary
// pair via Stage 4 fallback. FF_OLLAMA_NATIVE remains default=false — see the
// ⛔ note on OllamaNativeEnabled (R72 audit §3.1).
//
// Design: docs/供应商协议优化-实施规划.md P4.1-P4.3
type P4FeatureFlags struct {
	// EndpointSelectorEnabled is the master switch for endpointselect.Select()
	// integration in executor_dispatch.go. When false, the dispatcher uses the
	// legacy candidate.BaseURL and candidate.Protocol without modification
	// (Stage 4 fallback behavior).
	//
	// Default: true since A6 (2026-11-28 owner 拍板). The selector only
	// rewrites (BaseURL, Protocol) when the provider has explicit
	// provider_endpoint_protocols rows; providers without endpoint rows and
	// requests whose protocol has no matching endpoint fall back to the
	// primary pair (Stage 4) — byte-identical to legacy routing. Rollback
	// knob: FF_ENDPOINT_SELECTOR=false restores pre-A6 behavior with zero
	// code change.
	// Env: FF_ENDPOINT_SELECTOR=false to disable
	EndpointSelectorEnabled bool

	// OllamaNativeEnabled enables the ollama-native protocol executor.
	// When false, the dispatch switch rejects an ollama-native primary
	// with KindUnsupportedFeature; it does not send OpenAI wire to /api/chat.
	// Selector also excludes optional ollama-native endpoints.
	//
	// Default: false (ollama-native executor disabled)
	// Env: FF_OLLAMA_NATIVE=true
	//
	// ⛔ 不可开启（R72 审计 §3.1，2026-10-01）：IR 模式下 handler 用
	// ir.DetectProtocol 给 ExecParams.ClientProtocol 打标，产出的是 **IR 词表**
	// （"openai-chat" / "ollama-chat"），而 ollama executor 的协议门禁
	// （finalizeOllamaUpstreamBody）只接受 **catalog 词表**
	// （"openai-completions"）——两个词表永不相等，开关一开，所有 Ollama
	// 出站请求在 executor_ollama.go 的门禁处全量 501。
	// 保持 false 时 dispatch 降级 routeOpenAI（Ollama 的 OpenAI 兼容端点），
	// 生产不受影响。待四步接线完成（usage 拆分 → ollama.* 命名空间扩展 →
	// tool_calls 解析 → 最后才开开关）之前，此旋钮必须留在 false。
	// cmd/gateway 启动时会对显式开启打 Warn；501 门禁行为不动——那是门禁
	// 在正确地挡半成品路径。
	OllamaNativeEnabled bool
}

// GetP4Flags returns the P4 feature flags. FF_ENDPOINT_SELECTOR defaults to
// true since A6 (2026-11-28 owner 拍板): the selector is safe-by-construction
// (Stage 4 fallback = legacy pair when no endpoint rows match) and its
// decisions are observable via the endpoint_selector_decision log. FF_OLLAMA_NATIVE
// remains default=false (⛔ see OllamaNativeEnabled — R72 audit §3.1).
//
// Usage in executor_dispatch.go:
//
//	p4flags := settings.GetP4Flags()
//	cand = selectDispatchEndpoint(cand, params.ClientProtocol, p4flags)
func GetP4Flags() *P4FeatureFlags {
	return &P4FeatureFlags{
		EndpointSelectorEnabled: envBoolP4("FF_ENDPOINT_SELECTOR", true),
		OllamaNativeEnabled:     envBoolP4("FF_OLLAMA_NATIVE", false),
	}
}

// envBoolP4 parses a boolean env var; empty or invalid → defaultValue.
// Duplicated from routing_opt_feature_flags.go to avoid cross-package import
// cycles (settings is a leaf package).
func envBoolP4(key string, defaultValue bool) bool {
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
