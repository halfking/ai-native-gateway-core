// Package urlutil normalizes LLM provider base URLs and constructs
// well-known probe endpoints (e.g. /v1/models) driven by the per-provider
// template stored in provider_catalog.models_endpoint_template.
package urlutil

import "strings"

// completionSuffixes are stripped when they appear at the tail of a base URL.
// Order matters: longer /vN/foo must be checked before bare /vN.
var completionSuffixes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/responses",
	"/v1/messages",
}

// CleanBaseURL strips a trailing slash and any well-known completion suffix
// (e.g. /v1/chat/completions) so callers can safely append a path segment.
// Note: bare version suffixes like /v1, /v2, /v3, /v4 are NOT stripped —
// that logic moved to BuildModelsURL which takes a per-provider template.
func CleanBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	for _, suffix := range completionSuffixes {
		if strings.HasSuffix(baseURL, suffix) {
			return baseURL[:len(baseURL)-len(suffix)]
		}
	}
	return baseURL
}

// BuildModelsURL joins a provider base URL with its catalog-supplied
// models-endpoint template. Returns "" when the template is empty,
// which signals "skip API probing" (used for manifest-only suppliers
// like azure-openai / github-copilot).
//
// Examples:
//
//	baseURL="https://api.minimaxi.com/v1",  template="/models"   → "https://api.minimaxi.com/v1/models"
//	baseURL="https://api.anthropic.com",   template="/v1/models" → "https://api.anthropic.com/v1/models"
//	baseURL="https://ark.../api/v3",        template="/v3/models" → "https://ark.../api/v3/models"
//	baseURL=anything,                       template=""            → ""
//	template="https://custom.example.com/models" → "https://custom.example.com/models"
func BuildModelsURL(baseURL, template string) string {
	if template == "" {
		return ""
	}
	if strings.HasPrefix(template, "http://") || strings.HasPrefix(template, "https://") {
		return template
	}
	base := strings.TrimRight(baseURL, "/")
	return base + template
}
