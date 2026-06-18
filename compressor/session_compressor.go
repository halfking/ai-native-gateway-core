// Package compressor - session_compressor.go (v3 T27)
//
// SessionCompressor is the top-level orchestrator for v3 session-level
// intelligent compression. It wires together:
//
//  1. SessionCache  (session_cache.go) — three-tier L1/L2/L3 state store
//  2. BuildOutboundMessages (diff.go)  — message-level LCS delta-append
//  3. ShouldTriggerWindow  (window.go) — proactive sliding-window triggers
//  4. tryLLMContextCompaction (compaction.go) — LOSSLESS LLM summary (v7)
//
// Compression philosophy ("有特色，尽量不丢失内容"):
//
//	LOSSLESS_FIRST: prefer LLM summary over mechanical trim. The enhanced
//	compactionSystemPrompt (v3 T22) instructs the LLM to preserve ALL exact
//	values (IDs, paths, error messages, numbers) and quote critical statements
//	verbatim. Mechanical trim is the LAST resort, only when:
//	  a) LLM summary fails (timeout / model unavailable), OR
//	  b) The mutual-exclusion window is active (Degraded=true).
//
// Prepare is the single entry point called by the chat handler before
// forwarding the request to the upstream LLM.

package compressor

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/transform"
)

// SessionCompressorDeps are the external dependencies of SessionCompressor.
// All fields are optional (nil = feature disabled).
type SessionCompressorDeps struct {
	// Cache is the three-tier session state cache. When nil, every request
	// is treated as a fresh session (no delta-append).
	Cache *SessionCache

	// CompactionDeps provides the Memora + Provider clients needed by
	// tryLLMContextCompaction. When nil, LLM summary is skipped and the
	// compressor falls back to mechanical trim.
	CompactionDeps *Dependencies

	// Disabled completely disables the session compressor when true.
	// Reads LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE env var at startup.
	Disabled bool
}

// PrepareResult is the output of SessionCompressor.Prepare.
type PrepareResult struct {
	// OutboundBody is the body to forward to the upstream LLM.
	// Nil when no rewrite was needed (forward clientBody as-is).
	OutboundBody []byte

	// MsgHashes is the per-message fingerprint array to persist in
	// request_logs.outbound_msg_hashes.
	MsgHashes json.RawMessage

	// MsgCount is the number of messages in OutboundBody (or clientBody
	// when OutboundBody is nil).
	MsgCount int

	// TokenEst is the token estimate for OutboundBody.
	TokenEst int

	// CompressionStrategy is the strategy that fired (or "" = no rewrite).
	// Written to request_logs.compression_strategy.
	CompressionStrategy string

	// WindowTriggered is the window trigger reason (or "" = no trigger).
	// Stored inside compression_meta JSONB as window_triggered.
	WindowTriggered string

	// SummaryMarker is the smm_v1 marker if an LLM summary was written.
	SummaryMarker string

	// Degraded is true when the mutual-exclusion window was active and
	// mechanical trim was used instead of LLM summary.
	Degraded bool
}

// SessionCompressor orchestrates v3 session-level compression.
type SessionCompressor struct {
	deps SessionCompressorDeps
}

// NewSessionCompressor builds a SessionCompressor. Call once at startup.
func NewSessionCompressor(deps SessionCompressorDeps) *SessionCompressor {
	return &SessionCompressor{deps: deps}
}

// Prepare is the main entry point. Call it after reading the client body
// but before routing/forwarding.
//
//   - clientBody: the raw request body from the client.
//   - tenantID: tenant identifier for the session cache key.
//   - gwSessionID: X-Gw-Session-Id header value (empty = no session).
//   - protocol: "openai" or "anthropic-messages".
//   - contextWindow: target model context window in tokens (0 = unknown).
//   - streamStarted: true when streaming response has started.
func (sc *SessionCompressor) Prepare(
	ctx context.Context,
	clientBody []byte,
	tenantID, gwSessionID, protocol string,
	contextWindow int,
	streamStarted bool,
) *PrepareResult {
	res := &PrepareResult{}

	if sc == nil || sc.deps.Disabled || gwSessionID == "" {
		// No session context — forward as-is.
		if len(clientBody) > 0 {
			hashes := computeHashes(mustExtractMessages(clientBody))
			res.MsgHashes = marshalHashes(hashes)
			res.MsgCount = countMessages(clientBody)
			res.TokenEst = estimateBodyTokens(clientBody)
		}
		return res
	}

	// ── Load session state ───────────────────────────────────────────────
	var (
		state           *SessionState
		lastOutboundBody []byte
	)
	if sc.deps.Cache != nil {
		var err error
		state, lastOutboundBody, err = sc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
		if err != nil {
			slog.Warn("session_compressor: cache load failed, treating as new session",
				"session", gwSessionID, "error", err)
		}
	}

	// ── Delta-append: find new turns, build outbound body ────────────────
	diffResult, err := BuildOutboundMessages(clientBody, state, lastOutboundBody, protocol)
	if err != nil {
		slog.Warn("session_compressor: diff failed, forwarding client body",
			"session", gwSessionID, "error", err)
		return sc.fallbackResult(clientBody)
	}

	outboundBody := diffResult.Body
	res.MsgCount = diffResult.MsgCount
	res.TokenEst = diffResult.TokenEst
	res.MsgHashes = marshalHashes(diffResult.MsgHashes)

	// ── Window trigger check ─────────────────────────────────────────────
	winResult := ShouldTriggerWindow(outboundBody, state, contextWindow, streamStarted, time.Now())

	if winResult.SkipStream {
		// Stream already started — no rewrite possible.
		if !diffResult.Unchanged && !diffResult.IsNewSess {
			res.OutboundBody = outboundBody
			res.CompressionStrategy = "delta_append"
		}
		sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, false)
		return res
	}

	if winResult.ShouldTrigger {
		res.WindowTriggered = winResult.Reason

		if winResult.Degraded {
			// Mutual-exclusion guard active — degrade to mechanical trim.
			res.Degraded = true
			trimmed := mechanicalTrim(outboundBody, contextWindow, protocol)
			if len(trimmed) < len(outboundBody) {
				outboundBody = trimmed
				res.OutboundBody = outboundBody
				res.CompressionStrategy = "mechanical_trim"
				res.MsgCount = countMessages(outboundBody)
				res.TokenEst = estimateBodyTokens(outboundBody)
				res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
			}
		} else {
			// ── LOSSLESS_FIRST: try LLM summary ──────────────────────────
			taskType := extractTaskType(ctx)
			summarised, ok := sc.tryLLMSummary(ctx, outboundBody, protocol, taskType)
			if ok && len(summarised) > 0 && len(summarised) < len(outboundBody) {
				// LLM summary succeeded — inject summary_marker.
				marker, markedBody := injectSummaryMarker(summarised, protocol)
				if markedBody != nil {
					outboundBody = markedBody
					res.SummaryMarker = marker
				} else {
					outboundBody = summarised
				}
				res.OutboundBody = outboundBody
				res.CompressionStrategy = "sliding_window_" + strings.TrimPrefix(winResult.Reason, "sliding_window_")
				res.MsgCount = countMessages(outboundBody)
				res.TokenEst = estimateBodyTokens(outboundBody)
				res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
			} else {
				// LLM summary failed or didn't shrink — fall back to mechanical trim.
				slog.Info("session_compressor: LLM summary failed/no-op, falling back to mechanical trim",
					"session", gwSessionID, "trigger", winResult.Reason)
				trimmed := mechanicalTrim(outboundBody, contextWindow, protocol)
				if len(trimmed) < len(outboundBody) {
					outboundBody = trimmed
					res.OutboundBody = outboundBody
					res.CompressionStrategy = "mechanical_trim"
					res.MsgCount = countMessages(outboundBody)
					res.TokenEst = estimateBodyTokens(outboundBody)
					res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
				}
			}
		}
	} else if !diffResult.Unchanged && !diffResult.IsNewSess {
		// No window trigger, but delta-append rewrote the body.
		res.OutboundBody = outboundBody
		res.CompressionStrategy = "delta_append"
	}

	// ── Persist updated cache state ──────────────────────────────────────
	didCompress := winResult.ShouldTrigger && !winResult.Degraded && res.SummaryMarker != ""
	sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, didCompress)

	return res
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

func (sc *SessionCompressor) tryLLMSummary(ctx context.Context, body []byte, protocol, taskType string) ([]byte, bool) {
	if sc.deps.CompactionDeps == nil {
		return nil, false
	}
	newBody, ok := tryLLMContextCompaction(ctx, sc.deps.CompactionDeps, "", protocol, body)
	if !ok {
		return nil, false
	}
	return newBody, true
}

func (sc *SessionCompressor) updateCache(
	ctx context.Context,
	tenantID, gwSessionID string,
	prevState *SessionState,
	outboundBody []byte,
	res *PrepareResult,
	didCompress bool,
) {
	if sc.deps.Cache == nil {
		return
	}
	now := time.Now().Unix()
	newState := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: sha256Hex(outboundBody),
		MsgCount:         res.MsgCount,
		TokenEstimate:    res.TokenEst,
		SummaryMarker:    res.SummaryMarker,
	}
	if prevState != nil {
		newState.LastCompressedAt = prevState.LastCompressedAt
		newState.RecentlyCompressedAt = prevState.RecentlyCompressedAt
	}
	if didCompress {
		newState.LastCompressedAt = now
		newState.RecentlyCompressedAt = now
	}
	if err := sc.deps.Cache.Set(ctx, tenantID, gwSessionID, newState, outboundBody); err != nil {
		slog.Warn("session_compressor: cache set failed", "session", gwSessionID, "error", err)
	}
}

func (sc *SessionCompressor) fallbackResult(clientBody []byte) *PrepareResult {
	hashes := computeHashes(mustExtractMessages(clientBody))
	return &PrepareResult{
		MsgHashes: marshalHashes(hashes),
		MsgCount:  countMessages(clientBody),
		TokenEst:  estimateBodyTokens(clientBody),
	}
}

// mechanicalTrim calls the existing v7 mechanical trim path.
func mechanicalTrim(body []byte, contextWindow int, protocol string) []byte {
	if contextWindow <= 0 {
		return body
	}
	if protocol == "anthropic-messages" {
		return transform.CompressAnthropicMessagesIfNeeded(body, contextWindow)
	}
	return transform.CompressMessagesIfNeeded(body, contextWindow)
}

// injectSummaryMarker wraps the summarised body so the first assistant
// message content is prefixed with the smm_v1 marker. Returns the marker
// string and the new body (nil body = injection failed, use raw summarised).
func injectSummaryMarker(summarisedBody []byte, protocol string) (marker string, newBody []byte) {
	// Extract the first assistant message content.
	msgs, err := extractMessages(summarisedBody)
	if err != nil || len(msgs) == 0 {
		return "", nil
	}
	// Find the first assistant message.
	for i, m := range msgs {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(m, &msg) != nil || msg.Role != "assistant" {
			continue
		}
		marker = BuildSummaryMarker(msg.Content)
		// Prepend marker to content.
		msg.Content = marker + "\n" + msg.Content
		newMsgBytes, err := json.Marshal(msg)
		if err != nil {
			return "", nil
		}
		msgs[i] = newMsgBytes

		newMsgsRaw, err := json.Marshal(msgs)
		if err != nil {
			return "", nil
		}
		nb, ok := spliceBodyMessages(summarisedBody, newMsgsRaw)
		if !ok {
			return "", nil
		}
		return marker, nb
	}
	// No assistant message found — store marker without injection.
	if len(summarisedBody) > 0 {
		marker = BuildSummaryMarker(string(summarisedBody[:min512(len(summarisedBody))]))
	}
	return marker, summarisedBody
}

// extractTaskType retrieves the task_type from the request context if set
// by the auto-route decider. Falls back to "" (default prompt).
func extractTaskType(ctx context.Context) string {
	if v, ok := ctx.Value(taskTypeCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// taskTypeCtxKey is the context key for propagating task_type.
type taskTypeCtxKey struct{}

// WithTaskType stores the task_type in a context for the session compressor.
func WithTaskType(ctx context.Context, taskType string) context.Context {
	return context.WithValue(ctx, taskTypeCtxKey{}, taskType)
}

func mustExtractMessages(body []byte) []rawMsg {
	msgs, _ := extractMessages(body)
	return msgs
}

func countMessages(body []byte) int {
	return len(mustExtractMessages(body))
}

func marshalHashes(hashes []MsgHash) json.RawMessage {
	if len(hashes) == 0 {
		return nil
	}
	b, _ := json.Marshal(hashes)
	return b
}
