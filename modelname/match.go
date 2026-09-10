// match.go — provider-model ↔ standard-model (models_canonical) matching.
//
// Provider-supplied raw model names are frequently NOT verbatim copies of
// the standard catalog name. Two recurring shapes motivated this matcher
// (2026-09-10):
//
//	"cluade/opus-5"  → "claude-opus-5"   vendor prefix holds the family
//	                                     (misspelled) and must be RE-JOINED,
//	                                     not stripped; typo tolerated
//	"grok/4.6"       → "grok-4.6"        prefix holds the family, the base
//	                                     is only the version segment
//
// NormalizeRouteKey alone cannot bridge either case: it throws the prefix
// away ("opus-5" / "4.6"), which is exactly the "simply take a part of the
// raw name" behaviour this matcher replaces. Matching here is always done
// against the actual standard-model names supplied by the caller (i.e. the
// models_canonical catalog), never against transformations of the raw name.
package modelname

import (
	"sort"
	"strings"
)

// AutoLinkThreshold is the minimum MatchStandardModels score for a match
// to be considered confident enough to link a provider model to a standard
// model automatically (discovery ingestion / default suggestion).
// "cluade-opus-5" → "claude-opus-5" scores 0.917; "grok-4.6" → "grok-4.6"
// scores 1.0; different-generation names ("o4-mini" → "o3-mini") are
// rejected by the digit guard no matter how small the edit distance is.
const AutoLinkThreshold = 0.85

// StandardModelMatch is one ranked candidate of MatchStandardModels.
type StandardModelMatch struct {
	// Name is the standard (canonical) model name as supplied by the caller.
	Name string
	// Score is 0..1, higher is better. 1.0 = exact match of the re-joined
	// raw name; 0.9+ = cross-form / bounded typo match; ≤0.9 = token
	// containment (e.g. vendor-prefixed NIM names).
	Score float64
}

// matchScoreFloor drops candidates that share no meaningful signal with the
// raw name from the ranked output entirely.
const matchScoreFloor = 0.30

// fuzzCap bounds the typo-tolerance credit so a fuzzy match can never
// outrank an exact or cross-form match of a different candidate.
const fuzzCap = 0.96

// fuzzMin is the minimum normalized Damerau-Levenshtein similarity for the
// fuzzy (typo) channel to contribute at all.
const fuzzMin = 0.80

// containmentWeight scales the token-containment channel; even a perfect
// containment match stays below the typo channel so exact names win.
const containmentWeight = 0.90

// BestStandardModelMatch returns the highest-scoring standard model for
// rawName, or nil when no candidate reaches matchScoreFloor.
func BestStandardModelMatch(rawName string, canonicalNames []string) *StandardModelMatch {
	ranked := MatchStandardModels(rawName, canonicalNames)
	if len(ranked) == 0 {
		return nil
	}
	return &ranked[0]
}

// MatchStandardModels ranks every canonical name against rawName and
// returns candidates scoring above matchScoreFloor, best first. Ties break
// toward the shorter name (the base model beats its "-thinking" variants)
// and then alphabetically for determinism.
func MatchStandardModels(rawName string, canonicalNames []string) []StandardModelMatch {
	prefix, base := splitVendorPrefix(rawName)
	joined := joinVendorBase(prefix, base)

	matches := make([]StandardModelMatch, 0, len(canonicalNames)/4+1)
	for _, c := range canonicalNames {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if s := scoreCandidate(joined, base, c); s > matchScoreFloor {
			matches = append(matches, StandardModelMatch{Name: c, Score: s})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if len(matches[i].Name) != len(matches[j].Name) {
			return len(matches[i].Name) < len(matches[j].Name)
		}
		return matches[i].Name < matches[j].Name
	})
	return matches
}

// splitVendorPrefix splits "z-ai/glm-5.2" into ("z-ai", "glm-5.2") using
// the LAST slash, matching StripProviderPrefix. Names without a slash get
// an empty prefix.
func splitVendorPrefix(raw string) (prefix, base string) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:])
	}
	return "", s
}

// joinVendorBase re-attaches the vendor prefix as a family token:
// ("cluade", "opus-5") → "cluade-opus-5", ("grok", "4.6") → "grok-4.6".
// Runs of separators collapse so prefixes like "meta/" or bases with
// underscores normalize cleanly.
func joinVendorBase(prefix, base string) string {
	s := base
	if prefix != "" {
		s = prefix + "-" + base
	}
	s = dupDashPattern.ReplaceAllString(s, "-")
	return strings.Trim(s, "-_ ")
}

// punctKey normalizes version punctuation so the dot and dash spellings of
// the same version compare equal: "grok-4.6" and "grok-4-6" both become
// "grok-4-6". Only '.' is mapped — family separators are left alone.
func punctKey(s string) string {
	return strings.ReplaceAll(s, ".", "-")
}

// digitKey keeps only 0-9 so version-digit differences (a NEW model
// generation) can be distinguished from letter typos: "o4-mini" → "4" vs
// "o3-mini" → "3" differ, while "cluade-opus-5" and "claude-opus-5" both
// reduce to "5".
func digitKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// scoreCandidate scores one standard-model name against the raw side.
//
// Channels (highest wins):
//  1. exact re-joined match                                → 1.0
//  2. exact base-only match (raw == canonical, no prefix)  → 0.99
//  3. punctuation-insensitive (cross-form) match           → 0.98 / 0.97
//  4. bounded typo match (Damerau-Levenshtein ≥ fuzzMin,
//     same version digits)                                 → sim * fuzzCap
//  5. token containment (vendor-prefixed NIM-style names)  → ratio * containmentWeight
func scoreCandidate(joined, base, canonical string) float64 {
	c := strings.ToLower(strings.TrimSpace(canonical))

	if joined != "" && joined == c {
		return 1.0
	}
	if base != "" && base == c {
		return 0.99
	}
	if joined != "" && punctKey(joined) == punctKey(c) {
		return 0.98
	}
	if base != "" && punctKey(base) == punctKey(c) {
		return 0.97
	}

	best := 0.0
	// Typo channel — only when the version digits agree, so "cluade" →
	// "claude" passes but "o4" → "o3" (a different generation) never does.
	for _, rawForm := range []string{joined, base} {
		if rawForm == "" {
			continue
		}
		if digitKey(rawForm) != digitKey(c) {
			continue
		}
		sim := damerauSimilarity(rawForm, c)
		if sim >= fuzzMin {
			if s := sim * fuzzCap; s > best {
				best = s
			}
		}
	}

	// Token-containment channel — covers long vendor-qualified raw names
	// ("anthropic/claude-opus-5") whose re-joined form is too far from the
	// catalog name for the typo channel, yet whose tokens fully cover it.
	cTokens := tokenize(c)
	if len(cTokens) > 0 {
		rawTokens := map[string]bool{}
		for _, t := range tokenize(joined) {
			rawTokens[t] = true
		}
		for _, t := range tokenize(base) {
			rawTokens[t] = true
		}
		hit := 0
		for _, t := range cTokens {
			if rawTokens[t] {
				hit++
			}
		}
		ratio := float64(hit) / float64(len(cTokens))
		if ratio >= 0.5 {
			if s := ratio * containmentWeight; s > best {
				best = s
			}
		}
	}
	return best
}

// tokenize splits a normalized name on non-alphanumeric characters.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// damerauSimilarity is 1 - dist/max(len) using the optimal string
// alignment variant of Damerau-Levenshtein, where a transposition of two
// adjacent characters costs 1 (the "cluade"/"claude" typo shape).
func damerauSimilarity(a, b string) float64 {
	d := optimalStringAlignment(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(d)/float64(maxLen)
}

func optimalStringAlignment(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev2 := make([]int, lb+1)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			m := min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				if t := prev2[j-2] + 1; t < m {
					m = t
				}
			}
			cur[j] = m
		}
		copy(prev2, prev)
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
