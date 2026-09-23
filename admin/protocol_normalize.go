package admin

// protocol_normalize.go — normalize user-supplied provider protocol values
// before they are persisted to providers.protocol / provider_catalog.protocol.
//
// Why: providers.protocol has NO DB CHECK constraint (unlike
// provider_catalog), so a mistyped value such as "openai-response"
// (singular) is silently accepted at insert time and then falls into the
// executor's default chat-completions branch — the request "works" but the
// configured Responses transport never engages, and the admin UI shows a
// protocol the rest of the system does not recognize. Normalizing known
// aliases at the admin write boundary turns such configs into the canonical
// enum and rejects unknown ones with an explicit 400 instead.
//
// Canonical values mirror the provider_catalog CHECK constraint:
// openai-completions / openai-responses / anthropic-messages /
// gemini-generate / ollama-native.

import (
	"fmt"
	"strings"
)

// canonicalProviderProtocols is the authoritative enum (same set as the
// provider_catalog protocol CHECK constraint).
var canonicalProviderProtocols = []string{
	"openai-completions",
	"openai-responses",
	"anthropic-messages",
	"gemini-generate",
	"ollama-native",
}

// providerProtocolAliases maps accepted spellings/aliases to canonical
// values. Keys are compared lower-cased with '_' folded to '-'.
var providerProtocolAliases = map[string]string{
	// openai-completions (chat) family
	"openai":                 "openai-completions",
	"chat":                   "openai-completions",
	"openai-chat":            "openai-completions",
	"openai-completion":      "openai-completions",
	"openai-chatcompletion":  "openai-completions",
	"openai-chat-completion": "openai-completions",
	"chat-completions":       "openai-completions",
	"chatcompletions":        "openai-completions",

	// openai-responses family — the singular "openai-response" is the
	// exact misspelling observed in the 2026-09-23 vapeur incident.
	"openai-response":     "openai-responses",
	"response":            "openai-responses",
	"responses":           "openai-responses",
	"openai-response-api": "openai-responses",

	// anthropic family
	"anthropic":          "anthropic-messages",
	"anthropic-message":  "anthropic-messages",
	"claude":             "anthropic-messages",
	"claude-messages":    "anthropic-messages",
	"anthropic-messages": "anthropic-messages",

	// gemini family
	"gemini":          "gemini-generate",
	"google-gemini":   "gemini-generate",
	"gemini-generate": "gemini-generate",

	// ollama family
	"ollama":        "ollama-native",
	"ollama-native": "ollama-native",
}

// normalizeProtocolKey folds a raw protocol string into alias-lookup form:
// trim space, lower-case, underscores → hyphens.
func normalizeProtocolKey(raw string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "_", "-")
}

// NormalizeProviderProtocol maps a user-supplied protocol value to the
// canonical enum. Already-canonical inputs pass through unchanged; known
// aliases are mapped; anything else is rejected with an error listing the
// accepted values so the caller can return a 400.
func NormalizeProviderProtocol(raw string) (string, error) {
	key := normalizeProtocolKey(raw)
	if key == "" {
		return "", fmt.Errorf("protocol is required (one of: %s)", strings.Join(canonicalProviderProtocols, ", "))
	}
	for _, canonical := range canonicalProviderProtocols {
		if key == canonical {
			return canonical, nil
		}
	}
	if mapped, ok := providerProtocolAliases[key]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("unknown protocol %q (one of: %s)", raw, strings.Join(canonicalProviderProtocols, ", "))
}
