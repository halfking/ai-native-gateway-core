// Package urlutil normalizes LLM provider base URLs and constructs
// well-known probe endpoints (e.g. /v1/models).
//
// Mirrors services/llm-gateway/app/services/free_pool_probe.py
// (probe_openai_compatible_base) and discovery_utils._models_endpoint.
package urlutil

import (
	"regexp"
	"strings"
)

// versionTrailing matches an optional trailing version segment
// "/v1" through "/v9" at the very end of a base URL. It does NOT
// match "/v1beta", "/v2-preview", etc.
var versionTrailing = regexp.MustCompile(`/v[1-9]$`)

// completionSuffixes are stripped when they appear at the tail of a base URL.
var completionSuffixes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/responses",
	"/v1/messages",
}

// stripTail runs the standard 2-step base-URL cleanup:
//   1. trim trailing /
//   2. strip one well-known completion suffix if present
//   3. strip a trailing /vN (e.g. /v1, /v2, /v3, /v4) via regex
func stripTail(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	for _, suffix := range completionSuffixes {
		if strings.HasSuffix(baseURL, suffix) {
			baseURL = baseURL[:len(baseURL)-len(suffix)]
			break
		}
	}
	baseURL = versionTrailing.ReplaceAllString(baseURL, "")
	return baseURL
}

// ModelsURL returns the /v1/models probe URL for a provider, by cleaning
// the base URL and appending /v1/models. Returns "" if baseURL is empty.
//
// Mirrors free_pool_probe.probe_openai_compatible_base candidate #2.
//
//	"https://api.minimaxi.com/v1"            → "https://api.minimaxi.com/v1/models"
//	"https://api.openai.com"                 → "https://api.openai.com/v1/models"
//	"https://ark.../api/v3"                  → "https://ark.../api/v1/models"
//	"https://ark.../api/coding/v3"           → "https://ark.../api/coding/v1/models"
//	"https://api.openai.com/v1/chat/completions" → "https://api.openai.com/v1/models"
//	""                                       → ""
func ModelsURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return stripTail(baseURL) + "/v1/models"
}

// ChatCompletionsURL returns the /v1/chat/completions probe URL for
// a provider, by cleaning the base URL and appending /v1/chat/completions.
// Returns "" if baseURL is empty.
//
// Mirrors free_pool_probe.probe_openai_compatible_base chat-candidate #1.
func ChatCompletionsURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return stripTail(baseURL) + "/v1/chat/completions"
}

// ModelsURLCandidates returns the candidate models-endpoint URLs to try
// for a provider, in priority order. Empty baseURL returns nil.
//
// Mirrors free_pool_probe.probe_openai_compatible_base candidates list:
//   1. {root-with-v1-stripped}/v1/models   (only when base ends in /v1)
//   2. {base}/models
//   3. {base}/v1/models
//
// For xiaomi/minimax/nvidia/vapeur (base ends in /v1), candidates 2 and 3
// resolve to the same path; the caller should dedup if needed.
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
