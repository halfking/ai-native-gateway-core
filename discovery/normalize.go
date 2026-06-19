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

// vendorCanonicalFamilies maps the leading-token of a normalized model
// name to the canonical family id that should be used in
// models_canonical.family / model_families.id. The collapse eliminates
// historical family-id splits that arose because (a) the legacy Python
// llm-gateway + admin UI used vendor-prefixed ids like
// "anthropic-claude" / "openai-gpt" / "meta-llama" / "google-gemini" /
// "zhipu-glm", while (b) the Go rewrite's InferFamily (introduced
// during the 2026-06-18 P1 normalize refactor) returned the bare
// token ("claude" / "gpt" / "llama" / "gemini" / "glm").  The two
// naming schemes co-existed in the DB, so a family-chip filter in
// /models page returned 1 model for "Anthropic" instead of the 21
// claude models that should match (2026-06-20 user report).
//
// We do NOT collapse token differences that have *intra-vendor*
// meaning: qwen / qwen2 / qwen3 / qwen3.5 / qwen3.6 stay separate
// (different model generations), gemma stays separate from gemini
// (Google's gemma line is a distinct family), and unknown tokens fall
// through to the bare token so a brand-new vendor still works without
// a code change.
//
// 2026-06-20: re-introduced after the 2026-06-18 refactor removed the
// previous hard-coded map. The previous version was an if/else
// cascade; this is a single map for the same effect.
var vendorCanonicalFamilies = map[string]string{
	// Anthropic — all "claude-*" collapse to "anthropic-claude"
	"claude": "anthropic-claude",
	// OpenAI — gpt-*, o1/o3/o4 collapse to "openai-gpt"
	"gpt":  "openai-gpt",
	"o1":   "openai-gpt",
	"o3":   "openai-gpt",
	"o4":   "openai-gpt",
	// Meta — llama/llama2/llama3 collapse to "meta-llama"
	"llama":  "meta-llama",
	"llama2": "meta-llama",
	"llama3": "meta-llama",
	// Google — bare "gemini" → "google-gemini" (gemma is its own family)
	"gemini": "google-gemini",
	// Mistral AI — ministral / mistral / mixtral all collapse to "mistral"
	"mistral":   "mistral",
	"ministral": "mistral",
	"mixtral":   "mistral",
	// Zhipu AI — bare "glm" → "zhipu-glm"
	"glm": "zhipu-glm",
	// Moonshot AI — kimi is the moonshot family, "moonshot" stays
	"kimi":     "moonshot",
	"moonshot": "moonshot",
	// StepFun — bare "step" → "stepfun"
	"step":    "stepfun",
	"stepfun": "stepfun",
}

// CanonicalizeFamilyID takes a raw family id (as it might be stored in
// the DB from either the legacy Python admin UI or the Go
// InferFamily) and returns the vendor-prefixed canonical form. If the
// input is not a known split, the input is returned unchanged.
//
// Examples:
//
//	"claude"           → "anthropic-claude"
//	"anthropic-claude" → "anthropic-claude"  (idempotent)
//	"minimax"          → "minimax"           (no split, passthrough)
func CanonicalizeFamilyID(family string) string {
	if f, ok := vendorCanonicalFamilies[family]; ok {
		return f
	}
	return family
}

// InferFamily returns the canonical family id for a normalized model
// name. The previous generic form (just the leading token) was
// correct for families with a 1:1 leading-token mapping (mimo, glm,
// minimax, qwen) but produced the wrong id for vendors whose
// canonical name prefix differs from their family id (claude-*
// should map to "anthropic-claude" not "claude", gpt-* to
// "openai-gpt" not "gpt", etc.).  This now consults
// vendorCanonicalFamilies first and falls back to the bare token.
//
// Examples:
//
//	"mimo-v2.5-pro"     → "mimo"
//	"glm-5.1"           → "zhipu-glm"
//	"claude-opus-4-6"   → "anthropic-claude"
//	"gpt-5.4"           → "openai-gpt"
//	"o3-mini"           → "openai-gpt"
//	"minimax-m3"        → "minimax"
//	"qwen-max"          → "qwen"
//
// If the name has no "-" the whole name is returned.  An empty /
// whitespace input returns "unknown".
// canonicalFamilyIDs is the inverse of vendorCanonicalFamilies: the
// set of vendor-prefixed canonical ids (values of the map).  Used to
// recognise an already-canonical name passed to InferFamily (e.g.
// "anthropic-claude" / "openai-gpt" / "meta-llama"), which the admin
// UI may have stored verbatim.  Built once at package init.
var canonicalFamilyIDs = func() map[string]bool {
	set := make(map[string]bool, len(vendorCanonicalFamilies))
	for _, v := range vendorCanonicalFamilies {
		set[v] = true
	}
	return set
}()

func InferFamily(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "unknown"
	}
	// Already canonical? (e.g. "anthropic-claude" passed in — the
	// legacy admin UI / Python path may have already stored it in
	// the vendor-prefixed form).
	if canonicalFamilyIDs[name] {
		return name
	}
	if idx := strings.Index(name, "-"); idx > 0 {
		first := name[:idx]
		if canonical, ok := vendorCanonicalFamilies[first]; ok {
			return canonical
		}
		return first
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
	return modelname.GenerateAliasVariants(rawName, canonicalName)
}
