// Package compressor - session_compression.go (v3 T27)
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

package compression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	summarymodel "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SessionCompressorDeps are the external dependencies of SessionCompressor.
// All fields are optional (nil = feature disabled).
type SessionCompressorDeps struct {
	// Cache is the three-tier session state cache (V1, legacy). When nil, every request
	// is treated as a fresh session (no delta-append).
	// DEPRECATED: Use CacheV2 instead. Kept for fallback during V2 migration.
	Cache *SessionCache

	// CacheV2 is the V2 cache architecture that reads from session_turns.
	// When non-nil and Feature Flag is enabled, this takes precedence over Cache.
	CacheV2 *v2.SessionCacheV2

	// Builder is the V2 outbound message builder that reconstructs full context
	// from incremental deltas stored in session_bodies.
	Builder *v2.OutboundBuilder

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

	// Lossiness (rtk borrowing, 2026-07-06) classifies how much information
	// the compression sacrificed, mirroring rtk's Lossiness enum
	// (src/core/toml_filter.rs). Values:
	//
	//	LossinessNone   — no rewrite happened (delta-only or no-op)
	//	LossinessTail   — only the tail was trimmed; the dropped middle is
	//	                  recoverable from the SessionCache / request_logs
	//	                  (mechanical strip / sliding-window trim)
	//	LossinessWhole  — the history was replaced by an LLM summary; the
	//	                  exact wording of dropped turns is NOT recoverable
	//	                  (LLM summary / compaction)
	//
	// Operators use this to decide whether to surface a "context was
	// summarized" notice. It is recorded in the compression_meta JSONB and
	// in the compression_lossiness_total{lossiness} Prometheus counter.
	Lossiness string
}

// Lossiness classification values. Kept as string constants (not a typed
// enum) so they marshal straight into JSON / Prometheus labels without a
// custom Stringer, matching how CompressionStrategy is handled above.
const (
	LossinessNone  = "none"
	LossinessTail  = "tail"
	LossinessWhole = "whole"
)

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
		return sc.fallbackResult(clientBody, res)
	}

	// ── Phase 0: Validate session ID to prevent cross-talk ───────────────
	if err := ValidateSessionID(gwSessionID); err != nil {
		slog.Warn("session_compressor: invalid session_id, treating as new session",
			"session", gwSessionID, "tenant", tenantID, "error", err)
		// Downgrade to sessionless mode to avoid cache pollution
		return sc.fallbackResult(clientBody, res)
	}

	mode := sc.resolveCompressionMode()
	if !settings.GetPlatformBool("compression.enabled", true) {
		mode = ModeDeltaOnly
	}

	// ── Phase 1: Load session state ──────────────────────────────────────
	var (
		state            *SessionState
		lastOutboundBody []byte
	)

	// ── V2 Integration: Try V2 cache first if enabled ────────────────────
	if sc.shouldUseV2(tenantID) {
		slog.InfoContext(ctx, "session_compressor: using v2 cache",
			"session", gwSessionID, "tenant", tenantID)

		v2Body, ok := sc.tryLoadV2State(ctx, tenantID, gwSessionID)
		if ok {
			lastOutboundBody = v2Body
			slog.InfoContext(ctx, "session_compressor: v2 cache loaded successfully",
				"session", gwSessionID, "body_size", len(v2Body))
		} else {
			slog.WarnContext(ctx, "session_compressor: v2 cache failed, falling back to v1",
				"session", gwSessionID)
			// Continue to V1 path below
		}
	}

	// ── V1 Path (existing logic or fallback) ─────────────────────────────
	if len(lastOutboundBody) == 0 && sc.deps.Cache != nil {
		var err error
		state, lastOutboundBody, err = sc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
		if err != nil {
			slog.Warn("session_compressor: cache load failed, treating as new session",
				"session", gwSessionID, "error", err)
		}
	}

	// ── Phase 2: Delta-append (find new turns) ────────────────────────────
	diffResult, err := BuildOutboundMessages(clientBody, state, lastOutboundBody, protocol)
	if err != nil {
		slog.Warn("session_compressor: diff failed, forwarding client body",
			"session", gwSessionID, "error", err)
		return sc.fallbackResult(clientBody, res)
	}

	outboundBody := diffResult.Body
	res.MsgCount = diffResult.MsgCount
	res.TokenEst = diffResult.TokenEst
	res.MsgHashes = marshalHashes(diffResult.MsgHashes)

	// ── Phase 3: Tools caching ────────────────────────────────────────────
	var toolsCached bool
	if state != nil {
		outboundBody, toolsCached = applyToolsCaching(outboundBody, state)
		if toolsCached {
			res.TokenEst = estimateBodyTokens(outboundBody)
		}
	}

	// ── Phase 4: v4 Smart modes ──────────────────────────────────────────
	// delta_only and legacy/off modes never compress: delta-append only.
	if mode == ModeDeltaOnly || (mode != ModeSmart && mode != ModeAggressive) {
		if !diffResult.Unchanged && !diffResult.IsNewSess {
			res.OutboundBody = outboundBody
			res.CompressionStrategy = "delta_append"
		}
		sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, false)
		return res
	}

	// 2026-08-06: split thinking-block strip (always safe) from tool-round
	// strip (destructive, only when compression will actually run). The
	// previous code ran StripToolInfo unconditionally in smart/aggressive
	// mode, which deleted ~97% of an agent's tool history on every request
	// even when the body was far under the context budget. Tool outputs are
	// only safe to drop once an LLM summary has captured them; stripping
	// before the window check destroyed history that the later summary
	// (which may fail) was supposed to preserve.
	//
	// Order is now:
	//   4a. StripThinkingBlocksOnly — always, cheap, non-destructive.
	//   4b. ShouldTriggerWindow — evaluate on the thinking-stripped body.
	//   4c. If triggered: StripToolInfo (completed rounds) right before
	//       the summary/trim that will replace the dropped content.
	//   4d. Task analysis (aggressive only), on the stripped body.
	if mode == ModeSmart || mode == ModeAggressive {
		if stripped, sr := StripThinkingBlocksOnly(outboundBody); sr.DidStrip {
			slog.Info("v4: thinking blocks stripped",
				"thinking_removed", sr.ThinkingRemoved,
				"bytes_before", sr.BytesBefore,
				"bytes_after", sr.BytesAfter)
			outboundBody = stripped
			res.MsgCount = countMessages(outboundBody)
			res.TokenEst = estimateBodyTokens(outboundBody)
			res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
		}
	}

	// ── Phase 5: Window trigger check ─────────────────────────────────────
	winResult := ShouldTriggerWindow(outboundBody, state, contextWindow, streamStarted, time.Now())

	if winResult.SkipStream {
		if !diffResult.Unchanged && !diffResult.IsNewSess {
			res.OutboundBody = outboundBody
			res.CompressionStrategy = "delta_append"
		}
		sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, false)
		return res
	}

	if winResult.ShouldTrigger && (mode == ModeSmart || mode == ModeAggressive) {
		// Only now is it safe to strip completed tool rounds: the window
		// has fired, so a summary or mechanical trim will follow and
		// capture/replace the dropped tool output.
		if stripped, sr := StripToolInfo(outboundBody, protocol); sr.DidStrip {
			slog.Info("v4: tool info stripped",
				"tools_removed", sr.ToolCallsRemoved,
				"thinking_removed", sr.ThinkingRemoved,
				"bytes_before", sr.BytesBefore,
				"bytes_after", sr.BytesAfter)
			outboundBody = stripped
			res.MsgCount = countMessages(outboundBody)
			res.TokenEst = estimateBodyTokens(outboundBody)
			res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
			if state != nil {
				state.StripsApplied++
				state.MessagesAfterStrip = res.MsgCount
				state.TokensAfterStrip = res.TokenEst
				state.LastStripAt = time.Now().Unix()
			}
		}

		// ── Task analysis (aggressive mode only) ──────────────────────
		if mode == ModeAggressive {
			msgs := mustExtractMessages(outboundBody)
			msgMaps := make([]map[string]any, 0, len(msgs))
			for _, m := range msgs {
				var msg map[string]any
				if json.Unmarshal(m, &msg) == nil {
					msgMaps = append(msgMaps, msg)
				}
			}
			taskResult := AnalyzeTasks(msgMaps)
			if taskResult.HasAnalysis {
				slog.Info("v4: task analysis completed",
					"completed_tasks", taskResult.CompletedCount,
					"active_tasks", taskResult.ActiveCount)
				if state != nil {
					state.CompletedTasks += taskResult.CompletedCount
				}
			}
		}

		res.WindowTriggered = winResult.Reason

		if winResult.Degraded {
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
			summarised, ok := sc.tryLLMSummary(ctx, outboundBody, tenantID, protocol, taskType)
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

	// ── Lossiness classification (rtk borrowing, 2026-07-06) ───────────────
	// Map the strategy that fired to a recoverability class so operators can
	// tell from metrics whether compression dropped recoverable tail content
	// or irrecoverable summary-replaced history. See PrepareResult.Lossiness.
	res.Lossiness = classifyLossiness(res.CompressionStrategy, res.SummaryMarker)
	if res.CompressionStrategy != "" {
		RecordLossiness(res.Lossiness)
	}

	// ── Persist updated cache state ──────────────────────────────────────
	didCompress := winResult.ShouldTrigger && !winResult.Degraded && res.SummaryMarker != ""
	sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, didCompress)

	return res
}

// classifyLossiness maps a compression strategy to a recoverability class.
//
//   - delta_append / strip / "" → none: no information was lost (delta-append
//     only adds turns; strip removes tool/thinking blocks that are redundant
//     with the tool registry and are recoverable from the session cache).
//   - mechanical_trim → tail: the middle of the conversation was dropped but
//     remains recoverable from the SessionCache L1/L2/L3 tiers.
//   - sliding_window_* WITH a summary_marker → whole: the history was replaced
//     by an LLM summary; the exact wording of dropped turns is NOT recoverable.
func classifyLossiness(strategy, summaryMarker string) string {
	switch {
	case strategy == "":
		return LossinessNone
	case strategy == "delta_append":
		return LossinessNone
	case strategy == "mechanical_trim":
		return LossinessTail
	case strings.HasPrefix(strategy, "sliding_window_"):
		if summaryMarker != "" {
			return LossinessWhole
		}
		return LossinessTail
	default:
		// Unknown strategy (e.g. a future rebuilder) — be conservative:
		// assume recoverable tail unless a summary marker says otherwise.
		if summaryMarker != "" {
			return LossinessWhole
		}
		return LossinessTail
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

// resolveCompressionMode returns the effective v4 compression mode.
// Priority: SessionState mode → env mode → default (ModeSmart).
func (sc *SessionCompressor) resolveCompressionMode() Mode {
	return LoadMode()
}

func (sc *SessionCompressor) tryLLMSummary(ctx context.Context, body []byte, tenantID, protocol, taskType string) ([]byte, bool) {
	if sc.deps.CompactionDeps == nil {
		return nil, false
	}

	conversation, err := extractConversationText(body, protocol)
	if err != nil || strings.TrimSpace(conversation) == "" {
		return nil, false
	}
	conversation = trimTextToTokenBudget(conversation, 900_000)

	dim := summarymodel.DimensionForTaskType(taskType)
	summarizer := summarymodel.NewSummarizer(newSummaryClientAdapter(sc.deps.CompactionDeps, "", tenantID))
	summaryText, sumErr := summarizer.Summarize(ctx, dim, conversation)
	if sumErr == nil && strings.TrimSpace(summaryText) != "" {
		if rebuilt, ok := rebuildBodyAfterSummary(body, strings.TrimSpace(summaryText), protocol); ok {
			return rebuilt, true
		}
	}

	newBody, ok := tryLLMContextCompaction(ctx, sc.deps.CompactionDeps, "", protocol, body)
	if !ok {
		return nil, false
	}
	return newBody, true
}

func rebuildBodyAfterSummary(body []byte, summaryText, protocol string) ([]byte, bool) {
	if strings.TrimSpace(summaryText) == "" {
		return nil, false
	}
	if protocol == "anthropic-messages" {
		ret, err := extractAnthropic(body)
		if err != nil {
			return nil, false
		}
		return RebuildAnthropicAfterSummary(body, summaryText, ret, 2)
	}
	ret, err := extractOpenAI(body)
	if err != nil {
		return nil, false
	}
	return RebuildOpenAIAfterSummary(body, summaryText, ret, 2)
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
		// ── Phase 1 optimization: preserve tools cache fields ──
		newState.ToolsHash = prevState.ToolsHash
		newState.SystemPrompt = prevState.SystemPrompt
	}
	// Always track LastCompressedAt so Redis lcat reflects the last cache
	// update time, even for pure delta-append (no LLM summary). This lets
	// operators distinguish "session has been touched" from "never visited".
	newState.LastCompressedAt = now
	if didCompress {
		newState.RecentlyCompressedAt = now
	}
	if err := sc.deps.Cache.Set(ctx, tenantID, gwSessionID, newState, outboundBody); err != nil {
		slog.Warn("session_compressor: cache set failed", "session", gwSessionID, "error", err)
	}
}

func (sc *SessionCompressor) fallbackResult(clientBody []byte, res *PrepareResult) *PrepareResult {
	if len(clientBody) > 0 {
		hashes := computeHashes(mustExtractMessages(clientBody))
		res.MsgHashes = marshalHashes(hashes)
		res.MsgCount = countMessages(clientBody)
		res.TokenEst = estimateBodyTokens(clientBody)
	}
	return res
}

// mechanicalTrim calls the existing v7 mechanical trim path.
func mechanicalTrim(body []byte, contextWindow int, protocol string) []byte {
	if contextWindow <= 0 {
		return body
	}
	if protocol == "anthropic-messages" {
		return transformation.CompressAnthropicMessagesIfNeeded(body, contextWindow)
	}
	return transformation.CompressMessagesIfNeeded(body, contextWindow)
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
		var msg map[string]json.RawMessage
		if json.Unmarshal(m, &msg) != nil {
			continue
		}
		var role string
		if json.Unmarshal(msg["role"], &role) != nil || role != "assistant" {
			continue
		}
		var content string
		if json.Unmarshal(msg["content"], &content) != nil {
			continue
		}
		marker = BuildSummaryMarker(content)
		// Prepend marker to content while preserving all other message fields.
		newContent, err := json.Marshal(marker + "\n" + content)
		if err != nil {
			return "", nil
		}
		msg["content"] = newContent
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

// WithTaskType stores the task_type in a context for the session compression.
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

// applyToolsCaching checks if tools array has changed since last request.
// If unchanged, removes tools from outboundBody and adds "_tools_cached": true marker.
// Updates state.ToolsHash for next comparison.
// Phase 1 optimization: reduces 50KB+ tools re-transmission to ~5 bytes.
func applyToolsCaching(outboundBody []byte, state *SessionState) ([]byte, bool) {
	if state == nil {
		return outboundBody, false
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(outboundBody, &body); err != nil {
		return outboundBody, false
	}

	toolsRaw, hasTools := body["tools"]
	if !hasTools || len(toolsRaw) == 0 {
		return outboundBody, false
	}

	// Compute SHA256 of tools array
	currentHash := sha256Hex(toolsRaw)

	// Check if tools changed
	if state.ToolsHash != "" && state.ToolsHash == currentHash {
		// Tools unchanged → remove from body, add cache marker
		delete(body, "tools")
		body["_tools_cached"] = json.RawMessage(`true`)
		modified, _ := json.Marshal(body)
		return modified, true
	}

	// Tools changed or first time → update state hash
	state.ToolsHash = currentHash
	return outboundBody, false
}

// sha256Hash computes the SHA256 hash of the given data and returns it as a hex string.
func sha256Hash(data any) string { //nolint:unused
	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ─────────────────────────────────────────────────────────────
// V2 Integration Helpers (Phase V2-2.4)
// ─────────────────────────────────────────────────────────────

// shouldUseV2 determines whether to read session state from the V2 store
// (gateway.session_bodies) instead of V1 (request_logs LCS delta).
//
// Returns true when:
//  1. V2 components (CacheV2 + Builder) are wired, AND
//  2. The platform flag "sessions_v2_compression_read" is true.
//
// The flag defaults to TRUE (docs/omni-ref3 A1 — decision: compress+summary
// cut over together, default-on, no canary). It is a kill-switch: setting it
// to false in settings_kv hot-reloads V1 reads back immediately, no redeploy.
// The tenantID arg is retained for a future per-tenant override; today the
// decision is platform-wide (no per-tenant bool setting exists).
//
// Fail-open: if V2 read itself errors, tryLoadV2State returns ok=false and the
// caller falls back to V1 for that request regardless of this flag.
func (sc *SessionCompressor) shouldUseV2(tenantID string) bool {
	if sc == nil {
		return false
	}
	if sc.deps.CacheV2 == nil || sc.deps.Builder == nil {
		return false
	}
	return settings.GetPlatformBool("sessions_v2_compression_read", true)
}

// tryLoadV2State attempts to load session state from V2 architecture.
//
// Returns:
//   - lastOutboundBody: the message array last sent to LLM (JSON marshaled)
//   - ok: true if V2 load succeeded, false to fallback to V1
//
// On V2 error, automatically logs and returns ok=false for V1 fallback.
func (sc *SessionCompressor) tryLoadV2State(
	ctx context.Context,
	tenantID, sessionID string,
) (lastOutboundBody []byte, ok bool) {
	// Call CacheV2.Get() - returns *SessionStateV2
	state, err := sc.deps.CacheV2.Get(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 cache get failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	if state == nil {
		// New session, no previous state
		return nil, true
	}

	// Call Builder.BuildFromLatestOutbound() - returns ([]Message, *BuildMeta, error)
	messages, _, err := sc.deps.Builder.BuildFromLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 build from outbound failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	// Marshal messages to JSON
	outboundBody, err := json.Marshal(messages)
	if err != nil {
		slog.WarnContext(ctx, "v2 marshal messages failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	return outboundBody, true
}
