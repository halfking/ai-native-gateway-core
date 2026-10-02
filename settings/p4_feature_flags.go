package settings

import (
	"os"
	"strconv"
	"strings"
)

// P4FeatureFlags controls the r0924 supplier-protocol-optimization P4
// (endpoint selector + Ollama native) rollout.
//
// All flags default to false/disabled to ensure zero impact on existing
// routing until explicitly enabled. When disabled, the dispatcher continues
// using legacy BaseURL/Protocol from provider.Candidate (Stage 4 fallback).
//
// Design: docs/供应商协议优化-实施规划.md P4.1-P4.3
type P4FeatureFlags struct {
	// EndpointSelectorEnabled is the master switch for endpointselect.Select()
	// integration in executor_dispatch.go. When false, the dispatcher uses the
	// legacy candidate.BaseURL and candidate.Protocol without modification
	// (Stage 4 fallback behavior).
	//
	// Default: false (selector disabled, legacy routing)
	// Env: FF_ENDPOINT_SELECTOR=true
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

// GetP4Flags returns the P4 feature flags, read from environment variables
// (FF_ENDPOINT_SELECTOR, FF_OLLAMA_NATIVE) with default=false.
//
// Usage in executor_dispatch.go:
//
//	p4flags := settings.GetP4Flags()
//	if p4flags.EndpointSelectorEnabled {
//	    decision := endpointselect.Select(...)
//	    cand.BaseURL = decision.BaseURL
//	    cand.Protocol = decision.Protocol
//	}
func GetP4Flags() *P4FeatureFlags {
	return &P4FeatureFlags{
		EndpointSelectorEnabled: envBoolP4("FF_ENDPOINT_SELECTOR", false),
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
