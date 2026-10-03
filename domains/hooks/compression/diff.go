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
// Algorithm — message-level LCS fingerprint:
//
//	For each message compute sha256(role + "\x00" + contentKey + "\x00" + toolID)
//	where contentKey is the first 512 bytes of the string-normalised content.
//	Walk the client messages from the END looking for the last message whose
//	hash appears anywhere in the last outbound body. Everything after that
//	index in the client array is "new". Append to last outbound, done.
//
// Summary marker preservation:
//
//	Any message in lastOutbound whose "content" string starts with
//	CompactionMarkerPrefix is a gateway-injected summary. It is kept verbatim
//	in the rebuilt body and its hash is deliberately excluded from the LCS
//	index so the diff algo never mistakes it for a client-sent message.
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
	// client's prefix. Compressed sessions retain gateway-only summary content
	// (stamped [smm_v1:] markers, unstamped rebuilder dynCtx messages, or the
	// Anthropic top-level system field); for those, the retained layout is the
	// two-segment [first-user head][contiguous recent tail], matched against a
	// unique client range. Ambiguous matches fail open rather than dropping a
	// client message by choosing the wrong anchor.
	anchorEnd, ok := findDeltaAnchor(clientMsgs, lastMsgs,
		protocol == "anthropic-messages" && hasAnthropicSystemSummary(lastOutboundBody))
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

// findDeltaAnchor locates the exclusive end index of the client-history range
// that the last outbound body already carries.
//
// Uncompressed outbound (no gateway summary anywhere): the whole prior
// sequence must be the client's exact prefix.
//
// Compressed outbound: the rebuilders retain a two-segment layout — the
// B-track first user (plus any preceding reminders) and a contiguous recent
// tail, separated by the summarized gap (rebuilder_anthropic.go /
// rebuilder_openai.go splitSystemAndTail). That layout is never a contiguous
// subsequence of the client history, so a single contiguous match cannot
// anchor (the structural fail-open pinned by anthropic_failopen_repro_test.go
// before the two-segment fix). Instead every head/tail split point is tried:
// the head must equal the client prefix and the tail must occur exactly once
// from there on. All valid splits provably agree on the anchor end (a longer
// head is the shorter head extended by messages that match the client prefix,
// which shifts the unique tail occurrence by exactly the head growth), and a
// tail that occurs more than once refuses to anchor — the same conservative
// contract as a duplicated contiguous suffix.
func findDeltaAnchor(clientMsgs, lastMsgs []rawMsg, summaryInSystem bool) (int, bool) {
	lastComparable := make([]rawMsg, 0, len(lastMsgs))
	hasGatewaySummary := summaryInSystem
	for _, message := range lastMsgs {
		if isSummaryMarkerMsg(message) || isUnmarkedGatewaySummaryMsg(message) {
			hasGatewaySummary = true
			continue
		}
		lastComparable = append(lastComparable, message)
	}
	if len(lastComparable) == 0 {
		return 0, false
	}

	if !hasGatewaySummary {
		if len(clientMsgs) < len(lastComparable) || !sameMessageSequence(clientMsgs[:len(lastComparable)], lastComparable) {
			return 0, false
		}
		return len(lastComparable), true
	}
	return findTwoSegmentAnchor(clientMsgs, lastComparable)
}

// findTwoSegmentAnchor matches comparable[:split] against the client prefix
// and comparable[split:] against one unique client run for every split point.
// Message identity is established once via msgHash, so the scan is string
// equality; an unhashable (unparseable) message on either side refuses to
// anchor rather than guessing around the gap in identity.
func findTwoSegmentAnchor(clientMsgs, lastComparable []rawMsg) (int, bool) {
	clientHashes := make([]string, len(clientMsgs))
	for i, m := range clientMsgs {
		clientHashes[i] = msgHash(m)
		if clientHashes[i] == "" {
			return 0, false
		}
	}
	comparableHashes := make([]string, len(lastComparable))
	for i, m := range lastComparable {
		comparableHashes[i] = msgHash(m)
		if comparableHashes[i] == "" {
			return 0, false
		}
	}

	for split := 0; split < len(comparableHashes); split++ {
		if split > len(clientHashes) || !equalHashStrings(comparableHashes[:split], clientHashes[:split]) {
			continue
		}
		tail := comparableHashes[split:]
		matches := 0
		end := 0
		for start := split; start+len(tail) <= len(clientHashes); start++ {
			if equalHashStrings(tail, clientHashes[start:start+len(tail)]) {
				matches++
				if matches > 1 {
					break
				}
				end = start + len(tail)
			}
		}
		if matches != 1 {
			continue
		}
		// Valid splits cannot disagree (see findDeltaAnchor): return the first.
		return end, true
	}
	return 0, false
}

func equalHashStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// isUnmarkedGatewaySummaryMsg reports whether a message is a gateway-built
// summary that injectSummaryMarker never stamped with [smm_v1:] — the OpenAI
// rebuilder's dynCtx message (CompressionSummaryPrefix) and the smart-window
// summary prefix generation. Such messages never exist in a client-authored
// history, so they are excluded from lineage matching exactly like stamped
// markers; leaving them in the comparable set would force the strict prefix
// branch and fail every unstamped compressed body open.
func isUnmarkedGatewaySummaryMsg(raw rawMsg) bool {
	if isSummaryMarkerMsg(raw) {
		return false
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return isGatewaySummaryContent(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				return isGatewaySummaryContent(p.Text)
			}
		}
	}
	return false
}

func sameMessageSequence(a, b []rawMsg) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, right := msgHash(a[i]), msgHash(b[i])
		if left == "" || right == "" || left != right {
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
// compaction summary (content starts with CompactionMarkerPrefix).
func isSummaryMarkerMsg(raw rawMsg) bool {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return strings.HasPrefix(s, CompactionMarkerPrefix)
	}
	// Array content: check the first text part.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				return strings.HasPrefix(p.Text, CompactionMarkerPrefix)
			}
		}
	}
	return false
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
