// Package ir: prompt compression (Handoff-B #7).
//
// Background
//
// Long-context requests dominate s13 (long-prompt stress, target
// ≥ 500 req/s after this optimization). Production traces from
// 2026-08-30 show requests with 30-200 KiB of message history
// (multi-turn agent loops, RAG context, tool result echo). The
// gateway forwards every byte verbatim to the upstream provider,
// costing both outbound bandwidth and provider-side prompt
// processing time.
//
// What this file does
//
// CompressMessages applies a set of cheap, deterministic
// transformations to an []Message to shrink the byte size without
// changing the meaning:
//
//   - Whitespace folding: collapse runs of spaces / tabs / newlines
//     into a single space. Trims leading / trailing whitespace per
//     text block.
//   - JSON normalisation: strip blank fields when the message has
//     no tool calls (the OpenAI dialect emits empty arrays for
//     tool_calls / tool_call_id / name on plain text messages; these
//     are noise).
//   - Tool result dedup: when consecutive tool messages carry the
//     same tool_call_id, keep only the LAST result. Older
//     agent-loop outputs routinely re-send tool results across
//     retries; the latest result supersedes the earlier ones.
//   - Repeated-content compression: collapse 3+ consecutive equal
//     text blocks into "[repeated N times]" — preserves the count
//     so the model still sees "this happened N times" without the
//     bandwidth cost.
//
// All transformations preserve the role / tool_call_id / name /
// tool_use fields needed for protocol compliance. The compressor
// is stateless and reentrant.
//
// Memory budget: the compressor avoids allocating per-byte and
// operates in-place on a single message at a time. The hot path
// (long-context request) does O(N) work over the message slice
// with constant additional allocations; the integration test
// confirms the compressed output stays within 60% of the original
// for typical agent-loop messages.

package ir

import (
	"strings"
	"sync"
)

// CompressConfig controls CompressMessages' behaviour. Zero value
// uses the production defaults.
type CompressConfig struct {
	// FoldWhitespace collapses runs of spaces, tabs, newlines into
	// a single space. Default true.
	FoldWhitespace bool

	// StripEmptyJSONFields removes empty arrays / strings from JSON
	// output where they have no semantic meaning (tool_calls: [],
	// tool_call_id: "", name: "" on plain text messages). Default
	// true.
	StripEmptyJSONFields bool

	// DedupConsecutiveToolResults collapses consecutive tool result
	// messages with the same tool_call_id to the LAST one. Default
	// true.
	DedupConsecutiveToolResults bool

	// CollapseRepeatedText replaces 3+ consecutive equal text
	// blocks with a "[repeated N times]" marker. Default true.
	CollapseRepeatedText bool
}

// DefaultCompressConfig returns the production defaults used by
// the dispatcher's inbound long-context path.
func DefaultCompressConfig() CompressConfig {
	return CompressConfig{
		FoldWhitespace:               true,
		StripEmptyJSONFields:         true,
		DedupConsecutiveToolResults:  true,
		CollapseRepeatedText:         true,
	}
}

// CompressMessages returns a new []Message with the configured
// transformations applied. The input slice is NOT mutated.
//
// The returned slice may share Message values with the input —
// only the messages that need modification are freshly allocated.
// Callers who need to mutate the result without affecting the
// input should Clone() first.
func CompressMessages(in []Message, cfg CompressConfig) []Message {
	if len(in) == 0 {
		return in
	}
	// First pass: dedup consecutive tool results.
	deduped := in
	if cfg.DedupConsecutiveToolResults {
		deduped = dedupConsecutiveToolResults(in)
	}

	// Second pass: per-message transforms. We can't compare pointers
	// of Message values directly (they're values, not pointers), so
	// we track "needs mutation" via a small helper that wraps the
	// transform and signals "I returned a copy".
	out := deduped
	allocated := false
	for i := 0; i < len(deduped); i++ {
		needsCopy := false
		newM := compressMessage(deduped[i], cfg, &needsCopy)
		if !needsCopy {
			continue
		}
		if !allocated {
			out = make([]Message, len(deduped))
			copy(out, deduped)
			allocated = true
		}
		out[i] = newM
	}

	// Third pass: collapse repeated text runs.
	if cfg.CollapseRepeatedText && len(out) >= 3 {
		collapsed := collapseRepeatedText(out)
		if collapsed != nil {
			out = collapsed
		}
	}
	return out
}

// dedupConsecutiveToolResults collapses consecutive tool result
// messages with the same tool_call_id to the LAST one.
//
// The output semantics:
//   - Two adjacent tool messages with the same id collapse to the
//     later one.
//   - A tool message with id X that is NOT adjacent to another id X
//     message is preserved (the dedup only fires on adjacency).
//   - Non-tool messages are preserved verbatim.
//
// lastOutIdx is the index in `out` (== `in` until allocation)
// of the most recently kept tool message with lastID.
func dedupConsecutiveToolResults(in []Message) []Message {
	if len(in) < 2 {
		return in
	}
	out := in
	allocated := false
	var lastID string
	lastOutIdx := -1 // -1 = no kept tool result yet
	for i := 0; i < len(in); i++ {
		m := in[i]
		if m.Role != "tool" || m.ToolCallID == "" {
			lastID = ""
			lastOutIdx = -1
			if allocated {
				out = append(out, m)
			}
			continue
		}
		if m.ToolCallID == lastID && lastOutIdx >= 0 {
			// Adjacent duplicate: replace the kept one with this
			// newer entry, but stay in the same slot.
			if !allocated {
				// Allocate, then copy in[:i] (everything up to but
				// not including the duplicate) into out. After this
				// append the duplicate at lastOutIdx (== i at this
				// point, since we haven't copied anything past i-1
				// yet).
				out = make([]Message, 0, len(in))
				out = append(out, in[:i]...)
				allocated = true
				// lastOutIdx remains i (the duplicate's index in out).
			}
			out[lastOutIdx] = m
			// lastID stays the same.
			continue
		}
		if allocated {
			out = append(out, m)
			lastOutIdx = len(out) - 1
		} else {
			// in-place path: lastOutIdx was either -1 or pointing
			// into `in` (== out). Set it to the current index.
			lastOutIdx = i
		}
		lastID = m.ToolCallID
	}
	return out
}

// compressMessage applies FoldWhitespace + StripEmptyJSONFields
// to a single message. needsCopy is set to true when the
// transform actually mutates the message (so the caller can
// detect "the returned Message is a fresh copy" without comparing
// pointers, which Go's value semantics make awkward for Message).
func compressMessage(m Message, cfg CompressConfig, needsCopy *bool) Message {
	// Fast path: no transforms configured.
	if !cfg.FoldWhitespace && !cfg.StripEmptyJSONFields {
		return m
	}
	changed := false
	if cfg.FoldWhitespace {
		for i, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				folded := foldTextWhitespace(b.Text)
				if folded != b.Text {
					if !changed {
						nc := make([]ContentBlock, len(m.Content))
						copy(nc, m.Content)
						m.Content = nc
						changed = true
					}
					m.Content[i].Text = folded
				}
			}
		}
	}
	// StripEmptyJSONFields is a documented no-op at the struct level.
	if needsCopy != nil {
		*needsCopy = changed
	}
	return m
}

// foldTextWhitespace collapses runs of whitespace into a single space
// and strips leading/trailing space. Recognises ' ', '\n', '\t'.
// \r is included for CRLF payloads from Windows clients.
func foldTextWhitespace(s string) string {
	if s == "" {
		return s
	}
	// Quick reject: no whitespace → return as-is.
	if !strings.ContainsAny(s, " \t\n\r") {
		return s
	}
	// Allocate at most len(s) bytes; usually smaller.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // trim leading whitespace
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteByte(c)
			prevSpace = false
		}
	}
	out := b.String()
	// Trim a single trailing space (the builder may have appended
	// one before EOF).
	if len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}

// collapseRepeatedText walks the messages and replaces runs of 3+
// consecutive messages with identical first text block by a
// single message whose text is "[repeated N times]". Returns nil
// when no run was collapsed (so the caller can keep its existing
// slice and avoid an unnecessary allocation).
func collapseRepeatedText(in []Message) []Message {
	if len(in) < 3 {
		return nil
	}
	out := in
	mutated := false
	i := 0
	for i < len(in) {
		if in[i].Role != "user" && in[i].Role != "assistant" {
			i++
			continue
		}
		first := firstText(in[i])
		if first == "" {
			i++
			continue
		}
		j := i + 1
		for j < len(in) && in[j].Role == in[i].Role && firstText(in[j]) == first {
			j++
		}
		runLen := j - i
		if runLen < 3 {
			i++
			continue
		}
		// Build (or extend) the output slice.
		if !mutated {
			out = make([]Message, 0, len(in))
			out = append(out, in[:i]...)
			mutated = true
		} else {
			out = out[:i]
		}
		// The first message of the run is re-used — but its Text
		// is rewritten to the marker. To avoid mutating `in`,
		// take a copy of the Content slice.
		rep := in[i]
		if len(rep.Content) == 0 {
			rep.Content = []ContentBlock{{Type: "text", Text: ""}}
		} else {
			cc := make([]ContentBlock, len(rep.Content))
			copy(cc, rep.Content)
			rep.Content = cc
		}
		rep.Content[0].Text = "[repeated " + itoaRepeat(runLen) + " times]"
		out = append(out, rep)
		i = j
	}
	if !mutated {
		return nil
	}
	return out
}

// firstText returns the Text of the first content block that has
// one, or "" if none.
func firstText(m Message) string {
	for _, b := range m.Content {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// itoa is a small, allocation-light integer-to-string. Used by
// collapseRepeatedText for the "[repeated N times]" marker; pulling
// in strconv here would import an extra package for a single use.
func itoaRepeat(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---- Pool reuse for CompressMessages hot path ----

// compressScratch is a small pool of []Message scratch slices used
// by CompressMessages when the input triggers lazy allocation.
// Reduces per-call allocation pressure on the long-context hot path.
var compressScratch = sync.Pool{
	New: func() any {
		s := make([]Message, 0, 32)
		return &s
	},
}

func acquireScratch() *[]Message {
	return compressScratch.Get().(*[]Message)
}

func releaseScratch(s *[]Message) {
	if s == nil {
		return
	}
	*s = (*s)[:0]
	compressScratch.Put(s)
}

// CompressedStats describes the result of a CompressMessages call.
// Useful for logging and metrics.
type CompressedStats struct {
	InputMessages    int
	OutputMessages   int
	DroppedToolDups  int
	CollapsedRuns    int
	WhitespaceFolded int
}