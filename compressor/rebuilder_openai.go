// Package compressor - rebuilder_openai.go (Round 47 / v7 T6)
//
// C-track rebuild for OpenAI chat: takes an LLM-generated summary and
// splices it back into the original body as a fresh user-role "context"
// message, while preserving:
//   - A-track system messages (verbatim, at front)
//   - B-track first user message (verbatim, after summary context)
//   - last keepRecentPairs × 2 turns (most recent user/assistant dialog)
//
// The summary is wrapped with Anthropic's SESSION_MEMORY_PROMPT template
// (v7 §4.2) so the downstream LLM sees:
//
//	[system msgs...]                  ← A-track
//	[user: dynamic_context: <summary>] ← C-track (NEW)
//	[user: <first user message>]      ← B-track
//	[assistant: <last reply>]         ← keepRecentPairs
//	[user: <most recent user turn>]   ← keepRecentPairs
//
// v7 §6 single-level chain rule: this rebuild never emits a new request_id
// (that's executor_chat.go's job via parent_request_id); it just rewrites
// the body bytes. Caller decides whether to spawn a new request or mutate
// in place.
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §4 (dual-track)
// + §5 (post-summary rebuild flow).

package compressor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CompressionSummaryPrefix is prepended to the dynamic_context user message
// so downstream LLMs recognize it as gateway-injected context (not user input).
// Mirrors the prefix used by memora/rebuilder.go (compactionSnippetPrefix)
// so the LLM treats both inject paths uniformly.
const CompressionSummaryPrefix = "[Gateway compacted conversation summary - prior turns collapsed to fit context. " +
	"Use direct quotes for exact identifiers, errors, user corrections. " +
	"The first user message is preserved verbatim below as the original intent.]\n"

// compressionSystemPrompt is the Anthropic SESSION_MEMORY_PROMPT template
// imported into the LLM-summary call. It's exported as a const so the
// upstream-side compaction caller (rebuildOpenAIBodyAfterSummary in
// routing/context_summarize.go) can reuse the same prompt without drift.
//
// See: https://platform.claude.com/cookbook/misc-session-memory-compaction
const compressionSystemPrompt = `You compress long agent conversation history for downstream LLM calls.

<summary-format>
## User Intent
The user's original request and any refinements. Use direct quotes for key requirements.
If the user's goal evolved during the conversation, capture that progression.

## Completed Work
## Errors & Corrections
## Active Work
## Pending Tasks
## Key References
</summary-format>

<preserve-rules>
Always preserve when present:
- Exact identifiers (IDs, paths, URLs, keys, names)
- Error messages verbatim
- User corrections and negative feedback
- Specific values, formulas, or configurations
- The precise state of any in-progress work
- The first user message verbatim MUST be quoted under "User Intent"
</preserve-rules>

Write a dense factual summary only - no preamble, no markdown fences.`

// RebuildOpenAIAfterSummary splices the LLM-generated summary into an
// OpenAI chat body, preserving A/B-tracks and the most recent tail.
//
// Layout produced (in order):
//
//	[system msgs...]                       (A-track verbatim)
//	[user: dynamic_context: <summary>]     (C-track NEW)
//	[first user message verbatim]          (B-track verbatim)
//	[last keepRecentPairs * 2 messages]    (recent tail verbatim)
//
// Returns (newBody, true) on success, (origBody, false) when:
//   - body is unparseable as OpenAI chat
//   - body has no messages
//   - ret (A/B-track extraction) is nil
//
// The original body bytes are never mutated; the returned slice is always
// a fresh allocation suitable for writing back to the upstream request.
//
// keepRecentPairs defaults to 2 (matches LLM_GATEWAY_COMPRESSION_KEEP_RECENT_PAIRS).
// Pass 0 or negative to use the default.
func RebuildOpenAIAfterSummary(body []byte, summary string, ret *Retained, keepRecentPairs int) ([]byte, bool) {
	if ret == nil || len(summary) == 0 {
		return body, false
	}
	if keepRecentPairs <= 0 {
		keepRecentPairs = 2
	}

	// Probe the wire format. We support the OpenAI chat shape:
	// {"messages":[...], "model":"...", "stream":..., "tools":[...]}
	// Other top-level keys are preserved verbatim.
	var probe struct {
		Model    string          `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, false
	}
	if len(probe.Messages) == 0 {
		return body, false
	}

	// Split the original messages into (preserved head, recent tail).
	// - preserved head = everything before FirstUserIndex (system msgs + first user)
	//   Wait: A-track system msgs live in probe.Messages too (OpenAI shape).
	//   We pass them via ret.SystemMessages separately; rebuild prepends them
	//   to the rebuilt messages array (deduplicated against probe.Messages
	//   entries with role=system).
	// - recent tail = last keepRecentPairs * 2 messages after FirstUserIndex.
	//   This is the dialog that the model must respond to; compressing it
	//   would lose context.
	allMsgs := probe.Messages
	headMsgs, tailMsgs := splitSystemAndTail(allMsgs, ret, keepRecentPairs*2)
	if len(headMsgs) == 0 && len(tailMsgs) == 0 {
		return body, false
	}

	// Build the dynamic_context user message (C-track).
	dynCtxRaw, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": CompressionSummaryPrefix + strings.TrimSpace(summary),
	})
	if err != nil {
		return body, false
	}

	// Reassemble: A-track system + C-track summary + head (first user) + tail.
	merged := make([]json.RawMessage, 0, len(ret.SystemMessages)+2+len(headMsgs)+len(tailMsgs))
	merged = append(merged, ret.SystemMessages...)
	merged = append(merged, dynCtxRaw)
	merged = append(merged, headMsgs...)
	merged = append(merged, tailMsgs...)
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return body, false
	}

	// Splice newMsgs into the original body, preserving every other top-level
	// key (model / stream / tools / temperature / ...). We do a raw replace
	// of the "messages":[...] slice to keep the byte layout stable for any
	// transport that does byte-equality checks.
	spliced, ok := spliceMessagesRaw(body, newMsgs)
	if !ok {
		return body, false
	}
	return spliced, true
}

// splitSystemAndTail separates the original messages array into:
//   - headMsgs: A-track system messages + B-track first user message
//     (everything before and including FirstUserIndex, minus system msgs
//     since ret.SystemMessages already carries them)
//   - tailMsgs: the last maxMsgs messages after FirstUserIndex (the
//     recent dialog that the model must keep seeing)
//
// We strip A-track system messages from the head because ret.SystemMessages
// is the canonical home for them - it was extracted by extractOpenAI which
// scans role=system entries. The rebuilder prepends ret.SystemMessages to
// the rebuilt array first, then C-track summary, then B-track head, then
// tail. This deduplication avoids emitting the same system prompt twice.
func splitSystemAndTail(messages []json.RawMessage, ret *Retained, maxTail int) (head, tail []json.RawMessage) {
	if ret == nil || ret.FirstUserIndex < 0 {
		// Defensive: no B-track pinned. Return everything as tail so the
		// rebuilder preserves the full conversation.
		return nil, lastN(messages, maxTail)
	}
	for i, m := range messages {
		role := messageRole(m)
		if i == ret.FirstUserIndex && role == "user" {
			head = append(head, m) // B-track
			break
		}
	}
	tailStart := ret.FirstUserIndex + 1
	if tailStart < len(messages) {
		tail = lastN(messages[tailStart:], maxTail)
	}
	return head, tail
}

// lastN returns the last n elements of s. If len(s) <= n, returns s
// unchanged. Negative n returns an empty slice.
func lastN(s []json.RawMessage, n int) []json.RawMessage {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// spliceMessagesRaw replaces the "messages":[...] slice in body with newMsgs
// while preserving every other top-level key. Returns (spliced, true) on
// success, (orig, false) on parse failure.
//
// Implementation: parse body into a generic map, overwrite the messages
// key with the pre-marshalled newMsgs bytes, re-marshal. This keeps the
// top-level key order reasonably stable (Go's encoding/json sorts map keys
// alphabetically, which is the standard behaviour for this codebase).
func spliceMessagesRaw(body []byte, newMsgs []byte) ([]byte, bool) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, false
	}
	generic["messages"] = newMsgs
	out, err := json.Marshal(generic)
	if err != nil {
		return nil, false
	}
	return out, true
}

// SummaryPrompt returns the Anthropic SESSION_MEMORY_PROMPT template that
// the upstream-side compaction caller passes to its LLM call. Exported so
// callers outside this package can use the exact same prompt without
// copy-paste drift.
//
// See compressionSystemPrompt above for the canonical template.
func SummaryPrompt() string {
	return compressionSystemPrompt
}

// ErrRebuildFailed indicates a rebuild attempt could not produce a usable
// body (parse failure, no messages, etc.). Callers use this to fall back
// to the strategy=noop path (write original body to request_logs).
var ErrRebuildFailed = fmt.Errorf("compressor: rebuild failed")
