// Package sanitize - placeholder_loose.go
//
// Mutation-tolerant placeholder recognition.
//
// The gateway rewrites sensitive values (passwords, host addresses, keys) into
// {SENSITIVE:type:index} tokens before a request leaves, and restores them when
// the response comes back. A model does not echo those tokens back verbatim. It
// re-spaces them, re-cases them, reflows them across a newline, pads the index,
// or emits full-width punctuation from a CJK input method.
//
// Without this stage each of those misses the exact matcher, and the miss is
// not a quiet loss of one value. Measured on current main:
//
//	{SENSITIVE: server_ip : 1}      -> whole response blocked
//	{SENSITIVE:\n server_ip : 1\n}  -> whole response blocked
//	{SENSITIVE:server-ip:1}         -> whole response blocked
//	{SENSITIVE:server_ip:01}        -> [REDACTED], value lost
//	{sensitive:server_ip:1}         -> raw internal token leaks to the client
//	{ＳＥＮＳＩＴＩＶＥ：…１｝        -> raw internal token leaks to the client
//
// The last two are the worse half: the marker no longer starts with the literal
// "{SENSITIVE:", so it slips past every residual probe in the package and lands
// on the client verbatim.
//
// This file adds a tolerant stage. A mutated marker is normalized back to its
// canonical spelling and then looked up under exactly the same session map key
// the exact stage would have used.
//
// The security contract of the tolerant stage is that it can never surface a
// value the exact stage would have refused to surface:
//
//   - A normalized marker resolves only through a real mapping row for the
//     current (tenant, session). Nothing is inferred from position, ordering or
//     type similarity, so a fabricated marker still finds nothing.
//   - The type must be an identifier and the index must be digits to the end.
//     Neither is repaired into a neighbour, so "{...:secret:-1}" and
//     "{...:secret:1a}" stay corrupt instead of folding onto index 1.
//   - ":" is the only separator. Admitting "-" would map "{...:secret:-1}" onto
//     index 1 — that is a real bug this grammar is shaped around.
//   - Recognition is bounded to short brace-delimited spans, so ordinary model
//     output (JSON, code, prose braces) is never rewritten.
//
// What this deliberately does NOT do: repair a *semantic* rewrite, such as a
// model renaming "server_ip" to "ip". That is guessing rather than
// normalization, and it is ambiguous whenever a session holds more than one
// candidate. Those markers keep failing closed, exactly as before.
package sanitize

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	// loosePlaceholderMaxSpan bounds the bytes one mutated marker may occupy
	// between its brackets. The canonical token is ~30 bytes; a model that
	// pads it with spaces or full-width punctuation stays far below this. The
	// bound keeps the tolerant scan off large JSON/code regions.
	loosePlaceholderMaxSpan = 96

	// placeholderMarkerWord is the fixed marker vocabulary.
	placeholderMarkerWord = "sensitive"
)

// loosePlaceholderSpanPattern matches a short bracket-delimited run that could
// be a mutated marker. It is deliberately permissive about the *inside* of the
// brackets: parseLoosePlaceholder is the strict validator, so an over-broad
// span costs one failed validation instead of a rewrite.
//
// 2026-10-10: measured on 154, minimax-m3 also drops the *braces* entirely and
// writes a bare "SENSITIVE:phone" (sometimes "SENSITIVE:phone, SENSITIVE:email,
// SENSITIVE:id_card") inside reasoning_content. The brace-delimited alternative
// below never matches that, so the bare form reached the client verbatim. This
// is a structural rewrite, not the semantic renaming the package header declines
// to repair — it sits next to the already-supported case normalisation
// ("{sensitive:…}" for "{SENSITIVE:…}") — so it is in scope for the tolerant
// stage.
//
// The bare alternative is anchored on the marker word itself rather than on
// arbitrary text, which is what keeps prose out: "sensitive data", "sensitive:
// 12345" and "the sensitive field" never satisfy it, because the span must be
// followed by ":" plus a type identifier, and parseLoosePlaceholder is still
// the strict validator on top.
//
// The index is optional on purpose. Measured on 154, the model does not just
// strip the braces — it enumerates the types it was shown, as in
// "（SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）", with no index at
// all. A form that resolves to nothing still has to be masked rather than
// passed through, so the span pattern admits it and the strict parser decides
// what (if anything) it resolves to.
var loosePlaceholderSpanPattern = regexp.MustCompile(
	`[\{｛][^{}｛｝]{0,` + strconv.Itoa(loosePlaceholderMaxSpan) + `}[\}｝]` +
		`|` + looseBareMarkerSpanSource)

// looseBareMarkerSpanSource is the brace-less form on its own. The re-scan
// below needs it separately: inside a brace-delimited span the same span
// pattern would match the very run being consumed, so the re-scan would never
// make progress.
var looseBareMarkerSpanSource = `\b(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`

var looseBareMarkerSpanPattern = regexp.MustCompile(looseBareMarkerSpanSource)

// unterminatedPlaceholderSpanPattern matches the same bounded run anchored at the
// end of the text, i.e. a marker whose closing brace never arrived. A truncated
// marker is a leaked internal token just as much as a complete one, so the
// residual probes have to see it — but it can never be shown to belong to a peer
// hop, so it keeps failing closed.
var unterminatedPlaceholderSpanPattern = regexp.MustCompile(
	`[\{｛][^{}｛｝]{0,` + strconv.Itoa(loosePlaceholderMaxSpan) + `}$`)

// loosePlaceholderGrammar is the normalized marker grammar. normalizePlaceholder
// has already folded width, dropped invisible characters and lowercased, so
// this pattern only accepts what a mutated marker can legitimately look like:
//
//	sensitive:*<type>:*<index>
//
// A separator run of any length is tolerated because a model reflowing the token
// out of "key: value" context emits doubled colons, and a missing one is
// tolerated for the same reason. The type may contain "-", which folds back to
// "_" so "server-ip" resolves to the same type as "server_ip".
//
// The index is optional (2026-10-10): measured on 154, minimax-m3 enumerates
// the marker types it was shown — "（SENSITIVE:phone, SENSITIVE:email,
// SENSITIVE:id_card）" — with no index on any of them. Requiring digits there
// made the whole span unparseable, so restoreLooseMarkers skipped it and the
// internal token reached the client. A missing index parses as marker syntax
// with index -1, which already has a defined meaning downstream: it can never
// resolve to a mapping row, so mask-mode callers redact it and the value is
// dropped rather than leaked.
//
// The type group therefore ends on a non-digit ([a-z0-9_-]*[a-z_-], or a bare
// leading [a-z_]). Making it optional-index without that would regress the
// long-standing "missing separator" case: "{SENSITIVEsecret1}" must split into
// type=secret index=1, and a type class that may end in a digit swallows the
// "1" instead, because an empty index group no longer forces the split.
var loosePlaceholderGrammar = regexp.MustCompile(
	`^` + placeholderMarkerWord + `:*([a-z_](?:[a-z0-9_-]*[a-z_-])?):*([0-9]*)$`)

// placeholderResolver reports the plaintext for a recognized marker.
type placeholderResolver func(typ SensitiveType, index int) (string, bool)

// looseUnknownPolicy decides what happens to a marker that parses as a
// placeholder but has no mapping row.
type looseUnknownPolicy int

const (
	// looseKeepUnknown leaves the marker text untouched. This mirrors
	// RestoreOutput, whose contract is "never guess, never error".
	looseKeepUnknown looseUnknownPolicy = iota
	// looseMaskUnknown replaces the marker with [REDACTED]. This mirrors
	// RestoreOutputOrMask for client-visible text, so an unmapped marker
	// still cannot reach the client as an internal token.
	looseMaskUnknown
)

const redactedMarker = "[REDACTED]"

// RedactionMarker returns the client-visible mask for a known sensitive type:
// "[REDACTED:phone]", "[REDACTED:secret]", and so on.
//
// A bare [REDACTED] tells the reader that *something* was removed and nothing
// else, which is why "why is my proxy address masked?" and "was that a phone
// number or a key?" could not be answered from anything the gateway emitted.
// The type is not sensitive — it is the name of a rule, not the value it
// matched — and naming it makes the loss self-describing.
//
// A marker whose type is unknown or empty falls back to the bare form. That
// case is real, not hypothetical: maskDegenerateMarkerSpans and the
// over-length placeholder tail mask bytes before any type has been parsed, and
// inventing a type there would be a guess about bytes nobody resolved.
func RedactionMarker(typ SensitiveType) string {
	if typ == "" {
		return redactedMarker
	}
	return redactedMarker[:len(redactedMarker)-1] + ":" + string(typ) + "]"
}

// foldPlaceholderWidth maps full-width ASCII to half-width and the ideographic
// space to a plain space. A CJK input method turns "{SENSITIVE:secret:1}" into
// "{ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１}" without the model noticing.
func foldPlaceholderWidth(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E:
		return r - 0xFEE0
	case r == 0x3000:
		return ' '
	default:
		return r
	}
}

// placeholderInvisible reports whether a rune carries no token meaning and is
// therefore safe to drop. Models insert ordinary spaces freely; zero-width and
// bidi controls appear when text is pasted through a rich-text layer.
func placeholderInvisible(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x00A0, 0x200B, 0x200C, 0x200D,
		0x2060, 0xFEFF:
		return true
	default:
		return false
	}
}

// normalizePlaceholder folds width, drops invisible characters and lowercases
// so that every tolerated spelling of one marker compares equal. Text with no
// non-ASCII byte, no uppercase letter and no invisible character — the common
// case for ordinary response text — returns unchanged without allocating.
func normalizePlaceholder(text string) string {
	interesting := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 0x80 || (c >= 'A' && c <= 'Z') || placeholderInvisible(rune(c)) {
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
		r = foldPlaceholderWidth(r)
		if placeholderInvisible(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// trimSpanQuoteWrap strips a wrapping layer of quote characters — `"`, `'` and
// the JSON-escaped `\"` — from the inside of a brace-delimited marker span.
//
// 2026-10-14 (r1030): MiniMax-M3 was observed emitting tool-call arguments
// whose password field carried the marker re-quoted as
// `"password": "{\"SENSITIVE:secret:1\"}"`. The model tried to echo the
// placeholder but wrapped it in an extra quote layer inside the JSON string;
// the grammar rejected the quote bytes, the restore pass missed, and the
// output-compliance layer then correctly refused the whole stream for an
// unmapped credential-looking value. Tolerating exactly one wrapping quote
// layer lets the real value reach the client; the grammar still gates
// everything after the trim, so ordinary JSON such as `{"a":1}` cannot parse.
//
// The trim is BOTH-EDGES-ONLY: a quote at one end alone (r1022's
// `{sensitive xyz stuff"}` — model prose about a marker, ending in a quote)
// must keep failing the grammar. normalizePlaceholder already drops spaces,
// so `{sensitive xyz stuff}` parses as a garbage-type marker and is masked
// whole-span; the lone trailing quote is what keeps its quoted sibling OUT of
// that path, and trimming it would swallow the span's non-marker tail.
const spanQuoteWrapCutset = "\\\"'"

func trimSpanQuoteWrap(inner string) string {
	if len(inner) < 2 {
		return inner
	}
	isQ := func(b byte) bool { return b == '"' || b == '\'' || b == '\\' }
	if isQ(inner[0]) && isQ(inner[len(inner)-1]) {
		return strings.Trim(inner, spanQuoteWrapCutset)
	}
	return inner
}

// parseLoosePlaceholder validates one candidate span and returns the type and
// index it denotes. It is strict about shape and never repairs a near miss.
//
// ok reports whether the span is marker syntax at all. index is -1 when the
// digits do not fit an int (a corrupt marker, not a real one): such a span is
// still marker syntax, so mask-mode callers still redact it, but it can never
// resolve to a mapping row.
func parseLoosePlaceholder(candidate string) (SensitiveType, int, bool) {
	normalized := strings.TrimPrefix(normalizePlaceholder(candidate), "{")
	normalized = strings.TrimSuffix(normalized, "}")
	normalized = trimSpanQuoteWrap(normalized)
	match := loosePlaceholderGrammar.FindStringSubmatch(normalized)
	if match == nil {
		return "", 0, false
	}
	index, err := strconv.Atoi(match[2])
	if err != nil || index < 0 {
		index = -1
	}
	return SensitiveType(strings.ReplaceAll(match[1], "-", "_")), index, true
}

// placeholderCandidate reports whether text carries an internal marker in any
// spelling: exact, re-spelled, or a degenerate echo whose grammar is broken
// ({SENSITIVE:secret:X}). All three must be recognized here — this is both the
// gate before a restore pass and the residual probe that fails closed
// afterwards, and a degenerate marker that only the exact grammar matched would
// otherwise slip past every probe in the package and reach the client verbatim.
//
// Ordinary model output (JSON, code, prose braces) does not parse and is left
// alone; the degenerate branch below requires marker-shaped content, so a
// sentence about a "{sensitive_value} field" is not mistaken for a token.
func placeholderCandidate(text string) bool {
	return len(markerSpans(text)) > 0
}

// looksLikeMarkerSpan reports whether a span carries marker shape even though it
// fails the strict grammar — the degenerate echo class. It requires the fixed
// marker vocabulary, a well-formed type run, and the colon that introduces the
// index, which is what separates "{SENSITIVE:secret:X}" from ordinary prose that
// merely happens to contain the word inside braces.
func looksLikeMarkerSpan(candidate string) bool {
	normalized := strings.TrimSuffix(strings.TrimPrefix(normalizePlaceholder(candidate), "{"), "}")
	normalized = trimSpanQuoteWrap(normalized)
	if !strings.HasPrefix(normalized, placeholderMarkerWord) {
		return false
	}
	rest := normalized[len(placeholderMarkerWord):]
	trimmed := strings.TrimLeft(rest, ":")
	if trimmed == "" {
		// "{SENSITIVE::}" — nothing but separators; degenerate, not prose.
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

// parseClosedLoosePlaceholder is the form the hop probes use. An unterminated
// span parses to a perfectly good (type, index), but it is not a complete
// marker: nothing about it can be matched against what a peer actually minted,
// so the exemption must not stretch to it.
func parseClosedLoosePlaceholder(candidate string) (SensitiveType, int, bool) {
	normalized := normalizePlaceholder(candidate)
	if !strings.HasPrefix(normalized, "{") || !strings.HasSuffix(normalized, "}") {
		return "", 0, false
	}
	return parseLoosePlaceholder(candidate)
}

// looseRestoreStats reports what one tolerant pass did, so observability and the
// rewrite share a single decision and cannot drift apart.
//
// passthrough is tracked apart from unresolved on purpose: a marker handed back
// verbatim for a downstream hop was neither recovered nor masked, and counting
// it as unresolved would log a "masking" warning and inflate a metric for a
// marker this gateway never owned.
type looseRestoreStats struct {
	text        string
	recovered   int
	unresolved  int
	passthrough int
}

// restoreLooseMarkers rewrites every mutated marker in text that the resolver
// recognizes. Exact markers are handled by the caller's exact pass first, so
// this pass only ever sees text the exact grammar rejected.
// maxLooseRescanDepth bounds the re-scan loop below. Each level strictly
// shrinks the text (the span is a proper substring), so termination is
// structural, but the cap keeps a pathological input from turning the pass
// into deep recursion on the request path.
const maxLooseRescanDepth = 4

func restoreLooseMarkers(ctx context.Context, text string, resolve placeholderResolver, foreign foreignMarkerSet, policy looseUnknownPolicy) looseRestoreStats {
	return restoreLooseMarkersDepth(ctx, text, resolve, foreign, policy, 0)
}

func restoreLooseMarkersDepth(ctx context.Context, text string, resolve placeholderResolver, foreign foreignMarkerSet, policy looseUnknownPolicy, depth int) looseRestoreStats {
	return rewriteLooseSpans(ctx, text, resolve, foreign, policy, depth,
		loosePlaceholderSpanPattern.FindAllStringIndex(text, -1))
}

func rewriteLooseSpans(ctx context.Context, text string, resolve placeholderResolver, foreign foreignMarkerSet, policy looseUnknownPolicy, depth int, spans [][]int) looseRestoreStats {
	if len(spans) == 0 {
		return looseRestoreStats{text: text}
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	changed := false
	stats := looseRestoreStats{}
	for _, span := range spans {
		candidate := text[span[0]:span[1]]
		typ, index, ok := parseLoosePlaceholder(candidate)
		if !ok {
			// The span was matched but is not itself a marker. That happens
			// when a brace-delimited run swallows more than one marker, the
			// common case being a JSON document whose *value* is a bare form:
			// {"phone":"SENSITIVE:phone"} is captured whole by the brace
			// branch, so skipping it outright would leave the inner marker
			// unscanned and let it reach the client. Re-scan inside the span
			// and splice the result back so the surrounding JSON survives.
			if depth >= maxLooseRescanDepth {
				continue
			}
			// Re-scan with the brace-less pattern only: the full pattern would
			// re-match the run being consumed and never terminate the descent.
			inner := rewriteLooseSpans(ctx, candidate, resolve, foreign, policy, depth+1,
				looseBareMarkerSpanPattern.FindAllStringIndex(candidate, -1))
			if inner.text != candidate {
				b.WriteString(text[last:span[0]])
				b.WriteString(inner.text)
				last = span[1]
				changed = true
				stats.recovered += inner.recovered
				stats.unresolved += inner.unresolved
				stats.passthrough += inner.passthrough
			}
			continue
		}
		if foreign.isForeignPlaceholder(typ, index) {
			// The inbound peer owns this marker. Do not even consult the local
			// resolver: shared session maps may contain the same token key.
			stats.passthrough++
			continue
		}
		replacement := ""
		resolved := false
		if resolve != nil {
			replacement, resolved = resolve(typ, index)
		}
		switch {
		case resolved:
			stats.recovered++
			changed = true
		case policy == looseMaskUnknown:
			stats.unresolved++
			replacement, changed = redactedMarker, true
		default:
			stats.unresolved++
			continue
		}
		b.WriteString(text[last:span[0]])
		b.WriteString(replacement)
		last = span[1]
	}
	if !changed {
		return looseRestoreStats{text: text, recovered: stats.recovered, unresolved: stats.unresolved, passthrough: stats.passthrough}
	}
	b.WriteString(text[last:])
	stats.text = b.String()
	return stats
}

// resolverFor returns a resolver backed by a session mapping table.
func (sm SanitizeMap) resolverFor() placeholderResolver {
	return func(typ SensitiveType, index int) (string, bool) {
		value, ok := sm[(Placeholder{Type: typ, Index: index}).String()]
		return value, ok
	}
}

// validLoosePlaceholderPrefix reports whether candidate — the bytes from the
// last '{' in a streamed delta to its end — is a prefix of a marker in any
// tolerated spelling. Streamed deltas split a marker mid-token, and the bytes
// after the split are not a marker until the next delta arrives. Holding them
// is what keeps a mutated marker from reaching the client half-restored.
func validLoosePlaceholderPrefix(candidate string) bool {
	normalized := normalizePlaceholder(candidate)
	if !strings.HasPrefix(normalized, "{") {
		return false
	}
	// A closing brace means the marker already landed; the restore pass owns
	// it. Holding it here would stall a complete token.
	if strings.ContainsRune(normalized, '}') {
		return false
	}
	// Arriving prefixes trim quotes from EITHER edge independently: the tail
	// `{\"SENSITIVE:secret:` has a leading quote layer with no closing quote
	// yet, and `{\"SENSITIVE:secret:1\"` carries its closing quote before the
	// brace arrives. Both must keep holding. (Completion always re-parses the
	// full span, where the both-edges trim above governs.)
	rest := strings.TrimRight(strings.TrimLeft(normalized[1:], spanQuoteWrapCutset), spanQuoteWrapCutset)
	if rest == "" {
		// `{"` / `{\"` at the very end: a quote-wrapped marker (r1030
		// re-quoted echo) may still be arriving. Hold the bytes; the
		// over-length release valve in restoreStreamStringField keeps a
		// long ordinary JSON string from being redacted by this hold.
		return true
	}
	if len(rest) >= len(placeholderMarkerWord) {
		if rest[:len(placeholderMarkerWord)] != placeholderMarkerWord {
			return false
		}
		rest = rest[len(placeholderMarkerWord):]
	} else {
		return strings.HasPrefix(placeholderMarkerWord, rest)
	}
	// The grammar is "sensitive:*<type>:*<index>". The leading colon run may be
	// empty, doubled or still arriving. A type never contains a colon, so the
	// next colon ends it — and everything past that colon must already be
	// digits, otherwise this is a corrupt marker rather than one still
	// arriving.
	rest = strings.TrimLeft(rest, ":")
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == ':' {
			return allPlaceholderDigits(rest[i+1:])
		}
		if !(c >= 'a' && c <= 'z' || c == '_' || c == '-' || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func allPlaceholderDigits(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// reservedMarkerStarted reports whether withheld delta bytes opened the marker
// grammar in any tolerated spelling. Callers use it to decide between releasing
// the bytes verbatim and redacting them: once the marker word has begun, a
// released prefix would put an internal token on the wire.
func reservedMarkerStarted(held string) bool {
	normalized := normalizePlaceholder(held)
	return strings.HasPrefix(normalized, "{"+placeholderMarkerWord) ||
		strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(normalized, "{"), spanQuoteWrapCutset), placeholderMarkerWord)
}

// reserveMutatedPlaceholders lifts the high-water mark past any re-spelled
// marker in client-supplied text. The input side must reserve these for the
// same reason it reserves exact markers: a marker the client echoes back with
// altered spacing must not alias a credential the gateway allocates later in
// this turn.
func reserveMutatedPlaceholders(text string, offsets map[SensitiveType]int) {
	if offsets == nil || !strings.ContainsAny(text, "{｛") {
		return
	}
	for _, span := range loosePlaceholderSpanPattern.FindAllStringIndex(text, -1) {
		candidate := text[span[0]:span[1]]
		if PlaceholderPattern.MatchString(candidate) {
			continue // already reserved by the exact pass
		}
		typ, index, ok := parseLoosePlaceholder(candidate)
		if ok && index > offsets[typ] {
			offsets[typ] = index
		}
	}
}
