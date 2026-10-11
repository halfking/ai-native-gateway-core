// Package sanitize - hop.go
//
// Gateway-behind-gateway sanitize passthrough.
//
// This gateway replaces sensitive values with {SENSITIVE:type:index} before a
// request leaves, and restores them when the response comes back. When the
// upstream it calls is another instance of the same gateway, the chain is:
//
//	downstream gateway -> upstream gateway -> model -> upstream gateway -> downstream
//
// The upstream instance sees markers it never issued. Its restore stage cannot
// resolve them, and its standing rule is to redact a marker it cannot resolve —
// because that is exactly what stops a model that invents "{SENSITIVE:secret:1}"
// from putting a session's real secret into a client-visible field. So it masks
// them, and the downstream peer's mapping is destroyed with them.
//
// This file adds the narrow exemption that fixes that chain without weakening
// that rule:
//
//   - A gateway stamps X-Kx-Sanitize-Hop on its outbound upstream request, but
//     only when it actually emitted placeholders.
//   - This instance scans the request it received and records the canonical
//     spelling of every marker that arrived.
//   - On the way back, a marker that resolves from this session's own map is
//     restored as always. A marker that does NOT resolve, but WAS observed
//     inbound, is handed back byte-for-byte for the peer that issued it.
//
// That last clause is the whole safety argument. Passthrough is granted per
// marker that demonstrably came in, never to a marker this gateway is merely
// unable to resolve. A marker that never arrived — the forgery case — is still
// redacted and still fails the residual-marker probes closed, so a model that
// invents a marker gains nothing it could not already write into its own prompt.
//
// The inbound set also preserves the fail-closed guarantee in hop mode: if this
// instance issued a marker and Redis expired before the response came back, that
// marker did not arrive inbound either — it was minted here — so it is still a
// residue and still blocks. Only markers that crossed a hop are exempted.
package sanitize

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// sanitizeHopNonce identifies this process to its peers. It is an
// observability label, not a credential: the exemption is granted by the
// inbound marker set, never by the nonce's value.
var sanitizeHopNonce = randomHopNonce()

func randomHopNonce() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf[:])
}

// SanitizeHopHeader is the header a gateway stamps on its outbound upstream
// request when it actually emitted placeholders. A peer that sees it knows the
// markers in this request belong to a downstream sanitize hop.
const SanitizeHopHeader = "X-Kx-Sanitize-Hop"

// SanitizeHopNonce returns this process's hop identifier.
func SanitizeHopNonce() string { return sanitizeHopNonce }

// maxForeignMarkers bounds the inbound marker set. A request carrying more
// distinct markers than this is not a normal sanitize hop, and an unbounded set
// would let a hostile caller inflate the exemption list.
const maxForeignMarkers = 256

// foreignMarkerSet is the restore stage's lookup form of the inbound markers.
// A nil map means "no exemption at all", so callers can treat nil as the
// feature being off.
type foreignMarkerSet map[string]struct{}

// CollectForeignMarkers returns the canonical spelling of every marker present
// in body. Duplicates collapse and the result is sorted so it is stable for
// logging and tests.
//
// Only the exact grammar is recognised. A marker arrives here because a peer
// gateway minted it, and a peer mints canonical tokens — the re-spellings a
// model produces happen downstream of this point and are handled by the
// ordinary restore stage.
func CollectForeignMarkers(body []byte) []string {
	if !strings.ContainsAny(string(body), "{｛") {
		return nil
	}
	seen := make(map[string]struct{})
	for _, marker := range PlaceholderPattern.FindAllString(string(body), -1) {
		if parsed, ok := ParsePlaceholder(marker); ok && parsed.Index >= 0 {
			seen[parsed.String()] = struct{}{}
		}
	}
	// A peer that already restores re-spelled markers can hand one back in a
	// mutated spelling. Recognize those too, so the exemption still matches the
	// canonical token the peer holds.
	for _, span := range loosePlaceholderSpanPattern.FindAllStringIndex(string(body), -1) {
		candidate := string(body[span[0]:span[1]])
		if PlaceholderPattern.MatchString(candidate) {
			continue
		}
		if typ, index, ok := parseLoosePlaceholder(candidate); ok && index >= 0 {
			seen[(Placeholder{Type: typ, Index: index}).String()] = struct{}{}
		}
	}
	if len(seen) == 0 || len(seen) > maxForeignMarkers {
		return nil
	}
	out := make([]string, 0, len(seen))
	for marker := range seen {
		out = append(out, marker)
	}
	sort.Strings(out)
	return out
}

// foreignMarkersFromBody returns the lookup set for an inbound request body.
// A peer must both declare the hop and actually carry markers; the header alone
// grants nothing, because a client can set any header it likes.
func foreignMarkersFromBody(body []byte) foreignMarkerSet {
	foreign := CollectForeignMarkers(body)
	if len(foreign) == 0 {
		return nil
	}
	set := make(foreignMarkerSet, len(foreign))
	for _, marker := range foreign {
		set[marker] = struct{}{}
	}
	return set
}

// isForeign reports whether a marker denotes one that arrived inbound from a
// downstream hop.
func (f foreignMarkerSet) isForeign(marker string) bool {
	if f == nil {
		return false
	}
	_, ok := f[marker]
	return ok
}

// isForeignPlaceholder is the type/index form used on the restore path, where
// the marker has already been parsed.
func (f foreignMarkerSet) isForeignPlaceholder(typ SensitiveType, index int) bool {
	if f == nil || index < 0 {
		return false
	}
	return f.isForeign((Placeholder{Type: typ, Index: index}).String())
}

// foreignCtxKey scopes the exemption to one request.
type foreignCtxKey struct{}

// WithForeignMarkers attaches an inbound marker set to ctx. Every restore entry
// point reads it from here rather than from a parameter, so the exemption
// applies uniformly without changing the public restore signatures.
func WithForeignMarkers(ctx context.Context, foreign foreignMarkerSet) context.Context {
	if foreign == nil {
		return ctx
	}
	return context.WithValue(ctx, foreignCtxKey{}, foreign)
}

// foreignMarkersFromContext returns the inbound marker set for this request,
// or nil when passthrough is not in effect.
func foreignMarkersFromContext(ctx context.Context) foreignMarkerSet {
	if ctx == nil {
		return nil
	}
	foreign, _ := ctx.Value(foreignCtxKey{}).(foreignMarkerSet)
	return foreign
}

// foreignMarkerSetFromHop converts the boundary-carried hop description into
// the restore stage's lookup set.
func foreignMarkerSetFromHop(hop response.SanitizeHop) foreignMarkerSet {
	if !hop.Enabled || len(hop.Foreign) == 0 {
		return nil
	}
	set := make(foreignMarkerSet, len(hop.Foreign))
	for _, marker := range hop.Foreign {
		set[marker] = struct{}{}
	}
	return set
}

// SanitizeHopFromRequest resolves the hop state for an inbound HTTP request: a
// peer's declaration, narrowed by the markers that actually arrived in the
// body. The HTTP boundary calls this and puts the result on
// InterceptRequest / StreamMeta.
func SanitizeHopFromRequest(header string, body []byte) response.SanitizeHop {
	foreign := CollectForeignMarkers(body)
	if strings.TrimSpace(header) == "" || len(foreign) == 0 {
		return response.SanitizeHop{}
	}
	return response.SanitizeHop{Enabled: true, Foreign: foreign}
}

// sanitizeHopCtxKey scopes the hop description to one request.
type sanitizeHopCtxKey struct{}

// withSanitizeHop records the hop description on the request context. The input
// middleware calls it; the HTTP boundary copies the value into
// InterceptRequest / StreamMeta.
func withSanitizeHop(ctx context.Context, hop response.SanitizeHop) context.Context {
	if !hop.Enabled || len(hop.Foreign) == 0 {
		return ctx
	}
	return context.WithValue(ctx, sanitizeHopCtxKey{}, hop)
}

// SanitizeHopFromContext returns the hop description recorded for this request.
func SanitizeHopFromContext(ctx context.Context) response.SanitizeHop {
	if ctx == nil {
		return response.SanitizeHop{}
	}
	hop, _ := ctx.Value(sanitizeHopCtxKey{}).(response.SanitizeHop)
	return hop
}

// AdoptSanitizeHop computes and records the hop state for an inbound request.
// It is the single place that decides whether this instance is behind a
// sanitize hop: the peer's declaration AND the markers that actually arrived
// must both be present. Gating on both is what keeps a client that sets the
// header itself from buying the passthrough exemption.
func AdoptSanitizeHop(ctx context.Context, header string, body []byte) context.Context {
	return withSanitizeHop(ctx, SanitizeHopFromRequest(header, body))
}

// SanitizeHopForOutbound reports the hop header value this instance should
// stamp on an upstream request. The declaration is needed both when this
// instance minted placeholders and when it is forwarding placeholders already
// validated as belonging to a downstream hop. The latter keeps the exemption
// alive through marker-only intermediate gateways.
func SanitizeHopForOutbound(ctx context.Context) string {
	if sm, ok := SanitizeMapFromContext(ctx); ok && len(sm) > 0 {
		return sanitizeHopNonce
	}
	// Only AdoptSanitizeHop can put this state on a request context, and it
	// requires both the peer header and markers actually present in the body.
	// A caller-controlled header by itself therefore cannot propagate the
	// declaration to another gateway.
	hop := SanitizeHopFromContext(ctx)
	if hop.Enabled && len(hop.Foreign) > 0 {
		return sanitizeHopNonce
	}
	return ""
}

// containsNonForeignMarker reports whether payload still carries a reserved
// marker that this gateway is responsible for. Markers that arrived from a
// downstream hop do not count — they are resolved by the peer that issued them.
//
// It also recognizes re-spelled markers, which the literal "{SENSITIVE:" test
// misses. That matters in both directions: without it a lowercased or full-width
// marker passes every probe in the package and lands on the client verbatim,
// and with it a marker this gateway cannot resolve still fails closed.
//
// Without an inbound set this reduces to "any marker at all", so a deployment
// without hop signalling keeps its previous behaviour.
func containsNonForeignMarker(ctx context.Context, payload []byte) bool {
	text := string(payload)
	if !mayContainMarkerText(text) {
		return false
	}
	spans := markerSpans(text)
	if len(spans) == 0 {
		return false
	}
	foreign := foreignMarkersFromContext(ctx)
	if foreign == nil {
		// No peer to defer to: any marker at all is ours to account for. This is
		// the pre-hop behaviour, now covering the re-spelled spellings too.
		return true
	}
	for _, span := range spans {
		typ, index, ok := parseClosedLoosePlaceholder(span)
		if !ok || index < 0 || !foreign.isForeignPlaceholder(typ, index) {
			return true
		}
	}
	return false
}

// fullWidthBraceLeadByte is the UTF-8 lead byte shared by '｛' (U+FF5B) and
// '｝' (U+FF5D). It is deliberately the shared byte rather than the two full
// code points: this is a negative filter, and being over-inclusive only costs
// the regex scan that follows.
const fullWidthBraceLeadByte = 0xEF

// mayCarryMarker is the cheap negative filter every marker probe runs first.
// A body with no opening brace — half or full width — and no case-folded
// "sensitive" run cannot carry a marker in any spelling, and the tolerant
// stage that replaced the old "{SENSITIVE:" byte search scans the whole
// payload with a regex. Most responses take this path, so it has to stay
// cheap rather than a regex scan.
//
// 2026-10-11: the brace-less bare form ("SENSITIVE:phone:1", observed on
// minimax-m3) entered the tolerated grammar on 2026-10-10, but this filter
// still keyed on braces only — a body whose markers were all bare passed
// every probe as clean, so the non-stream restorers (gated on
// placeholderCandidate) skipped the field and the residual guards blocked the
// whole response instead. The fold-case word scan keeps the filter honest for
// that spelling.
//
// 2026-10-11（本轮 12h 审计 A1）：把裸形认进过滤器是对的，但实现整轮换成了
// 朴素双重循环 + 一次 `string(payload)` 全量拷贝，实测在 16KB 真实响应体上
// 由 5.6ns/0-alloc 退化为 2477ns/12KB-alloc（~444×），512KB body 上退化为
// 2362470ns/524KB-alloc。mayCarryMarker 是每个 SSE 帧、每个响应体都要跑的
// 否定过滤（containsNonForeignMarker 13 个调用点 / hasReservedPlaceholder
// 9 个调用点），这正是本仓反复吃过亏的那类「为修漏判而放大常态成本」。
//
// 修法：保留裸形语义，把成本拉回 memchr 量级——两条 memchr 分别定位
// '{'/0xEF 与候选首字节，只在命中处做定长比较；全程不拷贝 body。
func mayCarryMarker(payload []byte) bool {
	if bytes.IndexByte(payload, '{') >= 0 || bytes.IndexByte(payload, fullWidthBraceLeadByte) >= 0 {
		return true
	}
	return containsFoldCaseWordBytes(payload, placeholderMarkerWord)
}

// mayContainMarkerText is mayCarryMarker for callers that already hold the
// payload as a string, so the conversion happens once per probe.
func mayContainMarkerText(text string) bool {
	if strings.ContainsAny(text, "{｛") {
		return true
	}
	return containsFoldCaseWord(text, placeholderMarkerWord)
}

// asciiLower folds one ASCII byte to lower case, leaving every other byte
// alone. The marker word is ASCII, so this is exact for our purposes and
// avoids allocating a lowercased copy of the payload.
func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// containsFoldCaseWord reports whether text contains word, ASCII
// case-insensitively, without allocating a lowercased copy.
//
// The first-byte gate is the whole point: strings.IndexByte is a vectorized
// memchr, while the naive form compared len(word) bytes at every offset. Only
// offsets whose first byte matches are worth a full comparison, and on ordinary
// model output there are none.
// foldCaseBytePair returns the lower- and upper-case spellings of an ASCII
// byte. For a non-letter they are the same byte.
func foldCaseBytePair(c byte) (lower, upper byte) {
	lower = asciiLower(c)
	upper = lower
	if 'a' <= lower && lower <= 'z' {
		upper = lower - ('a' - 'A')
	}
	return lower, upper
}

func containsFoldCaseWord(text, word string) bool {
	if word == "" {
		return true
	}
	n := len(word)
	lower, upper := foldCaseBytePair(word[0])
	for offset := 0; offset+n <= len(text); {
		idx := indexAnyByteString(text[offset:], lower, upper)
		if idx < 0 {
			return false
		}
		i := offset + idx
		offset = i + 1 // 严格前进 ⇒ 各次 memchr 扫描区间不重叠，总量 O(n)
		if i+n > len(text) {
			return false // 候选位够近但剩余长度不足 n，后面更不可能
		}
		match := true
		for j := 1; j < n; j++ {
			if asciiLower(text[i+j]) != word[j] {
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

// indexAnyByteString 返回 lower/upper 两个字节中「最早」出现的位置。
//
// 必须取最早而不是「先命中哪条就用哪条」：只取先命中的那条会让
// "SeNsItIvE" 因小写 s 在 index 3 命中而跳过 index 0 的大写 S，
// 把真实存在的裸形标记判成不存在（12h 审计 A1 修复过程中的差分自测实证）。
func indexAnyByteString(s string, lower, upper byte) int {
	lo := strings.IndexByte(s, lower)
	if lower == upper {
		return lo
	}
	up := strings.IndexByte(s, upper)
	if up >= 0 && (lo < 0 || up < lo) {
		return up
	}
	return lo
}

// containsFoldCaseWordBytes is containsFoldCaseWord for byte slices. It runs
// the same first-byte-gated comparison so callers holding a []byte (the common
// case on the response path) never pay for a string conversion of the body.
func containsFoldCaseWordBytes(text []byte, word string) bool {
	if word == "" {
		return true
	}
	n := len(word)
	lower, upper := foldCaseBytePair(word[0])
	for offset := 0; offset+n <= len(text); {
		idx := indexAnyByteSlice(text[offset:], lower, upper)
		if idx < 0 {
			return false
		}
		i := offset + idx
		offset = i + 1
		if i+n > len(text) {
			return false
		}
		match := true
		for j := 1; j < n; j++ {
			if asciiLower(text[i+j]) != word[j] {
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

func indexAnyByteSlice(s []byte, lower, upper byte) int {
	lo := bytes.IndexByte(s, lower)
	if lower == upper {
		return lo
	}
	up := bytes.IndexByte(s, upper)
	if up >= 0 && (lo < 0 || up < lo) {
		return up
	}
	return lo
}

// allMarkersForeign reports whether every marker in text was minted by a
// downstream hop, so this gateway has nothing left to resolve or mask in it.
// A value with no marker at all is not "all foreign" — callers use this to
// decide whether to release a frame it could not otherwise account for.
func allMarkersForeign(ctx context.Context, value any) bool {
	foreign := foreignMarkersFromContext(ctx)
	if foreign == nil {
		return false
	}
	markers, ok := collectMarkerSpansInValue(value, 0)
	if !ok || len(markers) == 0 {
		return false
	}
	return allMarkerSpansForeign(strings.Join(markers, " "), foreign)
}

// allMarkerSpansForeign reports whether every marker span in text resolves to a
// foreign marker. A span that does not parse — a degenerate echo such as
// {SENSITIVE:secret:X} — and a span with no closing brace are not foreign and
// keep failing closed.
func allMarkerSpansForeign(text string, foreign foreignMarkerSet) bool {
	spans := markerSpans(text)
	if len(spans) == 0 {
		return false
	}
	for _, span := range spans {
		typ, index, ok := parseClosedLoosePlaceholder(span)
		if !ok || index < 0 || !foreign.isForeignPlaceholder(typ, index) {
			return false
		}
	}
	return true
}

// markerSpans returns every brace-delimited candidate span that could be a
// marker, in either spelling. Bounded like the mask pass; spans that do not
// parse are returned too, so callers can classify them as corrupt rather than
// silently treat them as clean.
//
// 2026-10-11: the brace-only early bail predated the bare (brace-less) marker
// form and hid it from every probe built on this function
// (placeholderCandidate / hasReservedPlaceholder / containsNonForeignMarker);
// mayCarryMarker now admits the bare spelling and this function mirrors it.
func markerSpans(text string) []string {
	if !mayContainMarkerText(text) {
		return nil
	}
	var spans []string
	for _, span := range loosePlaceholderSpanPattern.FindAllStringIndex(text, -1) {
		candidate := text[span[0]:span[1]]
		if PlaceholderPattern.MatchString(candidate) {
			spans = append(spans, candidate)
			continue
		}
		if _, _, ok := parseLoosePlaceholder(candidate); ok {
			spans = append(spans, candidate)
			continue
		}
		// A span that names the marker but does not parse — a degenerate echo
		// such as {SENSITIVE:secret:X}. Keep it visible so callers fail closed
		// on it instead of treating an unaccounted span as clean.
		if looksLikeMarkerSpan(candidate) {
			spans = append(spans, candidate)
		}
	}
	// A trailing run with no closing brace is a truncated marker: the residual
	// probes must still see it, since a leaked half-token reaches the client
	// just as visibly as a whole one.
	if tail := unterminatedPlaceholderSpanPattern.FindString(text); tail != "" &&
		looksLikeMarkerSpan(tail) {
		spans = append(spans, tail)
	}
	return spans
}

// collectMarkerSpansInValue gathers marker spans from a decoded response tree.
func collectMarkerSpansInValue(value any, depth int) ([]string, bool) {
	if depth > maxNativeToolInputDepth {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		if !strings.ContainsAny(typed, "{｛") {
			return nil, true
		}
		return markerSpans(typed), true
	case map[string]any:
		var spans []string
		for _, child := range typed {
			got, ok := collectMarkerSpansInValue(child, depth+1)
			if !ok {
				return nil, false
			}
			spans = append(spans, got...)
		}
		return spans, true
	case []any:
		var spans []string
		for _, child := range typed {
			got, ok := collectMarkerSpansInValue(child, depth+1)
			if !ok {
				return nil, false
			}
			spans = append(spans, got...)
		}
		return spans, true
	default:
		return nil, true
	}
}
