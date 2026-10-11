// Package markerbound adjusts truncation cut points so a gateway placeholder
// marker is never split in half.
//
// The compression pipeline truncates text at byte/rune boundaries in several
// places (summary input budgets, mechanical fallback summaries, Responses
// input halving). When the cut lands inside a {SENSITIVE:type:index} marker
// the surviving text carries a mangled fragment: the placeholder→value
// association is lost for that value, the fragment is an internal token the
// restore stage can no longer parse, and — once it has been upstream and
// back — it surfaces to the client verbatim or blocks the response. The cut
// helpers here move such a cut to the nearest span boundary so the marker
// survives whole on the kept side.
//
// The span grammar mirrors security/sanitize (placeholder.go +
// placeholder_loose.go). It is duplicated here because security/sanitize
// imports domains/hooks/compression, so compression-side callers cannot
// import sanitize back, and domains/transformation is a deliberate leaf
// package. Drift is pinned by TestMarkerBoundaryContractMatchesSanitizeGrammars
// in security/sanitize, which runs both implementations over a shared corpus
// and requires identical span sets.
package markerbound

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSpanBytes mirrors sanitize.loosePlaceholderMaxSpan: one mutated marker
// may occupy at most this many bytes between its brackets.
const maxSpanBytes = 96

const markerWord = "sensitive"

var exactPattern = regexp.MustCompile(`\{SENSITIVE:([a-z_]+):(\d+)\}`)

// spanPattern mirrors sanitize.loosePlaceholderSpanPattern: a bounded
// brace-delimited run in either brace width, or a brace-less bare form.
var spanPattern = regexp.MustCompile(
	`[\{｛][^{}｛｝]{0,` + strconv.Itoa(maxSpanBytes) + `}[\}｝]` +
		`|` + `\b(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`)

// unterminatedSpanPattern mirrors sanitize.unterminatedPlaceholderSpanPattern:
// a marker whose closing brace never arrived, anchored at the text end.
var unterminatedSpanPattern = regexp.MustCompile(
	`[\{｛][^{}｛｝]{0,` + strconv.Itoa(maxSpanBytes) + `}$`)

// grammar mirrors sanitize.loosePlaceholderGrammar.
var grammar = regexp.MustCompile(
	`^` + markerWord + `:*([a-z_](?:[a-z0-9_-]*[a-z_-])?):*([0-9]*)$`)

// Spans returns every candidate marker span in text, using the same
// recognition rules as security/sanitize's internal markerSpans: exact
// grammar, parseable re-spelling, degenerate echo, and a trailing
// unterminated fragment. Ordinary brace text is not a span.
func Spans(text string) [][2]int {
	if !mayContainMarker(text) {
		return nil
	}
	var spans [][2]int
	for _, match := range spanPattern.FindAllStringIndex(text, -1) {
		span := [2]int{match[0], match[1]}
		candidate := text[span[0]:span[1]]
		if exactPattern.MatchString(candidate) {
			spans = append(spans, span)
			continue
		}
		if _, _, ok := parse(candidate); ok {
			spans = append(spans, span)
			continue
		}
		if looksLikeMarker(candidate) {
			spans = append(spans, span)
		}
	}
	if tail := unterminatedSpanPattern.FindString(text); tail != "" && looksLikeMarker(tail) {
		spans = append(spans, [2]int{len(text) - len(tail), len(text)})
	}
	return spans
}

// SafeCutKeepHead guards a truncation that keeps text[:cut]. If cut lands
// strictly inside a marker span, the cut moves to the span end so the whole
// marker stays on the kept side. Any other cut is returned unchanged.
func SafeCutKeepHead(text string, cut int) int {
	return adjustCut(text, cut, +1)
}

// SafeCutKeepTail guards a truncation that keeps text[cut:]. If cut lands
// strictly inside a marker span, the cut moves to the span start so the whole
// marker stays on the kept side. Any other cut is returned unchanged.
func SafeCutKeepTail(text string, cut int) int {
	return adjustCut(text, cut, -1)
}

// SafeCutKeepTailRunes is SafeCutKeepTail for callers that count the cut in
// runes (multibyte-safe truncation). It returns the adjusted cut as a BYTE
// index for direct string slicing.
func SafeCutKeepTailRunes(text string, cutRunes int) int {
	if cutRunes <= 0 {
		return 0
	}
	byteCut := 0
	for i := 0; i < cutRunes && byteCut < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[byteCut:])
		byteCut += size
	}
	return SafeCutKeepTail(text, byteCut)
}

func adjustCut(text string, cut, keep int) int {
	if cut <= 0 || cut >= len(text) {
		return cut
	}
	for _, span := range Spans(text) {
		if span[0] < cut && cut < span[1] {
			if keep > 0 {
				return span[1]
			}
			return span[0]
		}
	}
	return cut
}

// mayContainMarker is the cheap negative filter Spans runs first: a text with
// no opening brace and no case-folded "sensitive" run cannot carry a marker
// in any spelling the span grammar recognizes (the brace-less bare form
// entered the grammar 2026-10-10 and must pass this filter too).
func mayContainMarker(text string) bool {
	if strings.ContainsAny(text, "{｛") {
		return true
	}
	return containsFoldASCII(text, markerWord)
}

func containsFoldASCII(text, word string) bool {
	n := len(word)
	for i := 0; i+n <= len(text); i++ {
		match := true
		for j := 0; j < n; j++ {
			c := text[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != word[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// parse mirrors sanitize.parseLoosePlaceholder: strict shape validation,
// width/invisible normalization, never a near-miss repair.
func parse(candidate string) (string, int, bool) {
	normalized := strings.TrimPrefix(normalize(candidate), "{")
	normalized = strings.TrimSuffix(normalized, "}")
	match := grammar.FindStringSubmatch(normalized)
	if match == nil {
		return "", 0, false
	}
	index, err := strconv.Atoi(match[2])
	if err != nil || index < 0 {
		index = -1
	}
	return strings.ReplaceAll(match[1], "-", "_"), index, true
}

// looksLikeMarker mirrors sanitize.looksLikeMarkerSpan: marker vocabulary
// plus the colon introducing an index, even when the grammar fails.
func looksLikeMarker(candidate string) bool {
	normalized := strings.TrimSuffix(strings.TrimPrefix(normalize(candidate), "{"), "}")
	if !strings.HasPrefix(normalized, markerWord) {
		return false
	}
	rest := normalized[len(markerWord):]
	trimmed := strings.TrimLeft(rest, ":")
	if trimmed == "" {
		return len(trimmed) != len(rest)
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if c == ':' {
			return true
		}
		if !(c >= 'a' && c <= 'z' || c == '_' || c == '-' || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return false
}

// normalize mirrors sanitize.normalizePlaceholder: fold full-width ASCII,
// drop invisible characters, lowercase.
func normalize(text string) string {
	interesting := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 0x80 || (c >= 'A' && c <= 'Z') || invisible(rune(c)) {
			interesting = true
			break
		}
	}
	if !interesting {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f', 0x00A0, 0x200B, 0x200C, 0x200D,
			0x2060, 0xFEFF:
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func invisible(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x00A0, 0x200B, 0x200C, 0x200D,
		0x2060, 0xFEFF:
		return true
	default:
		return false
	}
}
