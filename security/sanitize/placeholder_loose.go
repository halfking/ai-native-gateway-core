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
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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

// looseFoldedBareMarkerSpanSource is the bare form as the **width-folded**
// copy of the text spells it. It differs from looseBareMarkerSpanSource in
// exactly one place: the leading "\b" is gone.
//
// 理由：边界必须按**原文**判断。原文里的 "\b" 看到的是 ASCII 词字符，而全角
// 字母**不是** ASCII 词字符——所以 "ＡＢＣＤsensitive:phone" 在 ASCII 形态下
// 一直是**被认出来的**（既有的 "\b(?i:sensitive)" 在原文上成立）。若把边界
// 判在折宽副本上，全角字母会变成 ASCII 词字符，于是
// "ＡＢＣＤＳＥＮＳＩＴＩＶＥ：…" 反而**认不出来**——那正是本轮要堵的洞
// （内部标记原样到达客户端）。
//
// 边界改由调用方按原文判断（见 bareSpanBoundaryOK）。结尾的 "\b" 保留：它决定
// 类型/索引串的**长度**，而那一段必须与 ASCII 形态逐字一致。
var looseFoldedBareMarkerSpanSource = `(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`

var looseFoldedBareMarkerSpanPattern = regexp.MustCompile(looseFoldedBareMarkerSpanSource)

// bareSpanBoundaryOK reports whether a span found on the width-folded copy may
// start where it does.
//
// The rule is the one the ASCII pattern's "\b" applies to the **original**
// text: the preceding character must not be an ASCII word character. A
// full-width letter is not an ASCII word character, so a full-width marker
// glued to one is still a marker — exactly like the ASCII spelling glued to a
// full-width letter, which the original pattern has always accepted.
func bareSpanBoundaryOK(text string, off int) bool {
	r, _, ok := runeBeforeStart(text, off)
	if !ok {
		return true // start of text
	}
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

// unterminatedPlaceholderSpanPattern matches the same bounded run anchored at the
// end of the text, i.e. a marker whose closing brace never arrived. A truncated
// marker is a leaked internal token just as much as a complete one, so the
// residual probes have to see it — but it can never be shown to belong to a peer
// hop, so it keeps failing closed.
var unterminatedPlaceholderSpanPattern = regexp.MustCompile(
	`[\{｛][^{}｛｝]{0,` + strconv.Itoa(loosePlaceholderMaxSpan) + `}$`)

// ---------------------------------------------------------------------------
// r1039 — 全角（无花括号）标记
// ---------------------------------------------------------------------------
//
// 症状：模型在 CJK 输入法下把标记写成全角且**不带花括号**，
//
//	your token is ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１ ok
//
// 花括号形态（｛ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１｝）早就被覆盖了——花括号分支
// 的字符类接受全角花括号，解析侧 normalizePlaceholder 也会折宽。缺口恰恰是
// 「全角**且**无花括号」这一格：
//
//   - span pattern 的裸形态分支只认 ASCII 标记词、ASCII 冒号、ASCII 类型/索引；
//   - 便宜门 mayContainMarkerText / containsFoldCaseWord 也只认 ASCII 词，
//     于是 markerSpans 在正则跑起来之前就提前返回了。
//
// 结果是内部标记原样到达客户端。修法有两条，都必须守住同一个约束：**还原
// 改写是按原文字节偏移做的**（rewriteLooseSpans 切 text[span[0]:span[1]]），
// 而折宽是「一 rune 换一 rune」、**不是**一字节换一字节（全角 ｓ 是 3 字节，
// 's' 是 1 字节）。所以不能拿折宽副本上的字节偏移直接用：必须映射回原文。
//
// 折宽只折**文法真正看得见的那些全角码位**（标记词、分隔冒号、类型串、索引），
// 其余全角标点折了也没有任何分支会看它，却会让每个中文响应都白付一次拷贝。

// fullWidthMarkerASCII returns the ASCII byte the width fold maps r to, and
// whether r is a full-width form the marker grammar can match at all.
//
// Only the forms the grammar can actually match are folded:
//
//	－  (U+FF0D) → '-'     ＿  (U+FF3F) → '_'
//	０-９(U+FF10-19) → '0'-'9'   ：  (U+FF1A) → ':'
//	Ａ-Ｚ(U+FF21-3A) → 'A'-'Z'   ａ-ｚ(U+FF41-5A) → 'a'-'z'
//
// The rest of the full-width block (， 。 ！ …) is punctuation no branch of the
// grammar looks at: folding it would rewrite bytes nothing reads and would put
// an O(n) copy on the hot path for every Chinese response.
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

// fullWidthLetterAt returns the ASCII letter a full-width form spells and
// whether the three bytes at offset i are a full-width letter at all.
//
// A full-width letter is always three bytes, so the check is a fixed number of
// byte comparisons and needs no decoding:
//
//	ＥＦ ＢＣ xx (xx = A1..BA) → Ａ-Ｚ → xx-0x60
//	ＥＦ ＢＤ xx (xx = 81..9A) → ａ-ｚ → xx-0x20
//
// It is written over a type parameter so the byte-slice and the string variant
// of the gate share one implementation and neither of them allocates.
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

// matchMarkerWordAnyWidthAt reports whether the marker word starts at byte
// offset i, with **each of its characters independently** spelled either in
// ASCII or in the full-width form.
//
// 2026-10-21（r1039 续轮第三轮）：**半角全角混拼**此前两种门都不认——
//
//   - containsFoldCaseWord 要求 9 个字符**全是** ASCII；
//   - matchFullWidthMarkerWordAt 要求 9 个字符**全是**全角。
//
// 而 foldMarkerWidth 的**前置门**正是这两者的析取 ⇒ 混拼两者皆不成立 ⇒
// **折宽副本根本不构造** ⇒ 生产扫描器看不见这种形态（实测 0 spans）⇒
// 标记原样到达客户端。这比先前登记的「便宜门漏检」更重：**扫描器本体也看不见**。
//
// ⇒ 修法落在**逐字符**这一层：每个位置各自决定宽度（ASCII 大小写不敏感，
// 或全角字母 Ａ-Ｚ/ａ-ｚ），于是混拼形态被认出来；折宽副本照旧由
// foldMarkerWidth 构造 ⇒ **扫描器本体一个字都不用改**。
func matchMarkerWordAnyWidthAt[T ~string | ~[]byte](text T, i int) bool {
	const word = "sensitive"
	for k := 0; k < len(word); k++ {
		if i >= len(text) {
			return false
		}
		if text[i] < 0x80 {
			// ASCII 拼写：大小写不敏感
			if asciiLower(text[i]) != word[k] {
				return false
			}
			i++
			continue
		}
		// 非 ASCII：只接受全角**字母**（Ａ-Ｚ / ａ-ｚ）。
		// 全角冒号、全角数字等一律不认 —— 它们不是标记词的字符。
		letter, ok := fullWidthLetterAt(text, i)
		if !ok || asciiLower(letter) != word[k] {
			return false
		}
		i += 3
	}
	return true
}

// containsMarkerWordAnyWidth reports whether text contains the marker word
// spelled in any mix of ASCII and full-width characters.
//
// 它取代的是「全 ASCII 词」与「全全角词」这两条门（containsFoldCaseWord /
// containsFullWidthMarkerWord）——那两条在混拼形态上都不成立。
//
// 成本形状与被它取代的两条**相同**：两条 memchr（ASCII 的 's'/'S' 与全角字母的
// 0xEF 首字节）各自定位候选，命中处做定长比较；全程不拷贝 body。
// ⇒ 便宜门没有被「为修漏判而放大常态成本」（hop.go mayCarryMarker 的注释
// 里记着上一次教训：16KB body 上 5.6ns/0-alloc → 2477ns/12KB-alloc，~444×）。
//
// 为什么普通中文不会被拖进昂贵路径：汉字的首字节是 0xE4-0xE9，**不是** 0xEF；
// 全角字母才是 0xEF。而 0xEF 候选处的比较要求前 9 个字符**逐个**是标记词
// 的字母 —— 汉字、全角数字、全角标点都在第一个字符处就被否掉。
func containsMarkerWordAnyWidth(text string) bool {
	// 候选 1：ASCII 的 's' / 'S'（纯 ASCII 拼写与「尾字全角」的混拼都从这里进）
	for i := 0; i < len(text); {
		idx := indexAnyByteString(text[i:], 's', 'S')
		if idx < 0 {
			break
		}
		i += idx
		if matchMarkerWordAnyWidthAt(text, i) {
			return true
		}
		i++
	}
	// 候选 2：全角字母的首字节 0xEF（'Ｓ' / 'ｓ' 都以它开头 ⇒「首字全角」的混拼
	// 从这里进）。全角码位一律 0xEF，与全角花括号共用同一条 memchr。
	for i := 0; i < len(text); {
		idx := strings.IndexByte(text[i:], fullWidthBraceLeadByte)
		if idx < 0 {
			break
		}
		i += idx
		if matchMarkerWordAnyWidthAt(text, i) {
			return true
		}
		i++
	}
	return false
}

// innerBareMarkerCandidates returns the brace-less marker spans hidden **inside**
// a brace-delimited run that is not itself a marker.
//
// 2026-10-27（自审轮）：brace 分支会把一整段 JSON 吞成一个候选；那个候选若不是
// 标记就会被探针判掉，于是**它内部的裸形标记没人看见**。典型的就是工具调用
// 参数：`function.arguments` 是一段 JSON 字符串，`{"pwd":"SENSITIVE:phone"}`
// 整个被吞 ⇒ 裸形标记不可见。
//
// 这里**复用重写路径的同一个原语**（looseBareMarkerSpans）⇒ 两边不会漂。
//
// 成本：先用 containsMarkerWordAnyWidth（两条 memchr）筛掉绝大多数候选 ⇒
// 只有**真的含标记词**的候选才付一次正则扫描。
func innerBareMarkerCandidates(candidate string) []string {
	if !containsMarkerWordAnyWidth(candidate) {
		return nil
	}
	spans := looseBareMarkerSpans(candidate)
	if len(spans) == 0 {
		return nil
	}
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		out = append(out, candidate[sp[0]:sp[1]])
	}
	return out
}

// foldMarkerWidth returns a copy of text in which every full-width form the
// marker grammar can match is rewritten to its ASCII spelling, and reports
// whether anything was rewritten.
//
// Two properties matter to the caller:
//
//   - The rewrite is one rune in, one rune out, so the folded copy has the
//     same **rune** count as the original. It does NOT have the same byte
//     count (a full-width letter is three bytes, its ASCII spelling is one),
//     so byte offsets found on the folded copy must be mapped back — see
//     mapFoldedSpans.
//   - Nothing is rewritten unless the text actually mentions the marker word.
//     The grammar cannot match anything else, so folding every response body
//     would put an O(n) copy on the hot path for no possible gain.
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
			// An invalid byte: copy it verbatim so the folded copy keeps the
			// same byte structure (and the same rune count) as the original.
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

// mapFoldedSpans translates byte spans found in a width-folded copy of text back
// onto text's own byte offsets.
//
// The fold rewrites one rune into one rune, so a folded byte offset always
// lands on a rune boundary of the original, and the original offset is the sum
// of the original byte lengths of the runes before it. The walk is monotone, so
// the spans come back in the same order they went in.
//
// Getting this wrong would splice unrelated text, which is worse than the leak
// it fixes, so the walk is driven by the same rune decoding the fold used.
func mapFoldedSpans(text string, foldedSpans [][]int) [][]int {
	if len(foldedSpans) == 0 {
		return nil
	}
	// The folded boundaries we need, in ascending order: the spans are
	// disjoint and ascending, so start/end alternate.
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
		// A boundary past the end of the text: the folded copy cannot be
		// longer than the original, so this only happens on a malformed
		// input. Clamp to the end rather than reading out of range.
		mapped[ti] = origOff
	}
	out := make([][]int, 0, len(foldedSpans))
	for i := range foldedSpans {
		out = append(out, []int{mapped[2*i], mapped[2*i+1]})
	}
	return out
}

// mergeMarkerSpans merges candidate spans into one ascending, non-overlapping
// list.
//
// When two candidates overlap the wider one wins: a width-folded match covers
// the full-width tail that the ASCII-only match could not see
// ("SENSITIVE:phone：１" matches "SENSITIVE:phone" on the original and the
// whole run on the folded copy), and keeping the narrow one would leave that
// tail in the client-visible text.
//
// A candidate that overlaps a span already kept is dropped, so the result never
// contains overlapping spans — the rewrite loop slices the original text by
// these offsets and overlapping spans would corrupt it.
func mergeMarkerSpans(candidates [][]int) [][]int {
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

// looseBareMarkerSpans returns the brace-less marker spans in text, in
// ascending order, as byte offsets into text.
//
// r1039: the bare form is also matched in its full-width spelling. The span
// pattern only knows ASCII, so the scan also runs on a width-folded copy of
// the text and the offsets are mapped back onto the original.
func looseBareMarkerSpans(text string) [][]int {
	spans := looseBareMarkerSpanPattern.FindAllStringIndex(text, -1)
	folded, ok := foldMarkerWidth(text)
	if !ok {
		return spans
	}
	foldedSpans := looseFoldedBareMarkerSpanPattern.FindAllStringIndex(folded, -1)
	if len(foldedSpans) == 0 {
		return spans
	}
	mapped := mapFoldedSpans(text, foldedSpans)
	mapped = keepBareSpanBoundaries(text, mapped)
	all := make([][]int, 0, len(spans)+len(mapped))
	all = append(all, spans...)
	all = append(all, mapped...)
	return mergeMarkerSpans(all)
}

// looseMarkerSpans returns every candidate marker span in text — the
// brace-delimited form and the brace-less form — in ascending order, as byte
// offsets into text.
//
// The brace-delimited form needs no width folding: its character class accepts
// the full-width braces and its content class accepts any character, and the
// parse side folds the width anyway. The brace-less form is the one that needs
// it, and it is scanned separately so the two can be merged without letting a
// narrow ASCII match hide a wider full-width one.
func looseMarkerSpans(text string) [][]int {
	spans := loosePlaceholderSpanPattern.FindAllStringIndex(text, -1)
	folded, ok := foldMarkerWidth(text)
	if !ok {
		return spans
	}
	foldedSpans := looseFoldedBareMarkerSpanPattern.FindAllStringIndex(folded, -1)
	if len(foldedSpans) == 0 {
		return spans
	}
	mapped := mapFoldedSpans(text, foldedSpans)
	mapped = keepBareSpanBoundaries(text, mapped)
	all := make([][]int, 0, len(spans)+len(mapped))
	all = append(all, spans...)
	all = append(all, mapped...)
	return mergeMarkerSpans(all)
}

// keepBareSpanBoundaries drops the spans whose start is preceded by an ASCII
// word character. The width-folded scan runs without the leading "\b" (see
// looseFoldedBareMarkerSpanSource), so the boundary is judged here, on the
// original text.
func keepBareSpanBoundaries(text string, spans [][]int) [][]int {
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

// redactedMarker 是遮蔽标记的**形状基座**（不带类型）。带类型的形态一律由
// RedactionMarker 生成——不要把它直接写进客户端可见文本。
const redactedMarker = "[REDACTED]"

// redactedMarkerPrefix 是判定「文本里有没有遮蔽标记」用的前缀。
//
// r1038（2026-10-20）：标记一律带类型之后，判定不能再拿整串 "[REDACTED]" 去
// Contains——带类型的形态（"[REDACTED:phone]"）并不包含它，那样的判定会
// 把「真的发生了遮蔽」误判成「没发生」。
const redactedMarkerPrefix = "[REDACTED"

// unknownRedactionType 是「类型不可知」时的类型名。
//
// 用户口径（2026-10-20）：「原则上每个占位符都有特殊的标记」——客户端看到的
// 遮蔽物必须**自证遮掉了哪一类**。类型确实不可知时（退化跨段、超长尾段、
// 整字段遮蔽），说「unknown」比说「不知道」更可用：它至少告诉客户端
// 「这里有一处被网关遮掉了，而网关没解析出类型」，而不是一个无从追问的裸标记。
//
// 这不是给类型编名字：unknown 是一句关于**这次遮蔽**的实话（类型未知），
// 不是关于被遮内容的猜测。类型名来自规则名，本就不构成泄漏。
const unknownRedactionType SensitiveType = "unknown"

// unknownRedactedMarker 是类型不可知时的遮蔽标记。
const unknownRedactedMarker = "[REDACTED:unknown]"

// RedactionMarker returns the client-visible mask for a known sensitive type:
// "[REDACTED:phone]", "[REDACTED:secret]", and so on.
//
// A bare [REDACTED] tells the reader that *something* was removed and nothing
// else, which is why "why is my proxy address masked?" and "was that a phone
// number or a key?" could not be answered from anything the gateway emitted.
// The type is not sensitive — it is the name of a rule, not the value it
// matched — and naming it makes the loss self-describing.
//
// r1038（2026-10-20）：类型不可知时**不再退回裸标记**，而是退到
// "[REDACTED:unknown]"。裸标记让客户端无法区分「遮了一处」与「网关出了
// 故障」，也让「每个占位符都有特殊标记」这条口径在最常见的降级路径上失效。
func RedactionMarker(typ SensitiveType) string {
	if typ == "" {
		return unknownRedactedMarker
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
		looseMarkerSpans(text))
}

// ---------------------------------------------------------------------------
// r1039 — 退化标记的残留
// ---------------------------------------------------------------------------
//
// 症状：标记退化成解析不了的东西时，客户端看到的是**补丁式**的结果：
//
//	{SENSITIVE:secret:1a}  →  {[REDACTED:secret]1a}   ← 多余的 '{'，尾巴 '1a' 留着
//	{SENSITIVE:sec        →  {[REDACTED:sec]          ← '{' 留着，类型名 'sec' 是垃圾
//	{{SENSITIVE:secret:1}} →  {s3cr3t}                ← 多余的花括号
//
// 根因有两条：
//
//  1. 退化跨段（brace 分支整段命中但内部不是标记）走的是**内部裸形态重扫**，
//     重扫只认得段内那截裸标记，段外的 '{' 与尾巴都留了下来；
//  2. span 的边界是**模式匹配到的范围**，不是标记自己的边界——模型多写的一层
//     花括号在 span 之外，于是留在了输出里。
//
// 修法：退化标记**整段**替换成一个干净的遮蔽标记，并且把属于标记自己的
// 定界花括号吸进 span（见 absorbMarkerDelimiters）。

// markerCore returns the span with its surrounding braces removed: the marker
// text itself.
//
// A model may double the braces ("{{SENSITIVE:secret:1}}") or drop them
// entirely; the core is what carries the type and the index, and it is what
// the grammar has to be given. parseLoosePlaceholder used to strip one brace
// pair on its own, which is not enough for the doubled form.
func markerCore(text string, s, e int) (int, int) {
	for s < e {
		r, size := utf8.DecodeRuneInString(text[s:])
		if !isOpenBraceRune(r) {
			break
		}
		s += size
	}
	for e > s {
		r, size := utf8.DecodeLastRuneInString(text[:e])
		if !isCloseBraceRune(r) {
			break
		}
		e -= size
	}
	return s, e
}

// isOpenBraceRune / isCloseBraceRune accept the half- and full-width braces.
func isOpenBraceRune(r rune) bool  { return r == '{' || r == 0xFF5B }
func isCloseBraceRune(r rune) bool { return r == '}' || r == 0xFF5D }

// absorbMarkerDelimiters widens a marker span over the delimiter braces that
// sit immediately outside it.
//
// Two shapes leave a stray brace in the client-visible text:
//
//   - the model doubled the braces ("{{SENSITIVE:secret:1}}"): one pair sits
//     outside the run the pattern matched;
//   - the marker was truncated ("{SENSITIVE:secret:1"): the opening brace
//     never got its closing one.
//
// A brace is only absorbed when it is the marker's own delimiter: the core
// (the span without its braces) must not change, or the span would stop being
// the marker it was. That is what keeps ordinary JSON nesting intact —
// {"a":{"SENSITIVE:secret:1"}} has its extra brace on one side only, and the
// core of the widened span would change.
//
// floor is the end of the text already consumed by the rewrite; a brace that
// was already spliced out is never absorbed again.
func absorbMarkerDelimiters(text string, s, e, floor int) (int, int) {
	coreS, coreE := markerCore(text, s, e)
	if coreS >= coreE {
		return s, e
	}
	// A brace-delimited span: absorb one extra pair when it surrounds the
	// span on both sides (the model doubled the braces).
	if r, ok := runeAt(text, s); ok && isOpenBraceRune(r) {
		if e < len(text) {
			lr, lstart, lok := runeBeforeStart(text, s)
			rr, rend, rok := runeAtSpan(text, e)
			if lok && rok && isOpenBraceRune(lr) && isCloseBraceRune(rr) && lstart >= floor {
				ns, ne := lstart, rend
				cs, ce := markerCore(text, ns, ne)
				if cs == coreS && ce == coreE {
					return ns, ne
				}
			}
		}
		return s, e
	}
	// A brace-less span: the opening brace of a truncated marker may sit
	// right in front of it.
	if lr, lstart, lok := runeBeforeStart(text, s); lok && isOpenBraceRune(lr) && lstart >= floor {
		s = lstart
	}
	return s, e
}

// runeAt returns the rune that starts at byte offset off.
func runeAt(text string, off int) (rune, bool) {
	r, _, ok := runeAtSpan(text, off)
	return r, ok
}

// runeAtSpan returns the rune that starts at byte offset off and the offset
// just past it.
func runeAtSpan(text string, off int) (rune, int, bool) {
	if off < 0 || off >= len(text) {
		return 0, off, false
	}
	r, size := utf8.DecodeRuneInString(text[off:])
	return r, off + size, true
}

// runeBeforeStart returns the rune that ends at byte offset off and the offset
// it starts at.
//
// The start offset matters: a full-width brace is three bytes, so "the byte
// before the span" is not the start of the preceding rune.
func runeBeforeStart(text string, off int) (rune, int, bool) {
	if off <= 0 || off > len(text) {
		return 0, off, false
	}
	r, size := utf8.DecodeLastRuneInString(text[:off])
	return r, off - size, true
}

// knownSensitiveTypes is the type vocabulary this gateway mints placeholders
// for. A marker's type is only trusted when it names one of these: a type
// nobody here has a rule for is a model invention, and naming it back would
// claim a category the gateway never detected.
//
// r1039: "{SENSITIVE:sec" is a truncated marker — "sec" could have been
// "secret" or "server_ip" — and the old code emitted "[REDACTED:sec]", which
// told the client a category that does not exist.
var knownSensitiveTypes = map[SensitiveType]struct{}{
	TypePhone:      {},
	TypeIDCard:     {},
	TypeEmail:      {},
	TypeCreditCard: {},
	TypeSecret:     {},
	TypeInternalIP: {},
	TypeServerIP:   {},
	TypeAccount:    {},
	TypeName:       {},
	TypeCustom:     {},
}

// maskType returns the type a client-visible mask may name.
//
// A type outside the vocabulary is not a category anything detected, so the
// mask says "unknown" — a statement about *this* mask (no type was
// resolvable), not a guess about the bytes.
func maskType(typ SensitiveType) SensitiveType {
	if _, ok := knownSensitiveTypes[typ]; ok {
		return typ
	}
	return unknownRedactionType
}

// degenerateMarkerType reads the type out of a marker-shaped span that does
// not parse as a marker. It returns "" when no type can be read.
//
// The type run is whatever follows the marker word and the separator run; it
// is reported as-is and validated by maskType later.
func degenerateMarkerType(core string) SensitiveType {
	normalized := normalizePlaceholder(core)
	if !strings.HasPrefix(normalized, placeholderMarkerWord) {
		return ""
	}
	rest := strings.TrimLeft(normalized[len(placeholderMarkerWord):], ":")
	end := 0
	for end < len(rest) {
		c := rest[end]
		if !(c >= 'a' && c <= 'z' || c == '_' || c == '-' || (c >= '0' && c <= '9')) {
			break
		}
		end++
	}
	if end == 0 {
		return ""
	}
	return SensitiveType(strings.ReplaceAll(rest[:end], "-", "_"))
}

// isTightMarkerRun reports whether a span carries nothing but the marker —
// no whitespace inside it.
//
// It is what separates the two degenerate shapes:
//
//   - "{SENSITIVE:secret:1a}" is tight: the whole run is the marker plus the
//     garbage the model appended, so replacing it whole leaves no residue.
//   - "{SENSITIVE:phone:13800138000x tail\"}" is not tight: it is a container
//     that merely *starts* with a marker (a JSON fragment in the r1022 case),
//     and replacing it whole would swallow the client's own bytes — the
//     accumulated tool arguments would stop being valid JSON.
//
// A span with whitespace inside therefore keeps the old treatment: only the
// marker itself is masked, the surrounding bytes survive.
func isTightMarkerRun(core string) bool {
	for _, r := range core {
		if unicode.IsSpace(r) {
			return false
		}
	}
	return true
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
		// The span is the run the pattern matched; the marker's own delimiters
		// may sit just outside it (r1039).
		start, end := absorbMarkerDelimiters(text, span[0], span[1], last)
		candidate := text[start:end]
		coreStart, coreEnd := markerCore(text, start, end)
		core := text[coreStart:coreEnd]
		typ, index, ok := parseLoosePlaceholder(core)
		if !ok {
			// The span was matched but is not itself a marker. There are two
			// very different cases here.
			//
			// (a) A degenerate echo such as {SENSITIVE:secret:1a}: the span
			// names the marker but is not marker syntax. r1039: this must be
			// replaced **whole** — the old code re-scanned the inside and
			// patched around the leftovers, which left a stray '{' and the
			// garbage tail in the client-visible text.
			//
			// Only a *tight* run qualifies: a span with whitespace inside is
			// a container that merely starts with a marker, and swallowing it
			// whole would eat the client's own bytes (r1022's contract).
			if looksLikeMarkerSpan(core) && isTightMarkerRun(core) {
				if policy != looseMaskUnknown {
					stats.unresolved++
					continue
				}
				stats.unresolved++
				replacement := RedactionMarker(maskType(degenerateMarkerType(core)))
				b.WriteString(text[last:start])
				b.WriteString(replacement)
				last = end
				changed = true
				continue
			}
			// (b) A brace-delimited run that swallowed more than one marker,
			// the common case being a JSON document whose *value* is a bare
			// form: {"phone":"SENSITIVE:phone"} is captured whole by the brace
			// branch, so skipping it outright would leave the inner marker
			// unscanned and let it reach the client. Re-scan inside the span
			// and splice the result back so the surrounding JSON survives.
			if depth >= maxLooseRescanDepth {
				continue
			}
			// Re-scan with the brace-less pattern only: the full pattern would
			// re-match the run being consumed and never terminate the descent.
			inner := rewriteLooseSpans(ctx, candidate, resolve, foreign, policy, depth+1,
				looseBareMarkerSpans(candidate))
			if inner.text != candidate {
				b.WriteString(text[last:start])
				b.WriteString(inner.text)
				last = end
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
			// 类型是**已知的**（typ 来自 parseLoosePlaceholder）：退化成一个裸
			// 标记会把唯一还能告诉客户端「遮的是哪一类」的信息丢掉。r1038 起
			// 连「类型不可知」也写成 "[REDACTED:unknown]"，此处更没有理由退回裸形态。
			//
			// r1039：只有**网关自己有规则**的类型才配被点名。文法外的类型名
			// 是模型编的（"{SENSITIVE:sec" 里的 "sec" 可能是 "secret" 被截断），
			// 点名它等于告诉客户端一个不存在的类别。
			replacement, changed = RedactionMarker(maskType(typ)), true
		default:
			stats.unresolved++
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(replacement)
		last = end
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
	if offsets == nil || !mayContainMarkerText(text) {
		return
	}
	for _, span := range looseMarkerSpans(text) {
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
