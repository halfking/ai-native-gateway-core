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
//
// 2026-09-11 audit hardening:
//   - full token containment only scores ≥ AutoLinkThreshold when the
//     catalog name is a contiguous TOKEN SUFFIX of the raw name (the
//     vendor-qualified shape "anthropic/claude-opus-5"). Suffix VARIANTS
//     ("claude-opus-5-thinking" when only "claude-opus-5" exists) are
//     capped below the threshold so they keep seeding their own standard
//     row instead of silently folding into the base model.
//   - when a raw name carries a vendor prefix, a base-exact candidate
//     ("opus-5", e.g. a junk row seeded by the old prefix-stripping code)
//     is DEMOTED below a confident typo/cross-form re-joined match
//     ("claude-opus-5") whose name has the base name as a token suffix.
package modelname

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// AutoLinkThreshold is the minimum MatchStandardModels score for a match
// to be considered confident enough to link a provider model to a standard
// model automatically (discovery ingestion / default suggestion).
// "cluade-opus-5" → "claude-opus-5" scores 0.886 ((1-1/13)*fuzzCap);
// "grok-4.6" → "grok-4.6" scores 1.0; different-generation names
// ("o4-mini" → "o3-mini") are rejected by the digit guard no matter how
// small the edit distance is.
const AutoLinkThreshold = 0.85

// matchForm attributes which side of the raw name produced a score:
// the vendor-prefix-re-joined form or the bare base form.
type matchForm uint8

const (
	formJoined matchForm = iota
	formBase
)

// scoredMatch is an internal candidate with its channel attribution.
type scoredMatch struct {
	Name  string
	Score float64
	Form  matchForm
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

// containmentCap limits the containment channel for candidates that are NOT
// suffix-aligned (see scoreCandidate): kept below AutoLinkThreshold so
// "-thinking" / "-fast" style variants never auto-fold into their base
// model and keep seeding their own standard row.
const containmentCap = 0.84

// BestStandardModelMatch returns the highest-scoring standard model for
// rawName, or nil when no candidate reaches matchScoreFloor.
func BestStandardModelMatch(rawName string, canonicalNames []string) *StandardModelMatch {
	ranked := MatchStandardModels(rawName, canonicalNames)
	if len(ranked) == 0 {
		return nil
	}
	return &ranked[0]
}

// StandardModelMatch is one ranked candidate of MatchStandardModels.
type StandardModelMatch struct {
	// Name is the standard (canonical) model name as supplied by the caller.
	Name string
	// Score is 0..1, higher is better. 1.0 = exact match of the re-joined
	// raw name; 0.9+ = cross-form / bounded typo match; ≤0.9 = token
	// containment (e.g. vendor-prefixed NIM names).
	Score float64
}

// MatchStandardModels ranks every canonical name against rawName and
// returns candidates scoring above matchScoreFloor, best first. Ties break
// toward the shorter name (the base model beats its "-thinking" variants)
// and then alphabetically for determinism.
//
// Ranking is not purely score-ordered in one case: when the raw name has a
// vendor prefix and the top candidate is a bare base-exact match (e.g. the
// junk "opus-5" row the pre-2026-09-10 prefix-stripping code seeded) while
// a confident typo/cross-form re-joined match exists ("claude-opus-5"),
// the joined match is promoted above the base-exact one (see demote below).
func MatchStandardModels(rawName string, canonicalNames []string) []StandardModelMatch {
	prefix, base := splitVendorPrefix(rawName)
	// 2026-09-21: a trailing "-cn" token is upstream distribution noise, not
	// a model variant — the 2026-09-20/21 canonical audit found kling/wan/
	// viduq3/qwen "-cn" rows whose base never existed anywhere, with zero
	// traffic and zero routes, re-seeded from the provider feed minutes after
	// every manual cleanup. Strip it before scoring so a re-reported "x-cn"
	// auto-links to its base canonical instead of seeding a duplicate row.
	// Deliberate variants ("-thinking"/"-fast") keep seeding their own rows:
	// containmentCap stays below AutoLinkThreshold for them.
	base = stripCNSuffix(base)
	joined := joinVendorBase(prefix, base)

	scored := make([]scoredMatch, 0, len(canonicalNames)/4+1)
	for _, c := range canonicalNames {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if s, form := scoreCandidate(prefix, joined, base, c); s > matchScoreFloor {
			scored = append(scored, scoredMatch{Name: c, Score: s, Form: form})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if len(scored[i].Name) != len(scored[j].Name) {
			return len(scored[i].Name) < len(scored[j].Name)
		}
		return scored[i].Name < scored[j].Name
	})

	demotePrefixJunkMatch(scored)
	// Re-sort after demotion so ordering stays monotonic in Score.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	matches := make([]StandardModelMatch, len(scored))
	for i, m := range scored {
		matches[i] = StandardModelMatch{Name: m.Name, Score: m.Score}
	}
	return matches
}

// demotePrefixJunkMatch resolves the two vendor-prefix ordering conflicts:
//
// Rule 1 (top is a bare base-exact row, e.g. the junk "opus-5" the
// pre-2026-09-10 prefix-stripping code seeded): when a confident typo or
// cross-form re-joined match exists ("claude-opus-5") whose name ends with
// the base row's tokens, the prefix is real family signal — demote the
// base row below the joined match.
//
// Rule 2 (top is an EXACT joined hit, e.g. "z-ai-glm-5.2" for raw
// "z-ai/glm-5.2"): the vendor slug is distribution metadata, not model
// identity — the established convention is the base name, so an exact
// slug-prefixed row yields to its own base row when one exists.
func demotePrefixJunkMatch(scored []scoredMatch) {
	if len(scored) < 2 {
		return
	}
	top := scored[0]

	// Rule 2: exact slug-prefixed row → its base row wins.
	if top.Form == formJoined && top.Score >= 0.99 {
		for i := 1; i < len(scored); i++ {
			m := scored[i]
			if m.Form == formBase && isStrictTokenSuffix(m.Name, top.Name) {
				scored[0].Score = m.Score - 0.01
				scored[0], scored[i] = scored[i], scored[0]
				return
			}
		}
		return
	}

	// Rule 1: junk base row → confident fuzzy/cross-form re-join wins.
	if top.Form != formBase {
		return
	}
	for i := 1; i < len(scored); i++ {
		m := scored[i]
		// Only typo/cross-form re-joins carry the "the prefix was the
		// family, misspelled/punctuated" signal. An EXACT joined hit is
		// handled by rule 2 above.
		if m.Form != formJoined || m.Score < AutoLinkThreshold || m.Score >= 0.99 {
			continue
		}
		if isStrictTokenSuffix(top.Name, m.Name) {
			// Demote just below the joined match so ordering stays honest
			// about which candidate the matcher now prefers.
			scored[0].Score = m.Score - 0.01
			scored[0], scored[i] = scored[i], scored[0]
			return
		}
	}
}

// stripCNSuffix drops one trailing "-cn" token ("kling-v2-1-cn" →
// "kling-v2-1"). A bare "cn" is left alone.
func stripCNSuffix(base string) string {
	if len(base) > 3 && strings.HasSuffix(base, "-cn") {
		return base[:len(base)-3]
	}
	return base
}

// isStrictTokenSuffix reports whether suffix is a proper token-level suffix
// of name ("opus-5" ⊂ "claude-opus-5") and strictly shorter.
func isStrictTokenSuffix(suffix, name string) bool {
	st := tokenize(suffix)
	sn := tokenize(name)
	if len(st) == 0 || len(st) >= len(sn) {
		return false
	}
	tail := sn[len(sn)-len(st):]
	for i := range st {
		if st[i] != tail[i] {
			return false
		}
	}
	return true
}

// SplitVendorPrefix splits "z-ai/glm-5.2" into ("z-ai", "glm-5.2") using
// the LAST slash, matching StripProviderPrefix. Names without a slash get
// an empty prefix.
//
// Exported for tooling that must apply the exact same vendor-prefix rule as
// the matcher (e.g. the junk-canonical governance tool in
// scripts/govern-junk-canonical); the lowercase result is intentional.
func SplitVendorPrefix(raw string) (prefix, base string) {
	return splitVendorPrefix(raw)
}

// JoinVendorBase re-attaches a vendor prefix as a family token, the exact
// inverse shape SplitVendorPrefix produced: ("z-ai", "glm-5.2") →
// "z-ai-glm-5.2". Exported alongside SplitVendorPrefix for the same
// tooling reason.
func JoinVendorBase(prefix, base string) string {
	return joinVendorBase(prefix, base)
}

// IsStrictTokenSuffix reports whether suffix is a proper token-level suffix
// of name ("opus-5" ⊂ "claude-opus-5") and strictly shorter — the shape the
// pre-2026-09-10 prefix-stripping discovery code produced when it seeded
// junk standard rows. Exported for the junk-canonical governance tool so
// its detector cannot drift from the matcher's tokenization.
func IsStrictTokenSuffix(suffix, name string) bool {
	return isStrictTokenSuffix(suffix, name)
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

// scoreCandidate scores one standard-model name against the raw side and
// attributes which raw form produced the score.
//
// Channels (highest wins):
//  1. exact re-joined match                                → 1.0   (joined)
//  2. exact base-only match (raw == canonical, no prefix)  → 0.99  (base)
//  3. punctuation-insensitive (cross-form) match           → 0.98/0.97
//  4. bounded typo match (Damerau-Levenshtein ≥ fuzzMin,
//     same version digits)                                 → sim * fuzzCap
//  5. token containment (vendor-prefixed NIM-style names)  → ratio * containmentWeight,
//     capped at containmentCap unless the catalog name is a contiguous
//     token suffix of the raw name (true vendor-qualified shape).
func scoreCandidate(prefix, joined, base, canonical string) (float64, matchForm) {
	c := strings.ToLower(strings.TrimSpace(canonical))

	if joined != "" && joined == c {
		return 1.0, formJoined
	}
	if base != "" && base == c {
		return 0.99, formBase
	}
	if joined != "" && punctKey(joined) == punctKey(c) {
		return 0.98, formJoined
	}
	if base != "" && punctKey(base) == punctKey(c) {
		return 0.97, formBase
	}

	best := 0.0
	form := formBase
	// Typo channel — only when the version digits agree, so "cluade" →
	// "claude" passes but "o4" → "o3" (a different generation) never does.
	for _, fc := range []struct {
		raw  string
		form matchForm
	}{
		{joined, formJoined},
		{base, formBase},
	} {
		if fc.raw == "" {
			continue
		}
		if digitKey(fc.raw) != digitKey(c) {
			continue
		}
		sim := damerauSimilarity(fc.raw, c)
		if sim >= fuzzMin {
			if s := sim * fuzzCap; s > best {
				best = s
				form = fc.form
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
			s := ratio * containmentWeight
			// Suffix-aligned containment = genuine vendor-qualified name
			// ("anthropic" + "claude-opus-5"); anything else (raw carries
			// extra variant tokens like "-thinking"/"-fast") is capped so
			// it never crosses AutoLinkThreshold.
			if !tokenSuffixAligned(cTokens, tokenize(joined)) && s > containmentCap {
				s = containmentCap
			}
			if s > best {
				best = s
				if prefix != "" {
					form = formJoined
				} else {
					form = formBase
				}
			}
		}
	}
	return best, form
}

// tokenSuffixAligned reports whether want is a contiguous token suffix of
// have ("claude-opus-5" at the tail of "anthropic-claude-opus-5").
func tokenSuffixAligned(want, have []string) bool {
	if len(want) == 0 || len(want) > len(have) {
		return false
	}
	tail := have[len(have)-len(want):]
	for i := range want {
		if want[i] != tail[i] {
			return false
		}
	}
	return true
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
// Lengths are counted in RUNES to match the distance, which is computed
// over runes — byte lengths would overstate similarity for non-ASCII names.
func damerauSimilarity(a, b string) float64 {
	d := optimalStringAlignment(a, b)
	maxLen := utf8.RuneCountInString(a)
	if n := utf8.RuneCountInString(b); n > maxLen {
		maxLen = n
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
