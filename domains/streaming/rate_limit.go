// relay/rate_limit.go — unified gateway-layer API key rate limiting.
//
// 2026-06-15: extracted from handler.go / responses.go / messages.go to fix
// the three-endpoint inconsistency bug where /v1/responses and
// /v1/messages would skip RPM checks whenever the database row's
// rate_limit_rpm column was NULL, while /v1/chat/completions would fall
// back to tierDefaults — same key, different rules, depending on which
// endpoint the client hit.
//
// Behaviour contract (now identical across all three endpoints):
//
//	DB=NULL  → fall back to tier default (per authentication.EffectiveRPM)
//	DB=0     → explicit unlimited (CheckRPM treats limit<=0 as no cap)
//	DB=N>0   → cap at N RPM
package streaming

import (
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// rateLimitOutcome is the structured result of a single gateway RPM check.
//
//	Skipped   — no keyInfo / limiter configured / internal key (caller continues)
//	Blocked   — exceeded the per-key RPM cap (caller MUST stop and write the response)
//	Otherwise — within cap; caller may proceed (Limit/Remaining populated for telemetry)
type rateLimitOutcome struct {
	Skipped   bool
	Blocked   bool
	Limit     int
	Remaining int
	ResetSec  int
}

// checkGatewayRateLimit runs the single-source-of-truth RPM check used by
// the chat, responses and messages endpoints. It never writes to the
// response — callers compose headers + body themselves so each endpoint
// keeps its own response shape (OpenAI vs Anthropic vs Responses).
//
// AUDIT-2 (2026-07-12): when rate_limit.enabled is OFF, the entire
// gateway RPM layer is bypassed (Skipped=true). This matches the user
// semantic "限流降级模块关闭时不限制 RPM"; only the upstream provider's
// own rate-limit (which the LLM gateway cannot control) still applies.
func checkGatewayRateLimit(keyInfo *authentication.KeyInfo, rl ratelimit.RPMLimiter) rateLimitOutcome {
	// AUDIT-2: 限流总开关关闭 → 整个 RPM 检查 no-op。
	if !ratelimit.IsRateLimitEnabled() {
		return rateLimitOutcome{Skipped: true}
	}
	if keyInfo == nil || rl == nil || keyInfo.IsInternal {
		return rateLimitOutcome{Skipped: true}
	}
	limit := keyInfo.EffectiveRPM()
	if limit <= 0 {
		// Explicit unlimited (DB=0). No headers, no check.
		return rateLimitOutcome{Limit: 0, Remaining: -1, ResetSec: 0}
	}
	if !rl.CheckRPM(keyInfo.ID, limit) {
		_, remaining := rl.RPMStatus(keyInfo.ID, limit)
		if remaining < 0 {
			remaining = 0
		}
		return rateLimitOutcome{
			Blocked:   true,
			Limit:     limit,
			Remaining: remaining,
			ResetSec:  60,
		}
	}
	return rateLimitOutcome{Limit: limit, Remaining: -1, ResetSec: 0}
}

func rateLimitOutcomeKind(o rateLimitOutcome) string {
	if o.Skipped {
		return "skipped"
	}
	if o.Blocked {
		return "blocked"
	}
	return "passed"
}

// applicable. Always writes Retry-After when the request was blocked.
//
// Header semantics follow RFC draft-ietf-httpapi-ratelimit-headers:
//   - X-RateLimit-Limit      : per-minute cap (omitted when unlimited)
//   - X-RateLimit-Remaining  : remaining requests in the current window
//   - X-RateLimit-Reset      : Unix epoch seconds when the window resets
//   - Retry-After            : seconds the client should wait before retrying
func writeRateLimitHeaders(w http.ResponseWriter, o rateLimitOutcome) {
	if o.Limit > 0 {
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", o.Limit))
		if o.Remaining >= 0 {
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", o.Remaining))
		}
		if o.ResetSec > 0 {
			w.Header().Set("X-RateLimit-Reset",
				fmt.Sprintf("%d", time.Now().Add(time.Duration(o.ResetSec)*time.Second).Unix()))
		}
	}
	if o.Blocked && o.ResetSec > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", o.ResetSec))
	}
}
