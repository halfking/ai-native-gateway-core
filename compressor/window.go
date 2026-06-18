// Package compressor - window.go (v3 T26)
//
// ShouldTriggerWindow decides whether the current session's outbound body
// should be proactively compressed (summarised) before forwarding to the
// upstream LLM.
//
// Three independent triggers (OR logic):
//
//  1. TOKEN trigger  — outbound body exceeds contextWindow × 0.85 threshold.
//     Same formula as v7 mode=1 (auto_threshold) but applied at the
//     session level, BEFORE the request is sent (proactive vs reactive).
//
//  2. COUNT trigger  — outbound message count exceeds MaxMsgCount (default 50).
//     Prevents unbounded growth in long agentic sessions where each tool
//     round adds 2-4 messages.
//
//  3. IDLE trigger   — session has been idle ≥ IdleSeconds AND has accumulated
//     ≥ MinIdleMsgCount messages. Compacts the history while the user is
//     away so the next request starts with a clean context.
//
// Mutual exclusion with v7 on_4xx path:
//
//	If a proactive summary was written in the last RecentCompressedGuardSecs
//	seconds, this function sets Degraded=true and the caller should fall back
//	to mechanical trim only (not re-invoke the LLM summariser). This prevents
//	double-compression in the same 60-second window.
//
// Streaming guard (v7 §6 rule R3):
//
//	If the response stream has already started sending chunks, proactive
//	compression is meaningless (the body has already been sent). SkipStream
//	is set in this case.

package compressor

import (
	"os"
	"strconv"
	"time"
)

const (
	// DefaultMaxMsgCount is the default message-count threshold for the
	// COUNT trigger. Configurable via LLM_GATEWAY_WINDOW_MAX_MSG_COUNT.
	DefaultMaxMsgCount = 50

	// DefaultIdleSeconds is the minimum idle time (seconds) for the IDLE
	// trigger. Configurable via LLM_GATEWAY_WINDOW_IDLE_SECONDS.
	DefaultIdleSeconds = 300 // 5 minutes

	// DefaultMinIdleMsgCount is the minimum message count required for the
	// IDLE trigger to fire. Prevents compression of very short idle sessions.
	DefaultMinIdleMsgCount = 10

	// RecentCompressedGuardSecs is the mutual-exclusion window: if a proactive
	// summary was written within this many seconds, the caller degrades to
	// mechanical trim only (avoids double LLM call in rapid-fire sequences).
	RecentCompressedGuardSecs = 60

	// DefaultWindowFraction is the fraction of contextWindow used as the
	// token-count threshold for the TOKEN trigger. Matches v7 §2 (0.85).
	DefaultWindowFraction = 0.85
)

// WindowTriggerResult is the output of ShouldTriggerWindow.
type WindowTriggerResult struct {
	// ShouldTrigger is true when at least one trigger condition is met
	// AND neither SkipStream nor Degraded applies.
	ShouldTrigger bool

	// Reason is the primary trigger that fired.
	// One of: "sliding_window_token", "sliding_window_count", "sliding_window_idle", "".
	Reason string

	// Degraded is true when ShouldTrigger would have fired but a proactive
	// summary was already written within RecentCompressedGuardSecs seconds.
	// The caller should use mechanical trim as a cheaper fallback.
	Degraded bool

	// SkipStream is true when the response stream has already sent its first
	// chunk. Compression is pointless at that point; skip entirely.
	SkipStream bool

	// TokensEst is the estimated token count of the outbound body.
	TokensEst int

	// Threshold is the token threshold that was used (0 when contextWindow unknown).
	Threshold int
}

// ShouldTriggerWindow evaluates the three window triggers against the
// current outbound body and session state.
//
//   - outboundBody is the body to be forwarded (post-delta-append).
//   - state is the current SessionState (nil = new session → never trigger).
//   - contextWindow is the target model's context window in tokens
//     (0 = unknown → TOKEN trigger is skipped).
//   - streamStarted is true when the response stream has already emitted
//     at least one chunk (v7 §6 rule R3).
//   - now is the current time (passed explicitly for testability).
func ShouldTriggerWindow(
	outboundBody []byte,
	state *SessionState,
	contextWindow int,
	streamStarted bool,
	now time.Time,
) WindowTriggerResult {
	tokensEst := estimateBodyTokens(outboundBody)
	res := WindowTriggerResult{TokensEst: tokensEst}

	// Streaming guard: body already sent, skip.
	if streamStarted {
		res.SkipStream = true
		return res
	}

	// New / unknown session: never trigger.
	if state == nil {
		return res
	}

	// Read env-configurable thresholds (cheap: cached by the runtime).
	maxMsgCount := envInt("LLM_GATEWAY_WINDOW_MAX_MSG_COUNT", DefaultMaxMsgCount)
	idleSecs := envInt("LLM_GATEWAY_WINDOW_IDLE_SECONDS", DefaultIdleSeconds)
	minIdleMsgs := envInt("LLM_GATEWAY_WINDOW_MIN_IDLE_MSG_COUNT", DefaultMinIdleMsgCount)
	fraction := envFloat("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION", DefaultWindowFraction)

	// ── TOKEN trigger ────────────────────────────────────────────────────
	var threshold int
	if contextWindow > 0 {
		threshold = int(float64(contextWindow) * fraction * 3.5) // chars ≈ tokens × 3.5
		res.Threshold = threshold
		if len(outboundBody) > threshold {
			res.Reason = "sliding_window_token"
		}
	}

	// ── COUNT trigger ─────────────────────────────────────────────────────
	if res.Reason == "" && state.MsgCount >= maxMsgCount {
		res.Reason = "sliding_window_count"
	}

	// ── IDLE trigger ──────────────────────────────────────────────────────
	if res.Reason == "" && state.LastCompressedAt > 0 {
		idleElapsed := now.Unix() - state.LastCompressedAt
		if idleElapsed >= int64(idleSecs) && state.MsgCount >= minIdleMsgs {
			res.Reason = "sliding_window_idle"
		}
	}

	if res.Reason == "" {
		// No trigger.
		return res
	}

	// ── Mutual-exclusion guard ────────────────────────────────────────────
	if state.RecentlyCompressedAt > 0 {
		elapsed := now.Unix() - state.RecentlyCompressedAt
		if elapsed < RecentCompressedGuardSecs {
			// A proactive summary was written very recently. Degrade to
			// mechanical trim so we don't call the LLM again so soon.
			res.Degraded = true
			return res
		}
	}

	res.ShouldTrigger = true
	return res
}

// ──────────────────────────────────────────────────────────────────────────────
// Env helpers
// ──────────────────────────────────────────────────────────────────────────────

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}
