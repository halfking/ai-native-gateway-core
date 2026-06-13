// Package textsplit provides a tiny utility for stripping a leading
// `<think>...</think>` segment from a reasoning-model's streamed text.
//
// Lives in internal/ so both relay/ (which performs streaming protocol
// translation) and routing/ (which performs non-stream rewriting) can
// share one implementation without forming an import cycle (relay
// already imports routing).
package textsplit

import "strings"

// MaxThinkBytes is the upper bound on the extracted thinking text. If
// the captured thinking exceeds this size it is truncated to that size
// with an ellipsis appended, ensuring the gateway never returns an
// unbounded payload to the client even if upstream misbehaves.
//
// 1 MiB comfortably accommodates realistic reasoning traces (typical
// traces are 1-50 KiB) while preventing accidental OOM on a 100 MB
// upstream response.
const MaxThinkBytes = 1 << 20 // 1 MiB

// SplitLeadingThink extracts a leading `<think>...</think>` segment
// from s, returning the inner thinking text, the remainder of s with the
// segment stripped (leading newlines trimmed), and ok=true if the prefix
// was found. Otherwise it returns s unchanged and ok=false.
//
// Conservative by design: only the FIRST complete `<think>...</think>`
// at the start of s is promoted. Bodies without the opening tag, with
// unclosed tags, or with the tags appearing later in the body are
// returned verbatim. This protects legitimate XML/HTML content
// elsewhere in the response from being accidentally rewritten.
func SplitLeadingThink(s string) (think, rest string, ok bool) {
	return SplitLeadingThinkBounded(s, MaxThinkBytes)
}

// SplitLeadingThinkBounded is SplitLeadingThink with an explicit cap on
// the size of the thinking text returned.
func SplitLeadingThinkBounded(s string, maxThinkBytes int) (think, rest string, ok bool) {
	const openTag = "<think>"
	const closeTag = "</think>"
	if !strings.HasPrefix(s, openTag) {
		return "", s, false
	}
	end := strings.Index(s, closeTag)
	if end < 0 {
		return "", s, false
	}
	raw := s[len(openTag):end]
	if maxThinkBytes > 0 && len(raw) > maxThinkBytes {
		// Truncate at a rune boundary to avoid splitting a UTF-8 sequence.
		cut := maxThinkBytes
		for cut > 0 && !utf8Start(raw[cut]) {
			cut--
		}
		raw = raw[:cut] + "…"
	}
	think = raw
	rest = strings.TrimLeft(s[end+len(closeTag):], "\n")
	return think, rest, true
}

// utf8Start reports whether b is the first byte of a UTF-8 rune
// (i.e. not a continuation byte 10xxxxxx).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }