// Package compressor - recovery_coordinator.go
//
// Session-aware 4xx recovery coordinator. Replaces the ad-hoc
// handleContextLengthRecovery in routing/context_summarize.go with a
// cache-aware flow that:
//
//  1. Checks SessionCache for a prior compression (incremental path)
//  2. Runs smart window analysis to find the optimal cut point
//  3. Executes compression (LLM summary → mechanical trim fallback)
//  4. Persists the result (CutMarker + summary) to SessionCache
//  5. Returns the rebuilt body for retry
//
// The coordinator is protocol-agnostic and works for both OpenAI Chat
// Completions and Anthropic Messages.

package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// RecoveryDeps holds the external dependencies for the recovery coordinator.
type RecoveryDeps struct {
	// Cache is the legacy session state cache. May be nil; V2 metadata can
	// provide the cold-start recovery path when the legacy cache is absent.
	Cache *SessionCache

	// V2Meta and V2Builder are optional V2 cold-start recovery dependencies.
	// They carry only body-free metadata plus the latest outbound snapshot.
	V2Meta    V2CompressionMetadataReader
	V2Builder V2OutboundBuilder

	// Summarizer is the LLM summarization callback. When nil, the coordinator
	// falls back to mechanical trim only.
	Summarizer SummaryFunc

	// Estimator provides token estimation (usually the package-level estimator).
	Estimator *Estimator

	// Retry ownership remains with the executor's upstream-attempt budget. A
	// coordinator invocation applies one ordered recovery plan so it cannot
	// amplify retries independently of candidate failover.
	//
	// Deprecated: retained only for source compatibility; it is intentionally
	// ignored. Configure the executor attempt budget instead.
	MaxRetries int
}

// SummaryFunc is the callback signature for LLM-based summarization.
// The implementation lives in routing/context_summarize.go (tryLLMContextCompaction)
// and is injected here to avoid a routing → compressor import cycle.
//
// Returns (summaryText, ok). When ok=false, the coordinator falls back to
// mechanical trim.
type SummaryFunc func(ctx context.Context, body []byte, protocol string) (summary string, ok bool)

// RecoveryResult is the output of RecoveryCoordinator.Recover.
type RecoveryResult struct {
	// NewBody is the rebuilt body to send on retry. Nil when recovery failed.
	NewBody []byte

	// Strategy is which strategy succeeded: "smart_window_llm",
	// "smart_window_mechanical", "incremental_cache", or "" (failed).
	Strategy string

	// CutMarker records the compression boundary (for caching).
	CutMarker *CutMarker

	// Reason is a human-readable explanation for telemetry/logging.
	Reason string

	// EstTokensBefore / EstTokensAfter for telemetry.
	EstTokensBefore int
	EstTokensAfter  int

	// ShouldRetry is true when the rebuilt body should be re-sent.
	ShouldRetry bool
}

// RecoveryCoordinator orchestrates context_length_exceeded recovery with
// session cache integration.
type RecoveryCoordinator struct {
	deps RecoveryDeps
}

// NewRecoveryCoordinator builds a RecoveryCoordinator.
func NewRecoveryCoordinator(deps RecoveryDeps) *RecoveryCoordinator {
	return &RecoveryCoordinator{deps: deps}
}

// Recover attempts to compress an oversized conversation after a
// context_length_exceeded 4xx. It tries (in order):
//
//  1. Incremental: if SessionCache has a valid CutMarker for this session,
//     apply it to skip already-compressed messages.
//  2. Smart window + LLM summary: analyze conversation, find optimal cut,
//     generate LLM summary of the dropped portion.
//  3. Smart window + mechanical trim: if LLM summary fails, use a simpler
//     sliding window trim without summarization.
//
// On success, the result (including CutMarker) is persisted to SessionCache
// so the next request for the same session can use incremental compression.
func (rc *RecoveryCoordinator) Recover(
	ctx context.Context,
	body []byte,
	protocol string,
	contextWindow int,
	tenantID, gwSessionID string,
	attempt int,
) RecoveryResult {
	res := RecoveryResult{
		EstTokensBefore: estimateBodyTokens(body),
	}

	if rc == nil {
		return res
	}

	// ── Phase 0: Try incremental cache path ──────────────────────────────
	// On attempt 0, if the session cache already has a CutMarker from a
	// prior request, we can skip the already-compressed portion.
	if attempt == 0 && gwSessionID != "" {
		if recovered, ok := rc.recoverFromV2Metadata(ctx, body, protocol, tenantID, gwSessionID); ok {
			return recovered
		}
	}
	if attempt == 0 && rc.deps.Cache != nil && gwSessionID != "" {
		if state, _, _ := rc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID); state != nil && state.HasCutMarker {
			marker := state.ToCutMarker("")
			if marker != nil && !marker.IsExpired(sessionCacheRedisTTL()) {
				// The summary text is in L1 only; try to get it from cache.
				_, l1Body, _ := rc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
				if l1Body != nil {
					// Extract summary from the cached L1 body (the first
					// non-system user message with smartWindowSummaryPrefix).
					marker.SummaryText = extractSummaryFromCachedBody(l1Body)
				}
				if marker.SummaryText != "" {
					if rebuilt, ok := IncrementalBuild(body, *marker, protocol); ok {
						newTokens := estimateBodyTokens(rebuilt)
						if newTokens < res.EstTokensBefore {
							res.NewBody = rebuilt
							res.Strategy = "incremental_cache"
							res.CutMarker = marker
							res.Reason = "reused cached cut marker from prior compression"
							res.EstTokensAfter = newTokens
							res.ShouldRetry = true
							slog.Info("recovery: incremental cache hit",
								"session", gwSessionID,
								"cut_index", marker.CutIndex,
								"tokens_before", res.EstTokensBefore,
								"tokens_after", newTokens,
							)
							return res
						}
					}
				}
			}
		}
	}

	// ── Phase 1: Smart window analysis ───────────────────────────────────
	messages, err := extractMessages(body)
	if err != nil || len(messages) == 0 {
		res.Reason = "failed to parse messages"
		return res
	}

	// Adjust target utilization based on attempt: first attempt conservative,
	// second more aggressive.
	utilization := 0.65
	if attempt >= 1 {
		utilization = 0.50
	}

	plan := FindOptimalCutPoint(messages, contextWindow, utilization)
	if plan.CutIndex < 0 {
		res.Reason = "no compression needed (all messages fit)"
		return res
	}

	// ── Phase 2: Execute compression ─────────────────────────────────────
	// Phase 2a: Try LLM summary (first choice for minimal information loss).
	summaryText := ""
	strategy := ""
	if rc.deps.Summarizer != nil {
		s, ok := rc.deps.Summarizer(ctx, body, protocol)
		if ok && s != "" {
			summaryText = s
			strategy = "smart_window_llm"
		}
	}

	// Phase 2b: Fall back to mechanical extract (no LLM call).
	if summaryText == "" {
		summaryText = extractSummaryText(body, plan)
		strategy = "smart_window_mechanical"
	}

	// Phase 2c: Apply the smart compression.
	rebuilt, err := SmartCompress(body, plan, protocol, summaryText)
	if err != nil || len(rebuilt) >= len(body) {
		res.Reason = fmt.Sprintf("smart compress failed: %v", err)
		return res
	}

	// ── Phase 3: Build CutMarker and persist to cache ────────────────────
	// IMPORTANT: only attach an smm_v1 marker when an LLM summary was actually
	// produced. Mechanical fallback text is deterministic-but-unstable (its
	// prefix changes with every user input and message count), so hashing it
	// would generate a different marker on every compression pass even when
	// nothing semantically changed. The main session_compressor path applies
	// the same rule (SummaryMarker is only set on the LLM-success branch).
	var markerSummaryText string
	if strategy == "smart_window_llm" {
		markerSummaryText = summaryText
	}
	// PSOR is expressed in the original, pre-sanitize message coordinates.
	// The compression cut covers leading system messages plus the dropped
	// non-system prefix; system messages are included because they are part
	// of the exact source range replaced by the rebuilt representation.
	preSanitizeRange := [2]int{plan.SystemCount, plan.SystemCount + plan.CutIndex}
	if plan.CutIndex <= 0 || plan.SystemCount < 0 || preSanitizeRange[1] > len(messages) {
		preSanitizeRange = [2]int{}
	}
	marker := NewCutMarkerWithPreSanitize(
		plan,
		len(messages),
		strategy,
		BuildSummaryMarker(markerSummaryText),
		markerSummaryText,
		len(body),
		len(rebuilt),
		preSanitizeRange,
	)

	if rc.deps.Cache != nil && gwSessionID != "" {
		state, _, _ := rc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
		if state == nil {
			state = &SessionState{SchemaVersion: schemaVersion}
		}
		state.SetCutMarker(marker)
		state.LastCompressedAt = time.Now().Unix()
		state.RecentlyCompressedAt = time.Now().Unix()
		_ = rc.deps.Cache.Set(ctx, tenantID, gwSessionID, state, rebuilt)
	}

	res.NewBody = rebuilt
	res.Strategy = strategy
	res.CutMarker = &marker
	res.EstTokensAfter = estimateBodyTokens(rebuilt)
	res.ShouldRetry = true
	res.Reason = fmt.Sprintf("smart_window cut at idx=%d (summarise=%d, retain=%d, %s)",
		plan.CutIndex, plan.SummariseCount, plan.RetainCount, plan.Reason)

	slog.Info("recovery: smart compression applied",
		"session", gwSessionID,
		"strategy", strategy,
		"cut_index", plan.CutIndex,
		"summarise_count", plan.SummariseCount,
		"retain_count", plan.RetainCount,
		"first_user_kept", plan.FirstUserKept,
		"bytes_before", len(body),
		"bytes_after", len(rebuilt),
		"tokens_before", res.EstTokensBefore,
		"tokens_after", res.EstTokensAfter,
	)

	return res
}

func (rc *RecoveryCoordinator) recoverFromV2Metadata(ctx context.Context, body []byte, protocol, tenantID, sessionID string) (RecoveryResult, bool) {
	if rc == nil || rc.deps.V2Meta == nil || rc.deps.V2Builder == nil {
		return RecoveryResult{}, false
	}
	meta, err := rc.deps.V2Meta.CompressionMetadata(ctx, tenantID, sessionID)
	if err != nil || len(meta) == 0 {
		return RecoveryResult{}, false
	}
	cut, ok := meta["cut_marker"].(map[string]interface{})
	if !ok || len(cut) == 0 {
		return RecoveryResult{}, false
	}
	marker, ok := cutMarkerFromMetadata(cut)
	if !ok || marker.IsExpired(sessionCacheRedisTTL()) || !validatePersistedProvenance(meta, marker) {
		return RecoveryResult{}, false
	}
	cachedBody, err := rc.deps.V2Builder.BuildLatestOutbound(ctx, tenantID, sessionID)
	if err != nil || len(cachedBody) == 0 {
		return RecoveryResult{}, false
	}
	if marker.SummaryMarker != "" {
		marker.SummaryText = extractSummaryFromCachedBodyForMarker(cachedBody, marker.SummaryMarker)
	}
	var rebuilt []byte
	if marker.SummaryText != "" {
		rebuilt, ok = IncrementalBuild(body, marker, protocol)
	} else {
		rebuilt, ok = IncrementalBuildTail(body, marker, protocol)
	}
	if !ok || len(rebuilt) >= len(body) {
		return RecoveryResult{}, false
	}
	return RecoveryResult{
		NewBody: rebuilt, Strategy: "incremental_v2_metadata", CutMarker: &marker,
		Reason: "reused persisted V2 cut marker", EstTokensBefore: estimateBodyTokens(body),
		EstTokensAfter: estimateBodyTokens(rebuilt), ShouldRetry: true,
	}, true
}

func cutMarkerFromMetadata(meta map[string]interface{}) (CutMarker, bool) {
	toInt := func(value interface{}) (int, bool) {
		switch n := value.(type) {
		case float64:
			return int(n), n >= 0 && n == float64(int(n))
		case int:
			return n, n >= 0
		default:
			return 0, false
		}
	}
	created, ok1 := toInt(meta["created_at"])
	source, ok2 := toInt(meta["source_msg_count"])
	system, ok3 := toInt(meta["system_msg_count"])
	cut, ok4 := toInt(meta["cut_index"])
	strategy, ok5 := meta["strategy"].(string)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || created <= 0 || source <= 0 || cut <= 0 || system+cut > source {
		return CutMarker{}, false
	}
	marker := CutMarker{Version: cutMarkerSchemaVersion, CreatedAt: int64(created), SourceMsgCount: source, SystemMsgCount: system, CutIndex: cut, Strategy: strategy}
	if summary, ok := meta["summary_marker"].(string); ok {
		marker.SummaryMarker = summary
	}
	psorValue := meta["pre_sanitize_offset_range"]
	if psorValue == nil {
		psorValue = meta["psor"]
	}
	switch pair := psorValue.(type) {
	case []interface{}:
		if len(pair) == 2 {
			start, a := toInt(pair[0])
			end, b := toInt(pair[1])
			if a && b && start <= end && end <= source {
				marker.PreSanitizeOffsetRange = [2]int{start, end}
			}
		}
	case []int:
		if len(pair) == 2 && pair[0] >= 0 && pair[0] <= pair[1] && pair[1] <= source {
			marker.PreSanitizeOffsetRange = [2]int{pair[0], pair[1]}
		}
	}
	return marker, true
}

func validatePersistedProvenance(meta map[string]interface{}, marker CutMarker) bool {
	if psor := meta["pre_sanitize_offset_range"]; psor != nil {
		pair, ok := metadataIntPair(psor)
		if !ok || pair[0] != marker.SystemMsgCount || pair[1] != marker.GlobalCutIndex() {
			return false
		}
	}
	if refs, ok := meta["sanitize_message_refs"].([]interface{}); ok {
		if len(refs) > marker.SourceMsgCount {
			return false
		}
		for i, raw := range refs {
			ref, ok := raw.(map[string]interface{})
			if !ok {
				return false
			}
			rawIndex, ok1 := metadataNonNegativeInt(ref["raw_index"])
			sanitizedIndex, ok2 := metadataNonNegativeInt(ref["sanitized_index"])
			if !ok1 || !ok2 || rawIndex != i || sanitizedIndex != rawIndex {
				return false
			}
		}
	}
	if alignment, ok := meta["alignment_map"].([]interface{}); ok {
		if len(alignment) > marker.SourceMsgCount {
			return false
		}
		for i, raw := range alignment {
			record, ok := raw.(map[string]interface{})
			if !ok {
				return false
			}
			index, ok := metadataNonNegativeInt(record["original_index"])
			if !ok || index != i {
				return false
			}
		}
	}
	return true
}

func metadataIntPair(value interface{}) ([2]int, bool) {
	var out [2]int
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return out, false
	}
	start, ok1 := metadataNonNegativeInt(items[0])
	end, ok2 := metadataNonNegativeInt(items[1])
	if !ok1 || !ok2 || start > end {
		return out, false
	}
	return [2]int{start, end}, true
}

func metadataNonNegativeInt(value interface{}) (int, bool) {
	n, ok := value.(float64)
	return int(n), ok && n >= 0 && n <= 1<<53 && n == float64(int(n))
}

// extractSummaryText generates a brief summary from the messages that will
// be dropped by the cut plan. This is the mechanical fallback (no LLM call)
// — it extracts key phrases, roles, and first/last messages.
func extractSummaryText(body []byte, plan CutPlan) string {
	messages, _ := extractMessages(body)
	if len(messages) == 0 {
		return ""
	}

	nonSystem := messages[plan.SystemCount:]
	if plan.CutIndex >= len(nonSystem) || plan.CutIndex <= 0 {
		return ""
	}

	dropped := nonSystem[:plan.CutIndex]
	var b []byte
	b = append(b, []byte("Prior conversation (")...)
	b = append(b, []byte(fmt.Sprintf("%d messages, summarized):\n\n", len(dropped)))...)

	// Include the first user message (task intent).
	for _, m := range dropped {
		var probe struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m, &probe) != nil {
			continue
		}
		text := rawJSONTextContent(probe.Content)
		if probe.Role == "user" && text != "" {
			// Truncate to first 500 chars.
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			b = append(b, []byte("[Initial request] "+text+"\n\n")...)
			break
		}
	}

	// Count messages by role for a density summary.
	roleCount := map[string]int{}
	for _, m := range dropped {
		var probe struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(m, &probe) == nil {
			roleCount[probe.Role]++
		}
	}
	b = append(b, []byte(fmt.Sprintf("[Message distribution] user=%d, assistant=%d, tool=%d\n",
		roleCount["user"], roleCount["assistant"], roleCount["tool"]))...)

	// Include the last dropped message (most recent context before the cut).
	if len(dropped) > 0 {
		last := dropped[len(dropped)-1]
		var probe struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(last, &probe) == nil {
			text := rawJSONTextContent(probe.Content)
			if len(text) > 300 {
				text = text[:300] + "..."
			}
			if text != "" {
				b = append(b, []byte(fmt.Sprintf("[Last before cut - %s] %s\n", probe.Role, text))...)
			}
		}
	}

	return string(b)
}

// extractSummaryFromCachedBody extracts the summary text from a cached L1
// body. The summary is the first non-system user message whose content
// starts with smartWindowSummaryPrefix.
func extractSummaryFromCachedBody(body []byte) string {
	messages, err := extractMessages(body)
	if err != nil {
		return ""
	}
	for _, m := range messages {
		var probe struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(m, &probe) != nil {
			// Try array content format
			var probe2 struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			}
			if json.Unmarshal(m, &probe2) != nil {
				continue
			}
			for _, part := range probe2.Content {
				var p struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(part, &p) == nil && p.Type == "text" {
					if startsWithPrefix(p.Text, smartWindowSummaryPrefix) {
						return trimPrefix(p.Text, smartWindowSummaryPrefix)
					}
				}
			}
			continue
		}
		if startsWithPrefix(probe.Content, smartWindowSummaryPrefix) {
			return trimPrefix(probe.Content, smartWindowSummaryPrefix)
		}
	}
	return ""
}

func extractSummaryFromCachedBodyForMarker(body []byte, expectedMarker string) string {
	messages, err := extractMessages(body)
	if err != nil {
		return ""
	}
	for _, raw := range messages {
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		var content string
		if json.Unmarshal(msg.Content, &content) != nil {
			continue
		}
		if expectedMarker != "" && strings.HasPrefix(content, CompactionMarkerPrefix) {
			lineEnd := strings.IndexByte(content, '\n')
			if lineEnd < 0 || content[:lineEnd] != expectedMarker {
				continue
			}
		}
		if summary, ok := cachedSummaryText(content); ok {
			return summary
		}
	}
	return ""
}

func cachedSummaryText(content string) (string, bool) {
	if startsWithPrefix(content, smartWindowSummaryPrefix) {
		return trimPrefix(content, smartWindowSummaryPrefix), true
	}
	if !startsWithPrefix(content, CompactionMarkerPrefix) {
		return "", false
	}
	lineEnd := strings.IndexByte(content, '\n')
	if lineEnd < 0 {
		return "", false
	}
	withoutMarker := content[lineEnd+1:]
	if startsWithPrefix(withoutMarker, smartWindowSummaryPrefix) {
		return trimPrefix(withoutMarker, smartWindowSummaryPrefix), true
	}
	if startsWithPrefix(withoutMarker, CompressionSummaryPrefix) {
		return trimPrefix(withoutMarker, CompressionSummaryPrefix), true
	}
	return "", false
}

func startsWithPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimPrefix(s, prefix string) string {
	if startsWithPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
