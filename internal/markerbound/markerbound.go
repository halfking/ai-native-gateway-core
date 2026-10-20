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
	"sort"
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

// bareSpanPattern mirrors sanitize.looseBareMarkerSpanPattern: the brace-less
// alternative on its own. The re-scan inside a brace-delimited span needs it
// separately.
var bareSpanPattern = regexp.MustCompile(`\b(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`)

// foldedBareSpanPattern mirrors sanitize.looseFoldedBareMarkerSpanPattern: the
// bare form as the width-folded copy spells it, without the leading "\b".
//
// The boundary is judged on the ORIGINAL text instead (see
// bareSpanBoundaryOK): a full-width letter is not an ASCII word character, so
// the ASCII spelling glued to one has always been recognized, and the
// full-width spelling must not be treated more strictly than that.
var foldedBareSpanPattern = regexp.MustCompile(`(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`)

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
	for _, candidate := range spanCandidates(text) {
		span := [2]int{candidate[0], candidate[1]}
		text := text[span[0]:span[1]]
		if exactPattern.MatchString(text) {
			spans = append(spans, span)
			continue
		}
		if _, _, ok := parse(text); ok {
			spans = append(spans, span)
			continue
		}
		if looksLikeMarker(text) {
			spans = append(spans, span)
		}
	}
	if tail := unterminatedSpanPattern.FindString(text); tail != "" && looksLikeMarker(tail) {
		spans = append(spans, [2]int{len(text) - len(tail), len(text)})
	}
	return spans
}

// spanCandidates returns the candidate spans of text: what the span pattern
// matches, plus what the bare form matches in its full-width spelling.
//
// r1039 (2026-10-20): the bare form is also matched in its full-width
// spelling — a CJK input method writes "ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１" —
// and the span pattern only knows ASCII. The bare form is therefore scanned a
// second time on a width-folded copy of the text, and the offsets are mapped
// back onto the original.
//
// This mirrors security/sanitize's looseMarkerSpans; the two must stay
// identical or a truncation boundary can split a spelling this package
// recognizes but the other one does not (see the contract test in
// security/sanitize).
func spanCandidates(text string) [][2]int {
	spans := spanPattern.FindAllStringIndex(text, -1)
	out := make([][2]int, 0, len(spans))
	for _, s := range spans {
		out = append(out, [2]int{s[0], s[1]})
	}
	folded, ok := foldMarkerWidth(text)
	if !ok {
		return out
	}
	foldedSpans := foldedBareSpanPattern.FindAllStringIndex(folded, -1)
	if len(foldedSpans) == 0 {
		return out
	}
	mapped := mapFoldedSpans(text, foldedSpans)
	mapped = keepBareSpanBoundaries(text, mapped)
	all := make([][2]int, 0, len(out)+len(mapped))
	all = append(all, out...)
	all = append(all, mapped...)
	return mergeSpans(all)
}

// keepBareSpanBoundaries mirrors sanitize.keepBareSpanBoundaries: drop the
// spans whose start is preceded by an ASCII word character.
func keepBareSpanBoundaries(text string, spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return spans
	}
	out := spans[:0]
	for _, sp := range spans {
		if bareSpanBoundaryOK(text, sp[0]) {
			out = append(out, sp)
		}
	}
	return out
}

// bareSpanBoundaryOK mirrors sanitize.bareSpanBoundaryOK: the preceding
// character must not be an ASCII word character.
func bareSpanBoundaryOK(text string, off int) bool {
	if off <= 0 || off > len(text) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:off])
	switch {
	case r == '_':
		return false
	case r >= '0' && r <= '9':
		return false
	case r >= 'A' && r <= 'Z':
		return false
	case r >= 'a' && r <= 'z':
		return false
	}
	return true
}

// fullWidthMarkerASCII mirrors sanitize.fullWidthMarkerASCII: the ASCII byte
// the width fold maps r to, and whether r is a full-width form the marker
// grammar can match at all.
func fullWidthMarkerASCII(r rune) (byte, bool) {
	switch {
	case r == 0xFF0D: // －
		return '-', true
	case r >= 0xFF10 && r <= 0xFF19: // ０-９
		return byte(r - 0xFEE0), true
	case r == 0xFF1A: // ：
		return ':', true
	case r >= 0xFF21 && r <= 0xFF3A: // Ａ-Ｚ
		return byte(r - 0xFEE0), true
	case r == 0xFF3F: // ＿
		return '_', true
	case r >= 0xFF41 && r <= 0xFF5A: // ａ-ｚ
		return byte(r - 0xFEE0), true
	}
	return 0, false
}

// fullWidthLetterAt mirrors sanitize.fullWidthLetterAt: the ASCII letter a
// full-width form spells, read off the three bytes without decoding.
//
//	ＥＦ ＢＣ xx (xx = A1..BA) → Ａ-Ｚ → xx-0x60
//	ＥＦ ＢＤ xx (xx = 81..9A) → ａ-ｚ → xx-0x20
func fullWidthLetterAt[T ~string | ~[]byte](text T, i int) (byte, bool) {
	if i < 0 || i+3 > len(text) || text[i] != 0xEF {
		return 0, false
	}
	switch text[i+1] {
	case 0xBC: // Ａ-Ｚ
		if c := text[i+2]; c >= 0xA1 && c <= 0xBA {
			return c - 0x60, true
		}
	case 0xBD: // ａ-ｚ
		if c := text[i+2]; c >= 0x81 && c <= 0x9A {
			return c - 0x20, true
		}
	}
	return 0, false
}

// matchMarkerWordAnyWidthAt mirrors sanitize.matchMarkerWordAnyWidthAt:
// whether the marker word starts at byte offset i with **each character
// independently** spelled in ASCII or in the full-width form.
//
// 2026-10-21（r1039 续轮第三轮）：混拼形态（"ＳENSITIVE：…" / "SENSITIVＥ:…"）
// 在「全 ASCII 词」与「全全角词」两条门下**都不成立** ⇒ 这个副本与
// sanitize 一样看不见它 ⇒ 截断切点会把混拼标记切成两半 ⇒ 半截标记随上游跑掉。
// ⇒ 两个副本必须**同步**改，否则契约门（span 集合逐条比对）会分叉。
func matchMarkerWordAnyWidthAt[T ~string | ~[]byte](text T, i int) bool {
	const word = "sensitive"
	for k := 0; k < len(word); k++ {
		if i >= len(text) {
			return false
		}
		if text[i] < 0x80 {
			if asciiLower(text[i]) != word[k] {
				return false
			}
			i++
			continue
		}
		// 非 ASCII：只接受全角**字母**（Ａ-Ｚ / ａ-ｚ）
		letter, ok := fullWidthLetterAt(text, i)
		if !ok || asciiLower(letter) != word[k] {
			return false
		}
		i += 3
	}
	return true
}

// containsMarkerWordAnyWidth mirrors sanitize.containsMarkerWordAnyWidth:
// whether text contains the marker word in any mix of ASCII and full-width
// spellings.
func containsMarkerWordAnyWidth(text string) bool {
	for i := 0; i < len(text); {
		j := indexAnyByte(text[i:], 's', 'S')
		if j < 0 {
			break
		}
		i += j
		if matchMarkerWordAnyWidthAt(text, i) {
			return true
		}
		i++
	}
	for i := 0; i < len(text); {
		j := strings.IndexByte(text[i:], 0xEF)
		if j < 0 {
			break
		}
		i += j
		if matchMarkerWordAnyWidthAt(text, i) {
			return true
		}
		i++
	}
	return false
}

// indexAnyByte returns the index of the first occurrence of either byte in s,
// or -1. It is the two-byte twin of strings.IndexByte.
func indexAnyByte(s string, a, b byte) int {
	ia := strings.IndexByte(s, a)
	if a == b {
		return ia
	}
	ib := strings.IndexByte(s, b)
	if ib >= 0 && (ia < 0 || ib < ia) {
		return ib
	}
	return ia
}

// foldMarkerWidth mirrors sanitize.foldMarkerWidth: a copy of text in which
// every full-width form the marker grammar can match is rewritten to its ASCII
// spelling.
//
// The rewrite is one rune in, one rune out, so the folded copy has the same
// rune count as the original — but not the same byte count, so byte offsets
// found on the folded copy must be mapped back (see mapFoldedSpans).
//
// Nothing is rewritten unless the text actually mentions the marker word: the
// grammar cannot match anything else, and folding every text would put an
// O(n) copy on the path for no possible gain.
func foldMarkerWidth(text string) (string, bool) {
	if !containsMarkerWordAnyWidth(text) {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	for off := 0; off < len(text); {
		r, size := utf8.DecodeRuneInString(text[off:])
		if ascii, ok := fullWidthMarkerASCII(r); ok {
			b.WriteByte(ascii)
			changed = true
			off += size
			continue
		}
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(text[off])
			off += size
			continue
		}
		b.WriteString(text[off : off+size])
		off += size
	}
	if !changed {
		return "", false
	}
	return b.String(), true
}

// mapFoldedSpans mirrors sanitize.mapFoldedSpans: translate byte spans found
// in a width-folded copy of text back onto text's own byte offsets.
func mapFoldedSpans(text string, foldedSpans [][]int) [][2]int {
	if len(foldedSpans) == 0 {
		return nil
	}
	need := make([]int, 0, 2*len(foldedSpans))
	for _, sp := range foldedSpans {
		need = append(need, sp[0], sp[1])
	}
	mapped := make([]int, len(need))
	ti := 0
	origOff, foldOff := 0, 0
	for origOff < len(text) && ti < len(need) {
		r, size := utf8.DecodeRuneInString(text[origOff:])
		foldedSize := size
		if _, ok := fullWidthMarkerASCII(r); ok {
			foldedSize = 1
		}
		for ti < len(need) && need[ti] == foldOff {
			mapped[ti] = origOff
			ti++
		}
		origOff += size
		foldOff += foldedSize
	}
	for ; ti < len(need); ti++ {
		mapped[ti] = origOff
	}
	out := make([][2]int, 0, len(foldedSpans))
	for i := range foldedSpans {
		out = append(out, [2]int{mapped[2*i], mapped[2*i+1]})
	}
	return out
}

// mergeSpans mirrors sanitize.mergeMarkerSpans: merge candidate spans into one
// ascending, non-overlapping list. When two candidates overlap the wider one
// wins, so a full-width match is not cut short by the narrower ASCII one.
func mergeSpans(candidates [][2]int) [][2]int {
	if len(candidates) < 2 {
		return candidates
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i][0] != candidates[j][0] {
			return candidates[i][0] < candidates[j][0]
		}
		return candidates[i][1] > candidates[j][1]
	})
	out := candidates[:0]
	lastEnd := -1
	for _, sp := range candidates {
		if sp[0] < lastEnd {
			continue
		}
		out = append(out, sp)
		lastEnd = sp[1]
	}
	return out
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
//
// r1039: the full-width spelling of the marker word counts too — a CJK
// keyboard writes "ＳＥＮＳＩＴＩＶＥ：…" and the bare form of it must not
// slip past this filter.
func mayContainMarker(text string) bool {
	if strings.ContainsAny(text, "{｛") {
		return true
	}
	return containsMarkerWordAnyWidth(text)
}

// asciiLower folds one ASCII byte to lower case, leaving every other byte
// alone.
func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
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
