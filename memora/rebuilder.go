package memora

import (
	"encoding/json"
	"fmt"
	"strings"
)

// compactionSnippetPrefix is prepended to the dynamic_context user message
// so downstream LLMs recognize it as gateway-injected context (not user input).
const compactionSnippetPrefix = "[Gateway injected Memora context — earlier task-relevant facts, retrieved by L1 search. " +
	"These are stable, verified facts the upstream may not have in its own context window. " +
	"Treat as ground truth and do not paraphrase unless asked.]\n"

// RebuildBodyWithMemoraSnippets injects the given Memora facts into the
// OpenAI-format messages array of the body as a single user-role
// "dynamic_context" message placed AFTER any system messages and BEFORE
// the existing user/assistant turns.
//
// Layout produced:
//
//	[system msgs...]   (preserved)
//	[user: dynamic_context: <snippets>]   (NEW)
//	[original messages tail: last keepRecentPairs * 2 turns]   (preserved)
//
// Returns (newBody, true) on success, (origBody, false) if there is
// nothing meaningful to inject (no snippets / no messages / parse error).
//
// The original body bytes are never mutated; the returned slice is
// always a fresh allocation suitable for writing back to the upstream
// request.
func RebuildBodyWithMemoraSnippets(origBody []byte, snippets []Memory, keepRecentPairs int) ([]byte, bool) {
	if len(snippets) == 0 {
		return origBody, false
	}
	if keepRecentPairs <= 0 {
		keepRecentPairs = 2
	}

	// Probe the wire format. We support two layouts:
	//   1. { "messages": [...], "model": "...", ... }     — OpenAI chat
	//   2. { "system": "...|block", "messages": [...], "model": "..." } — Anthropic
	// Snippets are injected as a user-role "dynamic_context" message in
	// both shapes, since that's the safest insertion point (any model
	// can read a user msg; system msgs would change top-level semantics).
	var probe struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(origBody, &probe); err != nil {
		return origBody, false
	}
	if len(probe.Messages) == 0 {
		return origBody, false
	}

	// Split tail (last keepRecentPairs*2) from head.
	allMsgs := decodeMessages(probe.Messages)
	if len(allMsgs) == 0 {
		return origBody, false
	}
	headMsgs, tailMsgs := splitHeadAndTail(allMsgs, keepRecentPairs*2)
	if len(headMsgs) == 0 && len(tailMsgs) == 0 {
		return origBody, false
	}

	// Build the dynamic_context user message.
	plainText := buildPlainText(snippets)
	if strings.TrimSpace(plainText) == "" {
		return origBody, false
	}
	dynCtx, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": compactionSnippetPrefix + plainText,
	})
	if err != nil {
		return origBody, false
	}

	// Reassemble: head (any system messages) + dynamic_context + tail
	merged := make([]json.RawMessage, 0, 2+len(tailMsgs))
	merged = append(merged, headMsgs...)
	merged = append(merged, dynCtx)
	merged = append(merged, tailMsgs...)
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return origBody, false
	}

	// Splice newMsgs into the original body, preserving every other top-level
	// key (model / stream / tools / temperature / ...). We do a raw replace
	// of the "messages":[...] slice to keep the byte layout stable for any
	// transport that does byte-equality checks.
	spliced, ok := spliceMessagesRaw(origBody, newMsgs)
	if !ok {
		return origBody, false
	}
	return spliced, true
}

func buildPlainText(snippets []Memory) string {
	var b strings.Builder
	for i, s := range snippets {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "fact %d: %s", i+1, s.Text)
	}
	return b.String()
}

// decodeMessages decodes a raw "messages" array into raw messages.
// Tolerant: invalid JSON returns an empty slice.
func decodeMessages(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// splitHeadAndTail returns (head, tail) where tail is the last `tailMax`
// raw messages and head is everything before. If the array is shorter
// than tailMax, head is empty and tail is the full array.
func splitHeadAndTail(msgs []json.RawMessage, tailMax int) (head, tail []json.RawMessage) {
	if len(msgs) <= tailMax {
		return nil, msgs
	}
	return msgs[:len(msgs)-tailMax], msgs[len(msgs)-tailMax:]
}

// spliceMessagesRaw replaces the "messages":[...] value in origBody with
// newMsgs, preserving every other top-level field and key ordering.
//
// We use a state-machine scan rather than Unmarshal+Marshal so we don't
// change formatting of unrelated fields (e.g. pretty-printed config).
func spliceMessagesRaw(origBody, newMsgs []byte) ([]byte, bool) {
	// Find the "messages" key.
	key := []byte(`"messages"`)
	idx := indexOfKey(origBody, key)
	if idx < 0 {
		return nil, false
	}
	// Walk forward to find the colon after "messages".
	i := idx + len(key)
	// Skip whitespace.
	for i < len(origBody) && (origBody[i] == ' ' || origBody[i] == '\t' || origBody[i] == '\n' || origBody[i] == '\r') {
		i++
	}
	if i >= len(origBody) || origBody[i] != ':' {
		return nil, false
	}
	i++
	// Skip whitespace.
	for i < len(origBody) && (origBody[i] == ' ' || origBody[i] == '\t' || origBody[i] == '\n' || origBody[i] == '\r') {
		i++
	}
	if i >= len(origBody) || origBody[i] != '[' {
		return nil, false
	}
	// Find the matching ']' (top-level — no nested arrays at top level of a value).
	end := i + 1
	depth := 1
	inStr := false
	escape := false
	for end < len(origBody) {
		c := origBody[end]
		if inStr {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inStr = false
			}
		} else {
			switch c {
			case '"':
				inStr = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					goto found
				}
			}
		}
		end++
	}
	return nil, false
found:
	// origBody[idx..i] is "messages" + ws + ":" + ws + "["
	// origBody[i+1..end-1] is the original inner content (possibly empty)
	// Compose: prefix + newMsgs (already a valid JSON array) + suffix
	var out []byte
	out = append(out, origBody[:i]...)
	out = append(out, newMsgs...)
	out = append(out, origBody[end:]...)
	return out, true
}

// indexOfKey finds the first occurrence of `key` as a top-level JSON
// key (preceded by '{' or ',' and followed by optional whitespace and ':').
func indexOfKey(body, key []byte) int {
	for i := 0; i+len(key) <= len(body); {
		if body[i] == '"' && i+len(key) < len(body) && string(body[i:i+len(key)]) == string(key) {
			// Check that this is preceded by a top-level separator.
			if i > 0 {
				prev := body[i-1]
				if prev != '{' && prev != ',' {
					i++
					continue
				}
			}
			// Check that what follows is the colon (possibly with ws).
			j := i + len(key)
			for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
				j++
			}
			if j < len(body) && body[j] == ':' {
				return i
			}
		}
		i++
	}
	return -1
}
