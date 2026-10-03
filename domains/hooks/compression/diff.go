// Package compressor - diff.go (v3 T25)
//
// BuildOutboundMessages computes the outbound messages body for a session
// request by delta-appending new client turns to the last outbound body.
//
// Problem:
//
//	LLM clients (Cursor, RooCode, OpenCode) always send the FULL conversation
//	history on every request. If the gateway compressed the history on a prior
//	request, the client's next message still contains the original (uncompressed)
//	history plus new turns. Naively forwarding the client body would undo the
//	compression. We need to:
//	  1. Detect which messages the client added since the last outbound.
//	  2. Append only those new messages to the (compressed) last outbound body.
//
// Algorithm — ordered lineage anchor:
//
//	For each message compute a sha256 fingerprint of the canonical JSON.
//	Uncompressed sessions require the complete prior sequence to be the
//	client's prefix. Compressed sessions retain a gateway summary marker plus
//	a retained block of the client history; that block must anchor to exactly
//	one client range. Two retained shapes are recognised:
//	  1. contiguous: the whole retained block is one contiguous client range
//	     (pure recent-tail retention);
//	  2. two-segment (B-track rebuild layout): first-user head + recent tail
//	     are two non-adjacent client segments — the head pins lineage, the
//	     unique contiguous tail occurrence anchors the delta. Both lanes
//	     (openai rebuilder / anthropic rebuilder) produce this shape.
//	Any ambiguity fails open to a full resend rather than risking a wrong
//	anchor silently dropping client messages.
//
// Summary marker preservation:
//
//	Any message in lastOutbound whose "content" string starts with
//	CompactionMarkerPrefix ([smm_v1:) or one of the rebuilders' summary
//	prefixes (CompressionSummaryPrefix / smartWindowSummaryPrefix) is a
//	gateway-injected summary. It is kept verbatim in the rebuilt body and its
//	hash is deliberately excluded from the anchor index so the diff algo
//	never mistakes it for a client-sent message. Anthropic summaries live in
//	the top-level system field instead; BuildOutboundMessages detects that
//	shape separately.
//
// Edge cases:
//   - Full new session (no lastOutbound):     return clientBody unchanged.
//   - No shared message found:                return clientBody (session reset).
//   - Client unchanged vs last outbound:      return lastOutbound (deduplicated).
//   - Client added turns after a tool round:  LCS skip past orphaned tool_result.

package compression

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/tokenest"
)

// OutboundResult is the output of BuildOutboundMessages.
type OutboundResult struct {
	Body       []byte    // the body to forward to the upstream LLM
	MsgHashes  []MsgHash // per-message fingerprints for the next diff pass
	MsgCount   int       // number of messages in Body
	TokenEst   int       // heuristic token estimate (chars / 3.5)
	Unchanged  bool      // true when Body == lastOutboundBody (no delta)
	IsNewSess  bool      // true when lastOutboundBody was nil (full new session)
	DeltaCount int       // number of new messages appended
}

// rawMsg is a thin wrapper around json.RawMessage for internal use.
type rawMsg = json.RawMessage

// BuildOutboundMessages is the v3 session compressor diff engine.
// It is safe to call concurrently; it does not mutate its inputs.
//
//   - clientBody is the request body the client sent (required).
//   - lastState is the SessionState from the cache (nil = new session).
//   - lastOutboundBody is the body that was last forwarded to the LLM
//     (nil = new session or cache miss without body).
//   - protocol is "openai" or "anthropic-messages".
func BuildOutboundMessages(
	clientBody []byte,
	lastState *SessionState,
	lastOutboundBody []byte,
	protocol string,
) (*OutboundResult, error) {
	if len(clientBody) == 0 {
		return &OutboundResult{Body: clientBody, IsNewSess: true}, nil
	}

	// ── Extract client messages ──────────────────────────────────────────
	clientMsgs, err := extractMessages(clientBody)
	if err != nil || len(clientMsgs) == 0 {
		return &OutboundResult{Body: clientBody, IsNewSess: true}, nil
	}

	// ── New session: no prior outbound ──────────────────────────────────
	if lastState == nil || len(lastOutboundBody) == 0 {
		hashes := computeHashes(clientMsgs)
		est := estimateBodyTokens(clientBody)
		return &OutboundResult{
			Body:      clientBody,
			MsgHashes: hashes,
			MsgCount:  len(clientMsgs),
			TokenEst:  est,
			IsNewSess: true,
		}, nil
	}

	// ── Extract last outbound messages ───────────────────────────────────
	lastMsgs, err := extractMessages(lastOutboundBody)
	if err != nil || len(lastMsgs) == 0 {
		// Can't parse last outbound — treat as new session.
		return newSessionResult(clientBody, clientMsgs), nil
	}
	// A durable body hash is the session lineage guard. If it is present, the
	// cached body must match exactly; otherwise another session/lifecycle or a
	// stale cache entry could be merged with this request.
	if lastState.LastOutboundHash != "" && sha256Hex(lastOutboundBody) != lastState.LastOutboundHash {
		return newSessionResult(clientBody, clientMsgs), nil
	}
	if lastState.MsgCount > 0 && lastState.MsgCount != len(lastMsgs) {
		return newSessionResult(clientBody, clientMsgs), nil
	}

	// ── Establish one unambiguous ordered lineage anchor ─────────────────
	// Uncompressed sessions require the complete prior sequence to be the
	// client's prefix. Compressed sessions retain gateway-only summary markers
	// (messages[] markers on the OpenAI lane, the top-level system field on
	// the Anthropic lane); for those, match the non-summary retained block
	// against one unique client range, falling back to the two-segment B-track
	// layout when the block is not contiguous in the client history. Ambiguous
	// duplicate occurrences fail open rather than dropping a client message by
	// choosing the wrong anchor.
	anthropicSystemSummary := protocol == "anthropic-messages" && hasAnthropicSystemSummary(lastOutboundBody)
	anchorEnd, ok := findDeltaAnchor(clientMsgs, lastMsgs, anthropicSystemSummary)
	if !ok {
		return newSessionResult(clientBody, clientMsgs), nil
	}

	// ── Delta tail: client messages after the proven anchor ─────────────
	deltaTail := clientMsgs[anchorEnd:]

	if len(deltaTail) == 0 {
		// Client body is a subset or equal to last outbound — return last.
		hashes := computeHashes(lastMsgs)
		return &OutboundResult{
			Body:      lastOutboundBody,
			MsgHashes: hashes,
			MsgCount:  len(lastMsgs),
			TokenEst:  estimateBodyTokens(lastOutboundBody),
			Unchanged: true,
		}, nil
	}

	// ── Merge: last outbound + delta tail ────────────────────────────────
	merged := make([]rawMsg, 0, len(lastMsgs)+len(deltaTail))
	merged = append(merged, lastMsgs...)
	merged = append(merged, deltaTail...)

	newMsgsRaw, err := json.Marshal(merged)
	if err != nil {
		// Marshal failure is non-fatal; fall back to client body.
		hashes := computeHashes(clientMsgs)
		return &OutboundResult{
			Body:      clientBody,
			MsgHashes: hashes,
			MsgCount:  len(clientMsgs),
			TokenEst:  estimateBodyTokens(clientBody),
		}, nil
	}

	// Splice new messages into the client body (preserves model, stream, tools, etc.)
	newBody, ok := spliceBodyMessages(clientBody, newMsgsRaw)
	if ok && protocol == "anthropic-messages" {
		newBody = preserveAnthropicSystem(lastOutboundBody, newBody)
	}
	if !ok {
		// Splice failed — fall back to client body.
		hashes := computeHashes(clientMsgs)
		return &OutboundResult{
			Body:      clientBody,
			MsgHashes: hashes,
			MsgCount:  len(clientMsgs),
			TokenEst:  estimateBodyTokens(clientBody),
		}, nil
	}

	hashes := computeHashes(merged)
	return &OutboundResult{
		Body:       newBody,
		MsgHashes:  hashes,
		MsgCount:   len(merged),
		TokenEst:   estimateBodyTokens(newBody),
		DeltaCount: len(deltaTail),
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

func newSessionResult(body []byte, messages []rawMsg) *OutboundResult {
	return &OutboundResult{
		Body: body, MsgHashes: computeHashes(messages), MsgCount: len(messages),
		TokenEst: estimateBodyTokens(body), IsNewSess: true,
	}
}

func findDeltaAnchor(clientMsgs, lastMsgs []rawMsg, anthropicSystemSummary bool) (int, bool) {
	lastComparable := make([]rawMsg, 0, len(lastMsgs))
	hasGatewaySummary := anthropicSystemSummary
	for _, message := range lastMsgs {
		if isSummaryMarkerMsg(message) {
			hasGatewaySummary = true
			continue
		}
		lastComparable = append(lastComparable, message)
	}
	if len(lastComparable) == 0 {
		return 0, false
	}

	// Precompute fingerprints once: the anchor walk compares every retained
	// window against every client offset, and re-hashing per comparison made
	// that quadratic in JSON bytes instead of in string compares.
	clientHashes := hashAll(clientMsgs)
	lastHashes := hashAll(lastComparable)

	if !hasGatewaySummary {
		if len(clientMsgs) < len(lastComparable) || !hashSeqEqual(clientHashes[:len(lastComparable)], lastHashes) {
			return 0, false
		}
		return len(lastComparable), true
	}

	// A compressed outbound contains only a retained block of the original
	// client history. Preferred shape: the whole block occurs exactly once
	// contiguously in the client history.
	matches := 0
	end := 0
	for start := 0; start+len(lastComparable) <= len(clientMsgs); start++ {
		if hashSeqEqual(clientHashes[start:start+len(lastComparable)], lastHashes) {
			matches++
			end = start + len(lastComparable)
		}
	}
	if matches == 1 {
		return end, true
	}

	// Fallback shape (B-track rebuild layout): the retained block is two
	// non-adjacent client segments — first-user head + recent tail — because
	// the compressed middle was dropped between them.
	return findTwoSegmentAnchor(clientHashes, lastHashes)
}

// findTwoSegmentAnchor matches the B-track rebuild layout. For every split
// point the tail is the retained suffix lastHashes[split+1:] and the head is
// lastHashes[:split+1]. A split is valid when the tail occurs exactly once
// contiguously in the client history and the head occurs contiguously
// somewhere entirely before that tail occurrence (lineage evidence). Splits
// that agree on the same anchor produce the identical outbound body, so the
// ambiguity that must fail open is disagreement on the anchor itself.
func findTwoSegmentAnchor(clientHashes, lastHashes []string) (int, bool) {
	if len(lastHashes) < 2 {
		return 0, false
	}
	anchorEnd := 0
	seen := make(map[int]bool)
	for split := 0; split+1 < len(lastHashes); split++ {
		tail := lastHashes[split+1:]
		tailStart, tailMatches := uniqueContiguousIndex(clientHashes, tail)
		if tailMatches != 1 {
			continue
		}
		if containsSeqBefore(clientHashes, lastHashes[:split+1], tailStart) {
			end := tailStart + len(tail)
			seen[end] = true
			anchorEnd = end
		}
	}
	if len(seen) != 1 {
		return 0, false
	}
	return anchorEnd, true
}

// uniqueContiguousIndex returns the start of needle's contiguous occurrences
// in haystack plus how many there were (last occurrence start on match).
func uniqueContiguousIndex(haystack, needle []string) (start, matches int) {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if hashSeqEqual(haystack[i:i+len(needle)], needle) {
			matches++
			start = i
		}
	}
	return start, matches
}

// containsSeqBefore reports whether needle occurs contiguously in haystack
// with its whole window ending at or before the given boundary.
func containsSeqBefore(haystack, needle []string, before int) bool {
	limit := before
	if limit > len(haystack) {
		limit = len(haystack)
	}
	for i := 0; i+len(needle) <= limit; i++ {
		if hashSeqEqual(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func hashAll(msgs []rawMsg) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = msgHash(m)
	}
	return out
}

func hashSeqEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == "" || b[i] == "" || a[i] != b[i] {
			return false
		}
	}
	return true
}

// extractMessages parses the "messages" array from an OpenAI or Anthropic body.
// V2OutboundBuilder returns the persisted message array directly, so accept
// that shape as well. Keeping the compatibility here makes the builder/cache
// contract explicit without requiring the compression package to import V2.
func extractMessages(body []byte) ([]rawMsg, error) {
	var probe struct {
		Messages []rawMsg `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.Messages != nil {
		return probe.Messages, nil
	}

	var messages []rawMsg
	if err := json.Unmarshal(body, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// msgHash computes a canonical fingerprint of the complete message object.
// It includes tool calls and the full content, so suffix-only edits and distinct
// assistant tool calls cannot collapse to the same identity. JSON object key
// ordering is normalized by re-marshalling the decoded value.
func msgHash(raw rawMsg) string {
	var message interface{}
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return ""
	}
	canonical, err := json.Marshal(message)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", h[:16])
}

// contentFingerprint extracts the first 512 bytes of meaningful content
// from a message content field (string or array of parts).
func contentFingerprint(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first (most common).
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if len(s) > 512 {
			s = s[:512]
		}
		return s
	}
	// Array of content parts — concatenate meaningful fields from all block types.
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw[:min512(len(raw))])
	}
	var sb strings.Builder
	for _, p := range parts {
		blockType, _ := p["type"].(string)

		switch blockType {
		case "", "text", "input_text", "output_text":
			// Text blocks: extract the "text" field
			if text, ok := p["text"].(string); ok {
				sb.WriteString(text)
			}
		case "thinking":
			// Anthropic thinking blocks carry payload in "thinking".
			if text, ok := p["thinking"].(string); ok {
				sb.WriteString(text)
			}
		case "tool_use":
			// Anthropic tool_use: include id, name, and input
			if id, ok := p["id"].(string); ok {
				sb.WriteString(id)
				sb.WriteString("\x00")
			}
			if name, ok := p["name"].(string); ok {
				sb.WriteString(name)
				sb.WriteString("\x00")
			}
			if input, ok := p["input"]; ok {
				if inputJSON, err := json.Marshal(input); err == nil {
					sb.Write(inputJSON)
				}
			}
		case "tool_result":
			// Anthropic tool_result: include tool_use_id and content
			if toolUseID, ok := p["tool_use_id"].(string); ok {
				sb.WriteString(toolUseID)
				sb.WriteString("\x00")
			}
			if content, ok := p["content"]; ok {
				// content can be string or array
				if contentStr, ok := content.(string); ok {
					sb.WriteString(contentStr)
				} else if contentJSON, err := json.Marshal(content); err == nil {
					sb.Write(contentJSON)
				}
			}
		}

		// Per-block terminator: without it [{"text":"ab"}] and
		// [{"text":"a"},{"text":"b"}] collide on the same fingerprint.
		sb.WriteString("\x00")

		if sb.Len() >= 512 {
			break
		}
	}
	result := sb.String()
	if len(result) > 512 {
		result = result[:512]
	}
	return result
}

func min512(n int) int {
	if n > 512 {
		return 512
	}
	return n
}

// isSummaryMarkerMsg returns true when the message is a gateway-injected
// compaction summary. Two generations of injection must both be recognised:
// the marked form (content starts with CompactionMarkerPrefix, emitted by
// injectSummaryMarker) and the unmarked rebuilders' output (content starts
// with CompressionSummaryPrefix or smartWindowSummaryPrefix — a rebuilt body
// that has not been through injectSummaryMarker yet). Both are non-client
// lineage: the anchor walk, the B-track first-user pin, and the rebuild tail
// filter all rely on excluding them.
func isSummaryMarkerMsg(raw rawMsg) bool {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return isMarkedOrSummaryContent(s)
	}
	// Array content: check the first text part.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				return isMarkedOrSummaryContent(p.Text)
			}
		}
	}
	return false
}

func isMarkedOrSummaryContent(content string) bool {
	if strings.HasPrefix(content, CompactionMarkerPrefix) {
		return true
	}
	return isGatewaySummaryContent(content)
}

// computeHashes returns the MsgHash slice for a messages array.
func computeHashes(msgs []rawMsg) []MsgHash {
	out := make([]MsgHash, 0, len(msgs))
	for i, m := range msgs {
		if h := msgHash(m); h != "" {
			out = append(out, MsgHash{Index: i, SHA256: h})
		}
	}
	return out
}

// estimateBodyTokens is a cheap heuristic. docs/omni-ref3 C4: delegates to the
// shared tokenest helper so the delta-append path and the V2 outbound builder
// use one canonical chars-per-token ratio (previously hardcoded /3.5 here).
func estimateBodyTokens(body []byte) int {
	return tokenest.FromChars(len(body))
}

// spliceBodyMessages is the diff.go internal splice helper. It delegates to
// the package-level spliceMessagesRaw (defined in rebuilder_openai.go) which
// already handles the generic map swap correctly.
func spliceBodyMessages(origBody []byte, newMessages []byte) ([]byte, bool) {
	return spliceMessagesRaw(origBody, newMessages)
}

// preserveAnthropicSystem carries the prior outbound system field through a
// client delta body. Anthropic summaries live in this top-level field, while
// BuildOutboundMessages replaces only messages[].
func preserveAnthropicSystem(lastBody, newBody []byte) []byte {
	var previous, current map[string]json.RawMessage
	if json.Unmarshal(lastBody, &previous) != nil || json.Unmarshal(newBody, &current) != nil {
		return newBody
	}
	system, ok := previous["system"]
	if !ok || len(system) == 0 || string(system) == "null" {
		return newBody
	}

	// P1-12 fix (2026-08-28): Validate system field is well-formed JSON before
	// copying it to the new body. Corrupted cache data could otherwise produce
	// invalid requests that fail at the provider.
	var systemValidation interface{}
	if err := json.Unmarshal(system, &systemValidation); err != nil {
		// system field is not valid JSON; do not copy it
		return newBody
	}

	current["system"] = system
	out, err := json.Marshal(current)
	if err != nil {
		return newBody
	}
	return out
}
