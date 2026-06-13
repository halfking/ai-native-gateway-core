// Package upstreamurl is the single source of truth (SSoT) for constructing
// upstream LLM provider API endpoint URLs from a base URL.
//
// Why this package exists
//
// Before this package, llm-gateway-go had API endpoint construction logic
// duplicated across at least 11 call sites in 7 files (e.g.
// `base + "/v1/chat/completions"`, `strings.TrimRight(s.BaseURL, "/") +
// "/v1/messages"`, a local `modelsURLCandidates` in
// bg/credential_probe_v2.go, etc.). For providers whose base URL ends in
// a non-`/v1` version segment (volcengine ark `/api/v3`, zhipu
// `/api/coding/paas/v4`, etc.), the duplicated logic either 404'd or
// caused the background health probe to flip every credential to
// `health_status='unreachable'` at the same time, which made
// `v_routable_credential_models.is_routable` go FALSE for all providers
// simultaneously — the "every provider fails at the same time" outage.
//
// The fix is to centralize the rules in one package. All upstream URL
// construction MUST go through the functions here. The functions all
// run the standard 2-step base-URL cleanup:
//   1. trim trailing /
//   2. strip one well-known completion suffix if present
//      (e.g. `/v1/chat/completions`, `/v1/messages`)
//   3. strip a trailing /vN (e.g. /v1, /v2, /v3, /v4) via regex
// and then append the canonical endpoint path for the given Endpoint.
//
// Mirrors services/llm-gateway/app/services/free_pool_probe.py
// (probe_openai_compatible_base) and discovery_utils._models_endpoint.
package upstreamurl

import (
	"regexp"
	"strings"
)

// Endpoint enumerates the upstream LLM provider API endpoints that the
// gateway knows how to construct URLs for.
type Endpoint string

const (
	// EpModels is GET /v1/models (model listing).
	EpModels Endpoint = "models"
	// EpChatCompletions is POST /v1/chat/completions (OpenAI chat-style).
	EpChatCompletions Endpoint = "chat_completions"
	// EpCompletions is POST /v1/completions (legacy OpenAI completions).
	EpCompletions Endpoint = "completions"
	// EpMessages is POST /v1/messages (Anthropic Messages API).
	EpMessages Endpoint = "messages"
	// EpResponses is POST /v1/responses (OpenAI Responses API).
	EpResponses Endpoint = "responses"
)

// versionTrailing matches an optional trailing version segment "/v1"
// through "/v9" at the very end of a base URL. It does NOT match
// "/v1beta", "/v2-preview", etc.
var versionTrailing = regexp.MustCompile(`/v[1-9]$`)

// completionSuffixes are stripped when they appear at the tail of a base URL.
// Order matters: longer /vN/foo must be checked before bare /foo, and
// /v1/ variants must be checked before other /vN/ variants.
var completionSuffixes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/responses",
	"/v1/messages",
	"/v1/models",
	"/chat/completions",
	"/completions",
	"/responses",
	"/messages",
	"/models",
}

// stripCompletionSuffix trims trailing "/" and strips one well-known
// completion suffix if present (e.g. "/v1/chat/completions"). Unlike
// the old stripTail, it does NOT strip the trailing "/vN" version segment.
func stripCompletionSuffix(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	for _, suffix := range completionSuffixes {
		if strings.HasSuffix(baseURL, suffix) {
			baseURL = baseURL[:len(baseURL)-len(suffix)]
			break
		}
	}
	return baseURL
}

// pathAfterVersion returns the endpoint-specific path WITHOUT a version
// prefix, suitable for appending to a base URL that already ends with "/vN".
func pathAfterVersion(ep Endpoint) string {
	switch ep {
	case EpModels:
		return "/models"
	case EpChatCompletions:
		return "/chat/completions"
	case EpCompletions:
		return "/completions"
	case EpMessages:
		return "/messages"
	case EpResponses:
		return "/responses"
	}
	return ""
}

// PathFor returns the canonical API path for a given endpoint, e.g.
// PathFor(EpChatCompletions) → "/v1/chat/completions".
func PathFor(ep Endpoint) string {
	switch ep {
	case EpModels:
		return "/v1/models"
	case EpChatCompletions:
		return "/v1/chat/completions"
	case EpCompletions:
		return "/v1/completions"
	case EpMessages:
		return "/v1/messages"
	case EpResponses:
		return "/v1/responses"
	}
	return ""
}

// Build returns the full URL for a given endpoint and base URL.
//
// If the base URL already contains a version segment (e.g. /v1, /v3, /v4),
// only the endpoint-specific path is appended (preserving the provider's
// version). Otherwise, /v1 is prepended (matching OpenAI convention).
//
//	Build("https://ark.cn-beijing.volces.com/api/v3", EpChatCompletions)
//	  → "https://ark.cn-beijing.volces.com/api/v3/chat/completions"
//	Build("https://open.bigmodel.cn/api/paas/v4", EpChatCompletions)
//	  → "https://open.bigmodel.cn/api/paas/v4/chat/completions"
//	Build("https://api.openai.com/v1", EpChatCompletions)
//	  → "https://api.openai.com/v1/chat/completions"
//	Build("https://api.openai.com/v1/chat/completions", EpChatCompletions)
//	  → "https://api.openai.com/v1/chat/completions"
//	Build("https://api.example.com", EpChatCompletions)
//	  → "https://api.example.com/v1/chat/completions"
//	Build("", EpChatCompletions) → ""
func Build(baseURL string, ep Endpoint) string {
	if baseURL == "" {
		return ""
	}
	cleaned := stripCompletionSuffix(baseURL)
	if versionTrailing.MatchString(cleaned) {
		return cleaned + pathAfterVersion(ep)
	}
	return cleaned + PathFor(ep)
}

// ModelsURL is shorthand for Build(baseURL, EpModels). Kept for
// compatibility with the legacy urlutil.ModelsURL signature.
func ModelsURL(baseURL string) string {
	return Build(baseURL, EpModels)
}

// ChatCompletionsURL is shorthand for Build(baseURL, EpChatCompletions).
func ChatCompletionsURL(baseURL string) string {
	return Build(baseURL, EpChatCompletions)
}

// CompletionsURL is shorthand for Build(baseURL, EpCompletions).
func CompletionsURL(baseURL string) string {
	return Build(baseURL, EpCompletions)
}

// MessagesURL is shorthand for Build(baseURL, EpMessages).
func MessagesURL(baseURL string) string {
	return Build(baseURL, EpMessages)
}

// ResponsesURL is shorthand for Build(baseURL, EpResponses).
func ResponsesURL(baseURL string) string {
	return Build(baseURL, EpResponses)
}

// ModelsURLCandidates returns the candidate models-endpoint URLs to try
// for a provider, in priority order. Empty baseURL returns nil.
//
// For base URLs that do NOT end in /v1, returns [base/models, base/v1/models].
// For base URLs that DO end in /v1 (e.g. xiaomi, minimax, nvidia, vapeur),
// prepends root/v1/models so providers who serve models on the root
// (rather than the version-suffixed path) still get probed.
//
//	ModelsURLCandidates("https://api.openai.com")
//	  → ["https://api.openai.com/models",
//	     "https://api.openai.com/v1/models"]
//	ModelsURLCandidates("https://token-plan-cn.xiaomimimo.com/v1")
//	  → ["https://token-plan-cn.xiaomimimo.com/v1/models",
//	     "https://token-plan-cn.xiaomimimo.com/v1/models",  // root+v1
//	     "https://token-plan-cn.xiaomimimo.com/v1/v1/models"] // v1/v1
//	ModelsURLCandidates("https://ark.cn-beijing.volces.com/api/v3")
//	  → ["https://ark.cn-beijing.volces.com/api/models",
//	     "https://ark.cn-beijing.volces.com/api/v1/models"]   // /v3 stripped
func ModelsURLCandidates(baseURL string) []string {
	if baseURL == "" {
		return nil
	}
	normalized := strings.TrimRight(baseURL, "/")
	out := []string{
		normalized + "/models",
		normalized + "/v1/models",
	}
	if strings.HasSuffix(normalized, "/v1") {
		root := strings.TrimRight(normalized, "/v1")
		root = strings.TrimRight(root, "/")
		out = append([]string{root + "/v1/models"}, out...)
	}
	return out
}
