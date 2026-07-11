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
	"time"
)

// RecoveryDeps holds the external dependencies for the recovery coordinator.
type RecoveryDeps struct {
	// Cache is the session state cache. May be nil (recovery still works,
	// just without incremental compression on the next request).
	Cache *SessionCache

	// Summarizer is the LLM summarization callback. When nil, the coordinator
	// falls back to mechanical trim only.
	Summarizer SummaryFunc

	// Estimator provides token estimation (usually the package-level estimator).
	Estimator *Estimator

	// MaxRetries is the maximum compression retries per request.
	// Default 2: first attempt = conservative smart window, second = aggressive.
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
	if deps.MaxRetries <= 0 {
		deps.MaxRetries = 2
	}
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
	if attempt == 0 && rc.deps.Cache != nil && gwSessionID != "" {
		if state, _, _ := rc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID); state != nil && state.HasCutMarker {
			marker := state.ToCutMarker("")
			if marker != nil && !marker.IsExpired(redisKeyTTL) {
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
	marker := NewCutMarker(
		plan,
		messages,
		strategy,
		BuildSummaryMarker(summaryText),
		summaryText,
		len(body),
		len(rebuilt),
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

func startsWithPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimPrefix(s, prefix string) string {
	if startsWithPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
