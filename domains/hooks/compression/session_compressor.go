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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/cache/prefix"
	summarymodel "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary"
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

	// CacheV2 is the V2 session-state reader. Non-nil + feature flag on
	// makes the V2 read path take precedence over Cache.
	//
	// Defined as a local interface (not *v2.SessionCacheV2) so the
	// compression package does not import the v2 package and so tests
	// can inject a stub without a live database.
	CacheV2 V2StateReader

	// Builder rebuilds the last outbound body (JSON messages array) from
	// the V2 incremental tables. Defined as a local interface for the
	// same reasons as CacheV2.
	Builder V2OutboundBuilder

	// CompactionDeps provides the Memora + Provider clients needed by
	// tryLLMContextCompaction. When nil, LLM summary is skipped and the
	// compressor falls back to mechanical trim.
	CompactionDeps *Dependencies

	// ResultMemo caches compression results to avoid redundant computation
	// (docs/omni-ref3 C3). When nil, memo is disabled.
	ResultMemo *ResultMemo

	// Disabled completely disables the session compressor when true.
	// Reads LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE env var at startup.
	Disabled bool
}

// V2StateReader is the minimal slice of *v2.SessionCacheV2 that
// tryLoadV2State consumes: it only needs to know whether prior state
// exists for a session and whether that lookup errored.
type V2StateReader interface {
	// HasState reports whether any prior turn state exists for the
	// session. A (false, nil) result means "new session" and is NOT
	// an error — tryLoadV2State treats it as ok=true with an empty
	// body so the caller proceeds as a fresh session.
	HasState(ctx context.Context, tenantID, sessionID string) (bool, error)
}

// V2CompressionMetadataReader is an optional extension implemented by the V2
// cache. Keeping it separate preserves compatibility with lightweight readers
// used during rollout and in unit tests.
type V2CompressionMetadataReader interface {
	CompressionMetadata(ctx context.Context, tenantID, sessionID string) (map[string]any, error)
}

// V2OutboundBuilder rebuilds the most recent outbound body (the exact
// message array last forwarded to the upstream model, including any
// compression markers) as a JSON-marshaled []byte.
type V2OutboundBuilder interface {
	BuildLatestOutbound(ctx context.Context, tenantID, sessionID string) ([]byte, error)
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

	// TokenBand records the threshold classification of the fully assembled
	// outbound body (prior compressed session layer plus current delta).
	TokenBand OutboundTokenBand

	// PriorLayerTokens is the cached estimate of the previous outbound layer.
	PriorLayerTokens int

	// CompressionReason distinguishes an absolute threshold rewrite from the
	// existing context-window/count/idle triggers.
	CompressionReason string

	// CompressionStrategy is the strategy that fired (or "" = no rewrite).
	// Written to request_logs.compression_strategy.
	CompressionStrategy string

	// WindowTriggered is the window trigger reason (or "" = no trigger).
	// Stored inside compression_meta JSONB as window_triggered.
	WindowTriggered string

	// SummaryMarker is the smm_v1 marker if an LLM summary was written.
	SummaryMarker string

	// CompressedPrefixHash is the SHA256 hex of the stable prefix (System +
	// Tool + History, excluding TailClass) computed by cache/prefix.Stabilize.
	// docs/omni-ref3 C8/D7: this hash is the key for semantic cache lookups
	// and is persisted in cache_v2.CompressedPrefixHash. Empty when Stabilize
	// was not called or the prefix is all-tail.
	CompressedPrefixHash string

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

	// AlignmentMap (O-2, 2026-08-09) is the original→compressed message
	// index mapping when a window-triggered rewrite (summary/trim) fired.
	// Nil otherwise. Persisted into SessionState for audit tracing.
	AlignmentMap []AlignmentInfo

	// skipV1Cache remains internal: V2 owns its write side, so a final handler
	// commit must not back-fill the legacy cache for a V2-sourced request.
	skipV1Cache bool
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
	deps    SessionCompressorDeps
	breaker *summaryBreaker // docs/omni-ref3 C1: gates the LLM-summary path
}

// NewSessionCompressor builds a SessionCompressor. Call once at startup.
func NewSessionCompressor(deps SessionCompressorDeps) *SessionCompressor {
	return &SessionCompressor{deps: deps, breaker: newSummaryBreaker()}
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

	// ── Phase 0a: Enforce hard body size limit (P1-10) ───────────────────
	const maxBodySize = 50 * 1024 * 1024 // 50 MB
	if len(clientBody) > maxBodySize {
		slog.Warn("session_compressor: body exceeds 50MB limit, rejecting compression",
			"session", gwSessionID, "tenant", tenantID, "body_size", len(clientBody), "limit", maxBodySize)
		// Return original body without compression to avoid OOM
		return sc.fallbackResult(clientBody, res)
	}

	// ── Phase 0b: Validate session ID to prevent cross-talk ──────────────
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
	// fromV2 records whether THIS request was served from the V2 read path,
	// so updateCache can skip writing back into the V1 cache (see rationale
	// in updateCache). It stays false for the V1 path and any fallback.
	fromV2 := false
	if sc.shouldUseV2(tenantID) {
		slog.InfoContext(ctx, "session_compressor: using v2 cache",
			"session", gwSessionID, "tenant", tenantID)

		v2Body, ok := sc.tryLoadV2State(ctx, tenantID, gwSessionID)
		if ok {
			lastOutboundBody = v2Body
			state = sc.loadV2CompressionState(ctx, tenantID, gwSessionID)
			if state == nil {
				// A reader that only supports HasState is still valid; a
				// non-nil placeholder keeps the delta path active.
				state = &SessionState{}
			}

			fromV2 = true
			res.skipV1Cache = true
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

	// memoInputForStore is the body as it entered the expensive compression
	// stage; it is the memo key input (docs/omni-ref3 C3). Set right before the
	// summary/trim so the store path keys on exactly what the lookup path did.
	var memoInputForStore []byte

	// ── Phase 3: Tools caching ────────────────────────────────────────────
	var toolsCached bool
	if state != nil {
		outboundBody, toolsCached = applyToolsCaching(outboundBody, state)
		if toolsCached {
			res.TokenEst = estimateBodyTokens(outboundBody)
		}
	}

	// ── Phase 4: v4 Smart modes ──────────────────────────────────────────
	// Keep explicit off/delta-only modes authoritative. Other legacy modes can
	// be promoted for this request when the assembled outbound body exceeds the
	// absolute threshold; the later window check re-evaluates after safe strips.
	preliminaryBand := classifyOutboundTokenBand(res.TokenEst)
	res.TokenBand = preliminaryBand
	if state != nil {
		res.PriorLayerTokens = state.TokenEstimate
	}
	switch preliminaryBand {
	case OutboundTokenBandForced:
		res.CompressionReason = "token_threshold_forced_absolute"
		if mode != ModeOff && mode != ModeDeltaOnly {
			mode = ModeSmart
		}
	case OutboundTokenBandPreliminary:
		res.CompressionReason = "token_threshold_preliminary"
	}

	if mode == ModeOff || mode == ModeDeltaOnly || (mode != ModeSmart && mode != ModeAggressive) {
		if !diffResult.Unchanged && !diffResult.IsNewSess {
			res.OutboundBody = outboundBody
			res.CompressionStrategy = "delta_append"
		}
		sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, false, fromV2)
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
	res.TokenBand = winResult.TokenBand
	res.PriorLayerTokens = winResult.PriorLayerTokens
	res.CompressionReason = ""
	switch winResult.TokenBand {
	case OutboundTokenBandForced:
		res.CompressionReason = "token_threshold_forced_absolute"
	case OutboundTokenBandPreliminary:
		res.CompressionReason = "token_threshold_preliminary"
	}

	if winResult.SkipStream {
		if !diffResult.Unchanged && !diffResult.IsNewSess {
			res.OutboundBody = outboundBody
			res.CompressionStrategy = "delta_append"
		}
		sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, false, fromV2)
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

		// ── Result memo lookup (docs/omni-ref3 C3) ─────────────────────
		// Everything below this point is the expensive work: an LLM summary
		// (seconds + upstream quota) or a mechanical trim over a large body.
		// Key on the body AS IT ENTERS compression (post strip/tools-caching)
		// plus tenant+session+mode+protocol+contextWindow, so a retry of the
		// same turn replays the previous result instead of paying again.
		memoParts := MemoKeyParts{
			TenantID:      tenantID,
			SessionID:     gwSessionID,
			Mode:          mode.String(),
			Protocol:      protocol,
			ContextWindow: contextWindow,
		}
		memoInputForStore = outboundBody
		if sc.deps.ResultMemo.enabled() {
			cached, err := sc.deps.ResultMemo.Get(ctx, memoParts, memoInputForStore)
			if err != nil {
				// Treat any memo error as a miss: the compression path below is
				// always correct, the memo is only an optimisation.
				slog.WarnContext(ctx, "session_compressor: memo lookup failed, recomputing",
					"session", gwSessionID, "error", err)
			}
			if cached != nil {
				RecordMemo(MemoResultHit)
				slog.InfoContext(ctx, "session_compressor: memo hit, skipping summary/trim",
					"session", gwSessionID, "strategy", cached.Strategy,
					"cached_at", cached.CachedAt.Format(time.RFC3339))

				outboundBody = cached.CompressedBody
				res.OutboundBody = outboundBody
				res.CompressionStrategy = cached.Strategy
				res.SummaryMarker = cached.SummaryMarker
				res.Degraded = cached.Degraded
				res.MsgCount = cached.MsgCount
				res.TokenEst = cached.TokenEst
				if len(cached.MsgHashes) > 0 {
					res.MsgHashes = cached.MsgHashes
				}
				if len(cached.AlignmentMap) > 0 {
					var am []AlignmentInfo
					if json.Unmarshal(cached.AlignmentMap, &am) == nil {
						res.AlignmentMap = am
					}
				}
				if cached.WindowTriggered != "" {
					res.WindowTriggered = cached.WindowTriggered
				}

				res.Lossiness = classifyLossiness(res.CompressionStrategy, res.SummaryMarker)
				RecordLossiness(res.Lossiness)

				res.CompressedPrefixHash = cached.CompressedPrefixHash
				if res.CompressedPrefixHash == "" && len(outboundBody) > 0 {
					if _, report, _ := prefix.Stabilize(outboundBody, prefix.Options{TailTurns: 1}); report != nil {
						res.CompressedPrefixHash = report.PrefixHash
					}
				}

				didCompressCached := !cached.Degraded && cached.SummaryMarker != ""
				sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, didCompressCached, fromV2)
				return res
			}
			RecordMemo(MemoResultMiss)
		}

		if winResult.Degraded {
			res.Degraded = true
			before := outboundBody
			trimmed := mechanicalTrim(outboundBody, contextWindow, protocol)
			if len(trimmed) < len(outboundBody) {
				outboundBody = trimmed
				res.OutboundBody = outboundBody
				res.CompressionStrategy = "mechanical_trim"
				res.MsgCount = countMessages(outboundBody)
				res.TokenEst = estimateBodyTokens(outboundBody)
				res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
				res.AlignmentMap = buildAlignmentMap(before, outboundBody, -1)
			}
		} else {
			// ── LOSSLESS_FIRST: try LLM summary ──────────────────────────
			// docs/omni-ref3 C1: circuit-breaker the LLM-summary path. If the
			// summary model is erroring, skip the call (and its quota/latency
			// cost) and go straight to mechanical trim until the breaker resets.
			taskType := extractTaskType(ctx)
			if winResult.TokenBand == OutboundTokenBandForced {
				taskType = "document_summary"
			}
			now := time.Now()

			allow, _ := sc.breaker.allowDecide(now)
			var (
				summarised []byte
				ok         bool
			)
			if !allow {
				slog.Warn("session_compressor: summary breaker open, skipping LLM summary",
					"session", gwSessionID, "trigger", winResult.Reason)
			} else {
				// SP-03 (2026-08-19): route through the fallback wrapper so
				// the LLM summary path can honour ctx cancellation (state
				// machine cancels are propagated via the request ctx).
				summarised, ok = sc.tryLLMSummaryWithFallback(ctx, outboundBody, tenantID, protocol, taskType)
				// Record outcome: success only when it produced a usable, smaller body.
				sc.breaker.RecordResult(ok && len(summarised) > 0 && len(summarised) < len(outboundBody), time.Now())
			}
			if ok && len(summarised) > 0 && len(summarised) < len(outboundBody) {
				// LLM summary succeeded — inject summary_marker.
				before := outboundBody
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
				res.AlignmentMap = buildAlignmentMap(before, outboundBody, summaryMessageIndex(outboundBody, protocol))
			} else {
				// LLM summary failed or didn't shrink — fall back to mechanical trim.
				slog.Info("session_compressor: LLM summary failed/no-op, falling back to mechanical trim",
					"session", gwSessionID, "trigger", winResult.Reason)
				before := outboundBody
				trimmed := mechanicalTrim(outboundBody, contextWindow, protocol)
				if len(trimmed) < len(outboundBody) {
					outboundBody = trimmed
					res.OutboundBody = outboundBody
					res.CompressionStrategy = "mechanical_trim"
					res.MsgCount = countMessages(outboundBody)
					res.TokenEst = estimateBodyTokens(outboundBody)
					res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(outboundBody)))
					res.AlignmentMap = buildAlignmentMap(before, outboundBody, -1)
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

	// ── Compute stable prefix hash (docs/omni-ref3 C8/D7) ─────────────────
	// Call cache/prefix.Stabilize on the outbound body to get a hash of the
	// stable prefix (System + Tool + History, excluding volatile Tail). This
	// hash is the key for semantic cache lookups and cache-aware compression.
	// Empty when the prefix is all-tail or Stabilize fails gracefully.
	if len(outboundBody) > 0 {
		_, report, _ := prefix.Stabilize(outboundBody, prefix.Options{TailTurns: 1})
		if report != nil {
			res.CompressedPrefixHash = report.PrefixHash
		}
	}

	// ── Result memo store (docs/omni-ref3 C3) ────────────────────────────
	// Only cache results that actually cost something to produce: a summary
	// or a mechanical trim. delta_append is cheap and its input changes every
	// turn, so caching it would only churn Redis.
	if sc.deps.ResultMemo.enabled() && len(memoInputForStore) > 0 &&
		memoStorable(res.CompressionStrategy) && len(res.OutboundBody) > 0 {
		err := sc.deps.ResultMemo.Set(ctx, MemoKeyParts{
			TenantID:      tenantID,
			SessionID:     gwSessionID,
			Mode:          mode.String(),
			Protocol:      protocol,
			ContextWindow: contextWindow,
		}, memoInputForStore, &MemoValue{
			CompressedBody:       res.OutboundBody,
			Strategy:             res.CompressionStrategy,
			SummaryMarker:        res.SummaryMarker,
			WindowTriggered:      res.WindowTriggered,
			Degraded:             res.Degraded,
			MsgCount:             res.MsgCount,
			TokenEst:             res.TokenEst,
			MsgHashes:            res.MsgHashes,
			CompressedPrefixHash: res.CompressedPrefixHash,
			AlignmentMap:         marshalAlignment(res.AlignmentMap),
		})
		if err != nil {
			slog.WarnContext(ctx, "session_compressor: memo store failed",
				"session", gwSessionID, "error", err)
		}
	}

	// ── 计算并记录压缩质量评分（Phase 1 Task 1.3）──────────────────────
	if winResult.ShouldTrigger && !winResult.Degraded {
		// 计算原始消息的 token 估算（从 state 或 clientBody 估算）
		originalTokens := 0
		originalMsgCount := 0
		if state != nil && state.TokenEstimate > 0 {
			// 使用上一次的 token 估算作为基线
			originalTokens = state.TokenEstimate
			originalMsgCount = state.MsgCount
		} else {
			// 从 clientBody 估算
			originalTokens = len(clientBody) / 3 // 粗略估算：3 字节/token
			originalMsgCount = res.MsgCount
		}

		quality := ComputeCompressionQuality(
			originalTokens,
			res.TokenEst,
			originalMsgCount,
			res.MsgCount,
			res.AlignmentMap,
			res.Lossiness,
		)

		sc.logCompressionQuality(
			ctx,
			tenantID,
			gwSessionID,
			originalTokens,
			res.TokenEst,
			originalMsgCount,
			res.MsgCount,
			quality,
			res.CompressionStrategy,
		)
	}

	// ── Persist updated cache state ──────────────────────────────────────
	didCompress := winResult.ShouldTrigger && !winResult.Degraded && res.SummaryMarker != ""
	sc.updateCache(ctx, tenantID, gwSessionID, state, outboundBody, res, didCompress, fromV2)

	return res
}

// memoStorable reports whether a strategy is worth memoising. Only the
// expensive post-window strategies qualify (docs/omni-ref3 C3).
func memoStorable(strategy string) bool {
	return strategy == "mechanical_trim" || strings.HasPrefix(strategy, "sliding_window_")
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

	// SP-03 (2026-08-19): bail out early when ctx is already canceled so
	// the LLM summary path never issues a network request after the
	// state machine has signalled cancellation. The summarizer client
	// also honours ctx, but the early exit avoids creating a new
	// summarizer instance only to throw it away.
	if err := ctx.Err(); err != nil {
		slog.DebugContext(ctx, "session_compressor: ctx already canceled, skipping LLM summary",
			"tenant", tenantID, "session", "<masked>")
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

// tryLLMSummaryWithFallback (SP-03, 2026-08-19) wraps tryLLMSummary so the
// LLM summary path can honour ctx cancellation without touching the
// underlying cache get/set semantics. The wrapper checks ctx first, runs
// tryLLMSummary, then re-checks ctx before returning so a cancellation
// that arrived mid-summary is reflected back to the caller as a no-op
// (rather than silently shipping a summary that landed after the client
// gave up).
//
// tryLLMSummaryWithFallback preserves the (body, ok) signature of
// tryLLMSummary and is the only entry point the rest of the package
// should call.
func (sc *SessionCompressor) tryLLMSummaryWithFallback(ctx context.Context, body []byte, tenantID, protocol, taskType string) ([]byte, bool) {
	if err := ctx.Err(); err != nil {
		slog.DebugContext(ctx, "session_compressor: fallback skipping LLM summary (ctx canceled)",
			"tenant", tenantID)
		return nil, false
	}
	out, ok := sc.tryLLMSummary(ctx, body, tenantID, protocol, taskType)
	// Re-check ctx after the call: if cancellation arrived while the
	// LLM summary was in flight, the cached summarizer may have produced
	// a result, but the client is gone — discard it instead of letting
	// downstream code emit "still compressing" telemetry.
	if err := ctx.Err(); err != nil && ok {
		slog.DebugContext(ctx, "session_compressor: fallback discarding result due to ctx cancellation",
			"tenant", tenantID)
		return nil, false
	}
	return out, ok
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
	fromV2 bool,
) {
	// When the request was served from the V2 read path, do NOT write the
	// reconstructed state back into the V1 cache. The V2 path supplies an
	// empty placeholder SessionState (see Prepare), so persisting a derived
	// newState here would poison the V1 cache with a LastCompressedAt=now
	// entry that has no SummaryMarker / ToolsHash — and the next request
	// that falls back to V1 would read this corrupted state. V2 owns its
	// own write side (session_bodies via the DualWriter); the two caches
	// must stay independent.
	if fromV2 {
		return
	}
	if sc.deps.Cache == nil {
		return
	}
	newState := buildSessionState(prevState, outboundBody, res, didCompress, time.Now().Unix())
	hydrateSanitizeInfo(ctx, newState)
	if err := sc.deps.Cache.Set(ctx, tenantID, gwSessionID, newState, outboundBody); err != nil {
		slog.Warn("session_compressor: cache set failed", "session", gwSessionID, "error", err)
	}
}

// hydrateSanitizeInfo copies the request-scoped sanitize bridge info (set by
// the HTTP sanitize middleware, SC-1) into the cache state's v8 L3 fields.
// Requests without new placeholders keep whatever ref the previous state
// carried — buildSessionState already copies prevState forward.
func hydrateSanitizeInfo(ctx context.Context, state *SessionState) {
	if state == nil {
		return
	}
	info, ok := SanitizeInfoFromContext(ctx)
	if !ok {
		return
	}
	if info.MapRef != "" {
		state.SanitizeMapRef = info.MapRef
	}
	if info.Stats.PlaceholderCount > 0 || info.Stats.SanitizedAt > 0 {
		state.SanitizeStats = info.Stats
	}
}

// CommitFinal overwrites the compatible Prepare-time cache entry with the
// exact client-protocol body entering provider dispatch after NeverWorse,
// tools restoration, prefix stabilization, and cache-parameter injection.
// It also refreshes res in place so handler telemetry describes that same body.
func (sc *SessionCompressor) CommitFinal(
	ctx context.Context,
	tenantID, gwSessionID string,
	finalBody []byte,
	res *PrepareResult,
) error {
	if sc == nil || sc.deps.Cache == nil || gwSessionID == "" || len(finalBody) == 0 || res == nil {
		return nil
	}
	if res.skipV1Cache {
		slog.DebugContext(ctx, "session_compressor: skipping V1 final commit for V2 source",
			"session", gwSessionID, "tenant", tenantID)
		return nil
	}
	if err := ValidateSessionID(gwSessionID); err != nil {
		return fmt.Errorf("commit final session cache failed: %w (session_id=%s)", err, gwSessionID)
	}
	prevState, _, err := sc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
	if err != nil {
		return fmt.Errorf("load session cache before final commit failed: %w (session_id=%s)", err, gwSessionID)
	}

	res.OutboundBody = append(res.OutboundBody[:0], finalBody...)
	res.MsgCount = countMessages(finalBody)
	res.TokenEst = estimateBodyTokens(finalBody)
	res.MsgHashes = marshalHashes(computeHashes(mustExtractMessages(finalBody)))
	res.CompressedPrefixHash = ""
	if _, report, err := prefix.Stabilize(finalBody, prefix.Options{TailTurns: 1}); err == nil && report != nil {
		res.CompressedPrefixHash = report.PrefixHash
	}
	state := buildSessionState(prevState, finalBody, res, res.SummaryMarker != "", time.Now().Unix())
	hydrateSanitizeInfo(ctx, state)
	if err := sc.deps.Cache.Set(ctx, tenantID, gwSessionID, state, finalBody); err != nil {
		return fmt.Errorf("commit final session cache failed: %w (session_id=%s)", err, gwSessionID)
	}
	return nil
}

func buildSessionState(prevState *SessionState, outboundBody []byte, res *PrepareResult, didCompress bool, now int64) *SessionState {
	state := &SessionState{}
	if prevState != nil {
		*state = *prevState
		state.AlignmentMap = append([]AlignmentInfo(nil), prevState.AlignmentMap...)
	}
	state.SchemaVersion = schemaVersion
	state.LastOutboundHash = sha256Hex(outboundBody)
	state.MsgCount = res.MsgCount
	state.TokenEstimate = res.TokenEst
	state.RawMsgCount = countMessages(outboundBody)
	state.RawTokenEstimate = estimateBodyTokens(outboundBody)
	state.CompressedMsgs = res.MsgCount
	state.CompressedTokens = res.TokenEst
	if res.CompressedPrefixHash != "" {
		state.CompressedPrefixHash = res.CompressedPrefixHash
	}
	state.AuditedAt = now
	if didCompress {
		state.LastCompressedAt = now
		state.RecentlyCompressedAt = now
	}
	if res.SummaryMarker != "" {
		state.SummaryMarker = res.SummaryMarker
	} else if state.SummaryMarker != "" && !bytesContainSummaryMarker(outboundBody, state.SummaryMarker) {
		// A rewrite that removed the old gateway summary must not leave its
		// marker attached to the new body. Preserve it only when the exact
		// marker is still present in the body being committed.
		state.SummaryMarker = ""
	}
	if len(res.AlignmentMap) > 0 {
		state.AlignmentMap = append(state.AlignmentMap[:0], res.AlignmentMap...)
	}
	return state
}

func bytesContainSummaryMarker(body []byte, marker string) bool {
	return marker != "" && strings.Contains(string(body), marker)
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

// injectSummaryMarker adds the smm_v1 marker to the gateway-generated summary
// boundary. It never marks an arbitrary user or assistant message: the marker
// is only emitted when the body contains a known summary prefix from one of
// the protocol-specific rebuilders.
func injectSummaryMarker(summarisedBody []byte, protocol string) (marker string, newBody []byte) {
	if protocol == "anthropic-messages" {
		return injectAnthropicSummaryMarker(summarisedBody)
	}
	return injectOpenAISummaryMarker(summarisedBody)
}

func injectOpenAISummaryMarker(body []byte) (string, []byte) {
	msgs, err := extractMessages(body)
	if err != nil || len(msgs) == 0 {
		return "", nil
	}
	for i, raw := range msgs {
		var msg map[string]json.RawMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		var role string
		if json.Unmarshal(msg["role"], &role) != nil || role != "user" {
			continue
		}
		var content string
		if json.Unmarshal(msg["content"], &content) == nil {
			marker, baseContent, alreadyMarked := summaryMarkerContent(content)
			if !isGatewaySummaryContent(baseContent) {
				continue
			}
			if alreadyMarked {
				return marker, body
			}
			marker = BuildSummaryMarker(content)
			if marker == "" {
				return "", nil
			}
			encoded, err := json.Marshal(marker + "\n" + content)
			if err != nil {
				return "", nil
			}
			msg["content"] = encoded
			updated, err := json.Marshal(msg)
			if err != nil {
				return "", nil
			}
			msgs[i] = updated
			newMessages, err := json.Marshal(msgs)
			if err != nil {
				return "", nil
			}
			updatedBody, ok := spliceBodyMessages(body, newMessages)
			if !ok {
				return "", nil
			}
			return marker, updatedBody
		}
	}
	return "", nil
}

func injectAnthropicSummaryMarker(body []byte) (string, []byte) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return "", nil
	}
	system, ok := top["system"]
	if !ok || len(system) == 0 || string(system) == "null" {
		return "", nil
	}
	marker, updatedSystem, ok := markerizeAnthropicSystem(system)
	if !ok {
		return "", nil
	}
	top["system"] = updatedSystem
	updatedBody, err := json.Marshal(top)
	if err != nil {
		return "", nil
	}
	return marker, updatedBody
}

func markerizeAnthropicSystem(system json.RawMessage) (string, json.RawMessage, bool) {
	var content string
	if json.Unmarshal(system, &content) == nil {
		marker, baseContent, alreadyMarked := summaryMarkerContent(content)
		if !isAnthropicSummaryContent(baseContent) {
			return "", nil, false
		}
		if alreadyMarked {
			return marker, system, true
		}
		marker = BuildSummaryMarker(content)
		if marker == "" {
			return "", nil, false
		}
		updated, err := json.Marshal(marker + "\n" + content)
		return marker, updated, err == nil
	}

	var blocks []json.RawMessage
	if json.Unmarshal(system, &blocks) != nil {
		return "", nil, false
	}
	for i, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &block) != nil || block.Type != "text" || !isAnthropicSummaryContent(block.Text) {
			continue
		}
		marker, baseContent, alreadyMarked := summaryMarkerContent(block.Text)
		if !isAnthropicSummaryContent(baseContent) {
			continue
		}
		if alreadyMarked {
			return marker, system, true
		}
		marker = BuildSummaryMarker(block.Text)
		if marker == "" {
			return "", nil, false
		}
		block.Text = marker + "\n" + block.Text
		updatedBlock, err := json.Marshal(block)
		if err != nil {
			return "", nil, false
		}
		blocks[i] = updatedBlock
		updatedSystem, err := json.Marshal(blocks)
		return marker, updatedSystem, err == nil
	}
	return "", nil, false
}

func summaryMarkerContent(content string) (marker, base string, marked bool) {
	if !strings.HasPrefix(content, CompactionMarkerPrefix) {
		return "", content, false
	}
	end := strings.IndexByte(content, ']')
	if end < len(CompactionMarkerPrefix) {
		return "", content, false
	}
	marker = content[:end+1]
	return marker, strings.TrimPrefix(strings.TrimPrefix(content[end+1:], "\n"), "\r\n"), true
}

func isGatewaySummaryContent(content string) bool {
	return strings.HasPrefix(content, CompressionSummaryPrefix) || strings.HasPrefix(content, smartWindowSummaryPrefix)
}

func isAnthropicSummaryContent(content string) bool {
	literalPrefix := AnthropicSystemSummaryPrefix
	decodedPrefix := strings.ReplaceAll(literalPrefix, `\n`, "\n")
	return strings.HasPrefix(content, literalPrefix) || strings.HasPrefix(content, decodedPrefix) ||
		strings.HasPrefix(content, literalPrefix[2:]) || strings.HasPrefix(content, decodedPrefix[2:])
}

func summaryMessageIndex(body []byte, protocol string) int {
	if protocol == "anthropic-messages" {
		return -1
	}
	msgs, err := extractMessages(body)
	if err != nil {
		return -1
	}
	for i, raw := range msgs {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &msg) == nil && msg.Role == "user" {
			_, baseContent, _ := summaryMarkerContent(msg.Content)
			if isGatewaySummaryContent(baseContent) {
				return i
			}
		}
	}
	return -1
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
// (public.session_bodies) instead of V1 (request_logs LCS delta).
//
// Returns true when:
//  1. V2 components (CacheV2 + Builder) are wired, AND
//  2. The platform flag "sessions_v2_compression_read" is true.
//
// The flag defaults to TRUE (docs/omni-ref3 A1 — decision: compress+summary
// cut over together, default-on, no canary). It is a kill-switch: setting it
// to false in settings_kv hot-reloads V1 reads back immediately, no redeploy.
// Fail-open: if V2 read itself errors, tryLoadV2State returns ok=false and the
// caller falls back to V1 for that request regardless of this flag.
//
// tenantID is accepted for forward-compatibility with a future tenant-scoped
// reader; unused for now to avoid an "unused parameter" lint warning.
func (sc *SessionCompressor) shouldUseV2(tenantID string) bool {
	if sc == nil {
		return false
	}

	// V2 components must both be wired at startup.
	if sc.deps.CacheV2 == nil || sc.deps.Builder == nil {
		return false
	}

	_ = tenantID
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
	// Ask the V2 reader whether prior state exists. A "no" (new session)
	// is not an error: we return ok=true with an empty body so the caller
	// proceeds as a fresh session rather than falling back to V1.
	has, err := sc.deps.CacheV2.HasState(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 cache get failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}
	if !has {
		// New session, no previous state
		return nil, true
	}

	// Rebuild the last outbound body (JSON) via the V2 builder.
	outboundBody, err := sc.deps.Builder.BuildLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 build from outbound failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	return outboundBody, true
}

func (sc *SessionCompressor) loadV2CompressionState(ctx context.Context, tenantID, sessionID string) *SessionState {
	reader, ok := sc.deps.CacheV2.(V2CompressionMetadataReader)
	if !ok {
		return nil
	}
	meta, err := reader.CompressionMetadata(ctx, tenantID, sessionID)
	if err != nil || meta == nil {
		return nil
	}
	state := &SessionState{SchemaVersion: schemaVersion}
	state.SummaryMarker, _ = meta["summary_marker"].(string)
	state.CompressedPrefixHash, _ = meta["compressed_prefix_hash"].(string)
	state.ToolsHash, _ = meta["tools_hash"].(string)
	state.CompressionMode, _ = meta["strategy"].(string)
	state.TokenEstimate = intMeta(meta["token_estimate"])
	state.MsgCount = intMeta(meta["msg_count"])
	state.LastCompressedAt = unixMeta(meta["last_compressed_at"])
	state.RecentlyCompressedAt = unixMeta(meta["recently_compressed_at"])
	return state
}

func intMeta(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func unixMeta(v any) int64 {
	switch t := v.(type) {
	case time.Time:
		return t.Unix()
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return parsed.Unix()
		}
	case int64:
		return t
	case float64:
		return int64(t)
	}
	return 0
}

// 在每次压缩完成后调用，记录详细的质量指标，用于：
//  1. 监控压缩效果（token 节省、语义保真度）
//  2. 对比不同压缩策略的效果
//  3. 发现压缩质量问题（如压缩后反而变大）
func (sc *SessionCompressor) logCompressionQuality(
	ctx context.Context,
	tenantID, sessionID string,
	originalTokens, compressedTokens int,
	originalMsgCount, compressedMsgCount int,
	quality CompressionQualityScore,
	strategy string,
) {
	if sc == nil {
		return
	}

	slog.InfoContext(ctx, "compression_quality",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"strategy", strategy,
		"lossiness", quality.LossinessClass,
		// 原始 vs 压缩
		"original_msg_count", originalMsgCount,
		"original_tokens", originalTokens,
		"compressed_msg_count", compressedMsgCount,
		"compressed_tokens", compressedTokens,
		// 质量评分
		"token_savings_pct", formatPercent(quality.TokenSavingsPercent),
		"msg_retention_rate", formatFloat(quality.MessageRetentionRate),
		"semantic_fidelity", formatFloat(quality.SemanticFidelity),
		"info_density", formatFloat(quality.InformationDensity),
		"overall_score", formatFloat(quality.OverallScore),
	)
}

// formatPercent 格式化百分比为字符串（保留1位小数）
func formatPercent(v float64) string {
	return fmt.Sprintf("%.1f%%", v)
}

// formatFloat 格式化浮点数为字符串（保留2位小数）
func formatFloat(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
