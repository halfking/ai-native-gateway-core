// Package credential — status-code-aware weight nudge (T3.2 of the 154-server
// audit plan).
//
// Background
// The BanditScorer (scoring.go) picks a candidate credential by combining
// reliability × speed × intelligence, then multiplying by headroom and
// rateLimitFactor. Those two factors are coarse: rateLimitFactor only looks
// at quota-headroom, and headroom only looks at concurrency headroom. They
// do NOT distinguish 429 (rate limit) from "empty response" from "timeout" —
// all three just look like "this credential got a failure recently".
//
// What T1's audit surfaced (top 5xx / 4xx / 429 / timeout breakdowns) is that
// 429 and stream-timeout failures deserve a stronger demotion than a generic
// rate-limit hit, while a transient empty-response is less severe than a hard
// 429. WeightNudger encodes this as a small multiplicative factor that the
// bandit scorer can apply per-credential, alongside the existing headroom and
// rateLimitFactor.
//
// Design constraints (must remain true after this file):
//   - Pure function, no DB, no time, no globals. Reads only the input
//     snapshot. This keeps the nudge deterministic and easy to unit-test.
//   - Never multiplies by zero. Empty/recent observation windows must return
//     exactly 1.0 (no-op).
//   - Tunable but safe by default. DefaultWeights() returns 0.85/0.7/0.6/0.9
//     for the four kinds, which is conservative enough that flipping it on
//     out of the box does not crush any single credential.
//   - Composable with existing factors: the bandit scorer multiplies its
//     combined score by nudge × headroom × rateLimitFactor. Order is
//     irrelevant because all factors are are in [0.1, 1.0] and we're taking a
//     product.
//
// Configuration knobs (env vars):
//   LLM_GATEWAY_CREDENTIAL_WEIGHT_NUDGE=on|off         — kill switch
//   LLM_GATEWAY_CREDENTIAL_NUDGE_429=0.85              — rate-limit nudge
//   LLM_GATEWAY_CREDENTIAL_NUDGE_EMPTY=0.7             — empty-response nudge
//   LLM_GATEWAY_CREDENTIAL_NUDGE_TIMEOUT=0.6          — timeout nudge
//   LLM_GATEWAY_CREDENTIAL_NUDGE_AUTH=0.9              — 401/403 nudge
//   LLM_GATEWAY_CREDENTIAL_NUDGE_WINDOW=10m            — recent-observation window
package credential

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// KindWindow captures how many of each error kind landed within the recent
// window. Counts are not strictly necessary — we only need "did any land in
// the window" — but having counts lets future code weight the nudge by
// frequency. For now we treat any observation in the window as "yes".
type KindWindow struct {
	// RateLimit is incremented for KindRateLimit, KindQuota*, and explicit
	// 429s from upstream.
RateLimit int
	// Empty is incremented for KindEmptyResponse and short zero-byte streams.
Empty int
	// Timeout is incremented for KindTimeout, KindStreamTimeout, KindNetwork.
Timeout int
	// Auth is incremented for KindAuth, KindAuthRevoked.
Auth int
}

// Any returns true when at least one observation landed in the window.
// Kept as an exported helper because the bandit scorer will want to skip
// the multiplication entirely when no observation is present.
func (k KindWindow) Any() bool {
	return k.RateLimit > 0 || k.Empty > 0 || k.Timeout > 0 || k.Auth > 0
}

// WeightNudgeFactors captures the per-kind multiplicative weight factors.
// All values must be in [0.1, 1.0]. Zero and negative values are clamped to
// 1.0 (no-op) so a missing env var can never produce a hard zero that drops
// a credential from the pool.
type WeightNudgeFactors struct {
	RateLimit float64
	Empty     float64
	Timeout   float64
	Auth      float64
}

// DefaultWeightNudgeFactors is the conservative baseline. Values chosen so
// flipping the feature on out-of-the-box does not crush any one credential.
func DefaultWeightNudgeFactors() WeightNudgeFactors {
	return WeightNudgeFactors{
		RateLimit: 0.85,
		Empty:     0.70,
		Timeout:   0.60,
		Auth:      0.90,
	}
}

// LoadWeightNudgeFactors reads env vars on top of defaults. Bad input
// (negative, non-numeric, zero) is silently clamped to defaults — the
// nudge is safety-rated and must never silently break routing.
func LoadWeightNudgeFactors() WeightNudgeFactors {
	f := DefaultWeightNudgeFactors()
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_NUDGE_429")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0.1 && n <= 1.0 {
			f.RateLimit = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_NUDGE_EMPTY")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0.1 && n <= 1.0 {
			f.Empty = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_NUDGE_TIMEOUT")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0.1 && n <= 1.0 {
			f.Timeout = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_NUDGE_AUTH")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0.1 && n <= 1.0 {
			f.Auth = n
		}
	}
	return f
}

// WeightNudgeEnabled reports the kill-switch state.
func WeightNudgeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_WEIGHT_NUDGE"))) {
	case "on", "true", "1":
		return true
	}
	return false
}

// WeightNudgeWindow returns the configured look-back duration, defaulting to
// 10 minutes. Bad input falls back to the default.
func WeightNudgeWindow() time.Duration {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_NUDGE_WINDOW")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			return d
		}
	}
	return 10 * time.Minute
}

// WeightNudge returns the multiplicative factor to apply to a candidate's
// combined bandit score. Pure function: identical inputs always produce the
// same output. Returns 1.0 (no-op) when:
//
//   - the nudge is disabled via the kill switch
//   - the input window has no observations
//   - the input factors are zero/negative (treated as "use defaults")
//
// Otherwise it picks the minimum factor across the kinds that actually
// occurred in the window. Using the minimum (rather than the product)
// ensures we don't compound factors for credentials that hit multiple kinds
// at once — the score never collapses to near-zero on a transient spike.
//
// The result is clamped to [0.1, 1.0]: a factor above 1.0 would BOOST a
// credential that just hit auth failures, which is the opposite of what
// we want. Floor 0.1 keeps the bandit score from collapsing to zero and
// fully excluding a recoverable credential.
func WeightNudge(window KindWindow, factors WeightNudgeFactors, enabled bool) float64 {
	if !enabled || !window.Any() {
		return 1.0
	}
	worst := 1.0
	if window.RateLimit > 0 && factors.RateLimit < worst {
		worst = factors.RateLimit
	}
	if window.Empty > 0 && factors.Empty < worst {
		worst = factors.Empty
	}
	if window.Timeout > 0 && factors.Timeout < worst {
		worst = factors.Timeout
	}
	if window.Auth > 0 && factors.Auth < worst {
		worst = factors.Auth
	}
	// Hard clamp to [0.1, 1.0] — protects against direct construction
	// (e.g. WeightNudgeFactors{Auth: 1.5}) that bypasses LoadWeightNudgeFactors'
	// input validation.
	if worst < 0.1 {
		return 0.1
	}
	if worst > 1.0 {
		return 1.0
	}
	return worst
}