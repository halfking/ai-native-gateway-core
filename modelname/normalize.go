// Package modelname is the single source of truth for model-name canonicalization
// in llm-gateway-go. It does **no** family-specific rewrites (no GLM dot↔dash,
// no MiniMax m\d+ joining, no Claude tier-first dot insertion) — see the
// design doc at docs/llm-gateway-go/2026-06-18-model-match-and-404-plan.md
// (P2: zero hard-coded family rules). Cross-form matching (e.g. client asks
// for "claude-opus-4-6" but the upstream returns "claude-opus-4.6") is handled
// at the SQL layer through model_aliases, not here.
package modelname

import (
	"regexp"
	"strings"
)

var (
	// strip [-_]YYYYMMDD or [-_]YYYY-MM-DD from end of name
	dateSuffixPattern = regexp.MustCompile(`(?i)([-_])20\d{2}[-_]?\d{2}[-_]?\d{2}$`)
	// strip [-_]YYMMDD from end of name
	shortDatePattern = regexp.MustCompile(`(?i)([-_])\d{6}$`)
	// strip " [1M]" / " [2M]" etc.
	oneMSuffixPattern = regexp.MustCompile(`(?i)\s*\[(1m|\d+m)\]$`)
	// collapse runs of dashes/underscores to a single dash
	dupDashPattern = regexp.MustCompile(`[-_]{2,}`)

	featureMap = map[string]bool{
		"highspeed": true, "thinking": true, "reasoning": true,
		"flash": true, "turbo": true, "preview": true,
		"pro": true, "max": true, "mini": true, "nano": true,
		"chat": true, "instruct": true, "coder": true, "code": true,
		"vision": true, "audio": true, "air": true,
	}
)

// NormalizeRouteKey is the canonical form used as a routing/lookup key.
//
// What it does (deterministic, no family heuristics):
//
//	trim whitespace, lowercase
//	strip vendor prefix (text after the last "/")
//	strip " [1M]" / " [2M]" suffix
//	strip YYYYMMDD / YYMMDD date suffix
//	collapse runs of "-" / "_" to a single "-"
//	trim leading/trailing "-", "_", " "
//
// What it does NOT do (use model_aliases / SQL lookup instead):
//
//	convert "glm-4-7" ↔ "glm-4.7"        (dot vs dash in versions)
//	convert "minimax-m2-7" ↔ "minimax-m2.7"
//	convert "claude-opus-4-6" ↔ "claude-opus-4.6"
//	strip the family "m" prefix from "minimax-m2.7"
//	alias feature suffixes ("flash" / "air" / "thinking")
func NormalizeRouteKey(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	if model == "" {
		return ""
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	model = oneMSuffixPattern.ReplaceAllString(model, "")
	model = dateSuffixPattern.ReplaceAllString(model, "")
	model = shortDatePattern.ReplaceAllString(model, "")
	model = dupDashPattern.ReplaceAllString(model, "-")
	return strings.Trim(model, "-_ ")
}

// NormalizeModelRef is kept for backwards compatibility with any in-flight
// external callers. It returns (provider, baseModel, version) where
// baseModel == NormalizeRouteKey(model minus the provider prefix) and
// version is always "" (no version extraction — see package doc).
func NormalizeModelRef(model string) (provider string, baseModel string, version string) {
	model = strings.TrimSpace(model)
	if idx := strings.Index(model, "/"); idx >= 0 {
		provider = model[:idx]
		model = model[idx+1:]
	}
	baseModel = NormalizeRouteKey(model)
	return provider, baseModel, ""
}

// ExtractFeatures returns the recognized feature tokens (e.g. "flash",
// "thinking", "air") that appear in the model name. Used only for UI
// display/labeling — NOT for routing decisions. Routing uses the strict
// equality test in MatchModelOffer.
func ExtractFeatures(model string) []string {
	norm := NormalizeRouteKey(model)
	tokens := regexp.MustCompile(`[^a-z0-9]+`).Split(norm, -1)
	var features []string
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token != "" && len(token) >= 3 && featureMap[token] {
			features = append(features, token)
		}
	}
	return features
}

// MatchModelOffer is the strict-equality test used by the Go-side candidate
// filter (which is being phased out in favor of SQL-side matching, but is
// kept here for any non-SQL code paths that still need it).
//
// Cross-form matching (e.g. "claude-opus-4-6" vs "claude-opus-4.6", or
// "minimax-m2-7" vs "minimax-m2.7") is intentionally NOT done here — the
// SQL resolver joins against model_aliases which is populated at sync time.
func MatchModelOffer(clientModel, offerModel string) bool {
	return NormalizeRouteKey(clientModel) == NormalizeRouteKey(offerModel)
}

// StripProviderPrefix removes the provider prefix from a model name
// while preserving the original casing. Used for route matching and display —
// NOT for upstream request bodies; those must use the offer raw_model_name
// (see routing.resolveOutboundModel).
//
//	"z-ai/glm-5.1" → "glm-5.1"
//	"scnet/minimax-m2.5" → "minimax-m2.5"
//	"MiniMax-M3" → "MiniMax-M3" (no prefix, unchanged)
func StripProviderPrefix(rawName string) string {
	model := strings.TrimSpace(rawName)
	if model == "" {
		return ""
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	return model
}

// NormalizeRouteKeyAliases returns the set of safe variant forms a
// model name should be matched against, in priority order:
//
//	(1) the canonicalized form (e.g. "claude-sonnet-4.6")
//	(2) dash form (replace each '.' with '-')      ("claude-sonnet-4-6")
//	(3) dot form   (replace each '-' with '.')     ("claude.sonnet.4.6")
//	(4) original-cased (preserving upper/lower from the input)
//
// The first form is preferred; the rest are escape hatches for cases
// where the operator stored the canonical / alias in a different
// punctuation style.  Variants are deduped case-insensitively.
//
// Use this when the SQL resolver needs to walk the cross-form
// matching matrix (canonical + alias lookups, both against
// models_canonical.canonical_name and model_aliases.raw_name).
//
// 2026-06-19 audit: previous comments said "dot↔dash cross-form is
// intentionally NOT done in Go — use model_aliases instead".  In
// practice, model_aliases is rarely populated with the dot-form
// variants for vendor-supplied canonical names like
// "claude-sonnet-4-6" / "claude-sonnet-4.6", so resolve queries
// silently missed.  We now do the variant walk in Go at resolve time
// AND let discovery populate the alias table (see discovery/discovery.go).
func NormalizeRouteKeyAliases(model string) []string {
	base := NormalizeRouteKey(model)
	if base == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	// Always emit the canonical normalized form first so exact matches
	// still win on the very first SQL hit (no wasted round trips).
	add(base)
	// Dot ↔ dash bridge. We run both substitutions against both forms
	// so the resolver can bridge "claude-sonnet-4.6" ↔ "claude-sonnet-4-6"
	// in either direction without needing a pre-populated alias.
	add(strings.ReplaceAll(base, ".", "-"))   // dot → dash (canonical DB form)
	add(strings.ReplaceAll(base, "-", "."))   // dash → dot (some clients use dots)
	add(strings.ReplaceAll(strings.ReplaceAll(base, ".", "-"), "-", "."))
	add(strings.ReplaceAll(strings.ReplaceAll(base, "-", "."), ".", "-"))
	if strings.TrimSpace(model) != base {
		// Preserve the original case for callers that store mixed-case
		// canonical names (e.g. "MiniMax-M2.7").  Lowercase the
		// comparison so it still matches case-insensitive lookups.
		add(strings.TrimSpace(model))
	}
	return out
}

// StandardizeName is an alias for NormalizeRouteKey. The split between
// these two functions existed to apply family-specific dot/dash rewrites
// (GLM / MiniMax / Claude); that logic has been removed — see package doc.
// Keeping the name as an alias lets existing call sites continue to compile
// without churn.
func StandardizeName(rawName string) string {
	return NormalizeRouteKey(rawName)
}
