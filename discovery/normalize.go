package discovery

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// NormalizeModelName is a thin wrapper around modelname.NormalizeRouteKey.
// The discovery package used to maintain a separate normalization with its
// own regex set; that implementation has been retired in favour of the
// modelname package's single source of truth (P1 of
// 2026-06-18-model-match-and-404-plan.md). Behaviour is identical for the
// case-insensitive vendor-prefix-stripping + date-stripping + dash-collapse
// pipeline; see modelname.NormalizeRouteKey for details.
func NormalizeModelName(raw string) string {
	return modelname.NormalizeRouteKey(raw)
}

// InferFamily returns the leading "family" segment of a normalized model
// name. The previous implementation had a hard-coded familyPatterns map
// (anthropic-claude / openai-gpt / zhipu-glm / minimax / moonshot / etc.)
// which had to be updated every time a new vendor was onboarded. The new
// generic form just takes the text before the first "-" — for the
// well-known families this is the same answer, and for new vendors it
// still produces a sensible bucket without any code change.
//
// Examples:
//
//	"mimo-v2.5-pro"     → "mimo"
//	"glm-5.1"           → "glm"
//	"claude-opus-4-6"   → "claude"
//	"minimax-m3"        → "minimax"
//	"qwen-max"          → "qwen"
//
// If the name has no "-", the whole name is returned.
func InferFamily(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "unknown"
	}
	if idx := strings.Index(name, "-"); idx > 0 {
		return name[:idx]
	}
	return name
}

// GenerateAliases produces the set of alternative names a (canonical_id,
// raw_model_name) pair should be reachable by. New aliases are written to
// model_aliases at sync time, and the SQL resolver in
// provider/client.go:loadCandidatesDB joins on them when the client's
// requested model name doesn't exactly match the offer's raw_model_name.
//
// The previous implementation had a GLM-specific block (4 extra aliases
// for cross-form names like "glm-4.7" / "glm-4-7" / "glm47"). That
// special case is gone — every model family now gets the same 5 aliases
// below. If a future family needs additional cross-form coverage, add
// the variants explicitly to model_aliases after the sync (e.g. via a
// dedicated "alias: model:foo -> model:bar" admin endpoint) rather than
// re-introducing family-specific code here.
func GenerateAliases(rawName, canonicalName string) []string {
	seen := make(map[string]bool)
	var aliases []string

	add := func(s string) {
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		aliases = append(aliases, s)
	}

	// (1) The raw name exactly as the upstream returned it.
	add(rawName)

	// (2) The canonical name (whatever NormalizeModelName produced).
	add(canonicalName)

	// (3) Raw name with vendor prefix stripped (scnet/minimax-m2.5 → minimax-m2.5).
	if idx := strings.LastIndex(rawName, "/"); idx >= 0 {
		add(rawName[idx+1:])
	}

	// (4) Dashes replaced with underscores (mimo-v2.5-pro → mimo_v2.5_pro).
	add(strings.ReplaceAll(rawName, "-", "_"))

	// (5) Underscores replaced with dashes (mimo_v2.5_pro → mimo-v2.5-pro).
	add(strings.ReplaceAll(rawName, "_", "-"))

	return aliases
}
