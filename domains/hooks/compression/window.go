// Package compressor - window.go (v3 T26)
//
// ShouldTriggerWindow decides whether the current session's outbound body
// should be proactively compressed (summarised) before forwarding to the
// upstream LLM.
//
// Three independent triggers (OR logic):
//
//  1. TOKEN trigger  — outbound body exceeds contextWindow × 0.80 threshold.
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

package compression

import (
	"os"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// DefaultTokenThresholdConsider is the outbound token count that marks a preliminary compression band.
	DefaultTokenThresholdConsider = 200_000
	// DefaultTokenThresholdForce is the absolute outbound token count that forces compression.
	DefaultTokenThresholdForce = 400_000

	// DefaultMaxMsgCount is the default message-count threshold for the
	// COUNT trigger. Configurable via LLM_GATEWAY_WINDOW_MAX_MSG_COUNT.
	DefaultMaxMsgCount = 50

	// DefaultIdleSeconds is the minimum idle time (seconds) for the IDLE
	// trigger. Configurable via LLM_GATEWAY_WINDOW_IDLE_SECONDS.
	DefaultIdleSeconds = 300 // 5 minutes

	// DefaultMinIdleMsgCount is the minimum message count required for the
	// IDLE trigger to fire. Prevents compression of very short idle session.
	DefaultMinIdleMsgCount = 10

	// RecentCompressedGuardSecs is the mutual-exclusion window: if a proactive
	// summary was written within this many seconds, the caller degrades to
	// mechanical trim only (avoids double LLM call in rapid-fire sequences).
	RecentCompressedGuardSecs = 60

	// DefaultWindowFraction is the fraction of contextWindow used as the
	// token-count threshold for the TOKEN trigger.
	DefaultWindowFraction = 0.80
)

// OutboundTokenBand classifies the actual body about to be forwarded to the
// model. It never classifies the raw client body in isolation: session delta
// assembly has already included the prior compressed history at this point.
type OutboundTokenBand string

const (
	OutboundTokenBandBelow       OutboundTokenBand = "below"
	OutboundTokenBandPreliminary OutboundTokenBand = "preliminary"
	OutboundTokenBandForced      OutboundTokenBand = "forced"
)

func classifyOutboundTokenBand(outboundTokens int) OutboundTokenBand {
	force := tokenThresholdForce()
	if force > 0 && outboundTokens > force {
		return OutboundTokenBandForced
	}
	consider := tokenThresholdConsider()
	if consider > 0 && outboundTokens > consider {
		return OutboundTokenBandPreliminary
	}
	return OutboundTokenBandBelow
}

func tokenThresholdConsider() int {
	return settings.GetPlatformInt("compression.token_threshold_consider", DefaultTokenThresholdConsider)
}

func tokenThresholdForce() int {
	return settings.GetPlatformInt("compression.token_threshold_force", DefaultTokenThresholdForce)
}

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

	// TokenBand classifies the post-delta, post-cache outbound body. It lets
	// telemetry distinguish a soft 200k warning from a forced 400k rewrite.
	TokenBand OutboundTokenBand

	// PriorLayerTokens is the cached estimate for the previous outbound body.
	// Together with TokensEst it documents the multi-layer decision inputs.
	PriorLayerTokens int

	// Threshold is the token threshold that was used (0 when contextWindow unknown).
	Threshold int
}

// ShouldTriggerWindow evaluates the three window triggers against the
// current outbound body and session state.
//
//   - outboundBody is the body to be forwarded (post-delta-append).
//   - state is the current SessionState (nil = new session; TOKEN/COUNT still apply).
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
	priorLayerTokens := 0
	if state != nil {
		priorLayerTokens = state.TokenEstimate
	}
	res := WindowTriggerResult{
		TokensEst:        tokensEst,
		TokenBand:        classifyOutboundTokenBand(tokensEst),
		PriorLayerTokens: priorLayerTokens,
	}

	// Streaming guard: body already sent, skip.
	if streamStarted {
		res.SkipStream = true
		return res
	}

	// New sessions may already contain an overlong client-supplied history, so
	// TOKEN/COUNT triggers must evaluate the current body even when state is nil.
	// Only IDLE and recent-compression guards require persisted state.

	// Read env-configurable thresholds (cheap: cached by the runtime).
	maxMsgCount := envInt("LLM_GATEWAY_WINDOW_MAX_MSG_COUNT", DefaultMaxMsgCount)
	idleSecs := envInt("LLM_GATEWAY_WINDOW_IDLE_SECONDS", DefaultIdleSeconds)
	minIdleMsgs := envInt("LLM_GATEWAY_WINDOW_MIN_IDLE_MSG_COUNT", DefaultMinIdleMsgCount)
	fraction := loadFractionWithDefault(DefaultWindowFraction)
	if fraction <= 0 || fraction > 1 {
		fraction = DefaultWindowFraction
	}

	// ── TOKEN trigger ────────────────────────────────────────────────────
	if res.TokenBand == OutboundTokenBandForced {
		// The absolute gate applies to the fully assembled outbound body, which
		// already contains the prior compressed session layer plus this turn's
		// delta. It is intentionally independent of the selected model's window.
		res.Threshold = tokenThresholdForce()
		res.Reason = "sliding_window_token_absolute"
	} else if contextWindow > 0 {
		threshold := int(float64(contextWindow) * fraction * 3.5) // chars ≈ tokens × 3.5
		res.Threshold = threshold
		if len(outboundBody) > threshold {
			res.Reason = "sliding_window_token"
		}
	}

	// ── COUNT trigger ─────────────────────────────────────────────────────
	currentMsgCount := countMessages(outboundBody)
	if currentMsgCount == 0 && state != nil {
		// Defensive fallback for malformed/legacy bodies whose message array
		// cannot be parsed; persisted metadata is still better than disabling
		// the count/idle guards entirely.
		currentMsgCount = state.MsgCount
	}
	if res.Reason == "" && currentMsgCount >= maxMsgCount {
		res.Reason = "sliding_window_count"
	}

	// ── IDLE trigger ──────────────────────────────────────────────────────
	if res.Reason == "" && state != nil && state.LastCompressedAt > 0 {
		idleElapsed := now.Unix() - state.LastCompressedAt
		if idleElapsed >= int64(idleSecs) && currentMsgCount >= minIdleMsgs {
			res.Reason = "sliding_window_idle"
		}
	}

	if res.Reason == "" {
		// No trigger.
		return res
	}

	// ── Mutual-exclusion guard ────────────────────────────────────────────
	if state != nil && state.RecentlyCompressedAt > 0 {
		elapsed := now.Unix() - state.RecentlyCompressedAt
		if elapsed < RecentCompressedGuardSecs {
			// A proactive summary was written very recently. Degrade to
			// mechanical trim so we don't call the LLM again so soon. The
			// absolute forced band is exempt: it must still enter the forced
			// fallback even when the model context window is unknown.
			if res.TokenBand != OutboundTokenBandForced {
				res.Degraded = true
				return res
			}
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
