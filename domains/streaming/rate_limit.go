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
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// rateLimitOutcome is the structured result of a single gateway RPM check.
//
//	Skipped   — no keyInfo / limiter configured / internal key (caller continues)
//	Blocked   — exceeded the per-key RPM cap (caller MUST stop and write the response)
//	Otherwise — within cap; caller may proceed (Limit/Remaining populated for telemetry)
type rateLimitOutcome struct {
	Skipped   bool
	Blocked   bool // queue full or admission failed; caller writes the canonical error
	Limit     int
	Remaining int
	ResetSec  int
	Queue     ratelimit.AdmissionResult
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
//
// The static data-plane key is a gateway admission key shared by frontend
// callers. It must not be treated as a tenant API-key quota, or aggregate
// frontend traffic exhausts one shared RPM window. AuthMiddleware creates the
// context marker only after constant-time static-key verification.
func checkGatewayRateLimit(ctx context.Context, keyInfo *authentication.KeyInfo, rl ratelimit.RPMLimiter, notify func(ratelimit.AdmissionResult)) rateLimitOutcome {
	// AUDIT-2: 限流总开关关闭 → 整个 RPM 检查 no-op。
	if !ratelimit.IsRateLimitEnabled() {
		return rateLimitOutcome{Skipped: true}
	}
	if middleware.IsGlobalAuthPassed(ctx) {
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
	if admission, ok := rl.(ratelimit.RPMAdmission); ok {
		// 2026-08-26: budget the queue wait. Queued requests used to wait up
		// to 2 minutes (maxMinuteBucketWait) while the request context only
		// carries the upstream timeout (LLM_GATEWAY_UPSTREAM_TIMEOUT=60s on
		// 154/245). Any queue wait longer than the remaining budget produced
		// a mid-flight "context canceled" 502 with no request_logs row — the
		// kimi-k3 "always fails" incident. When the limiter supports
		// budgeted admission, reject fast with a 429 + Retry-After instead.
		maxQueueWait, hasDeadline := rateLimitQueueBudget(ctx)
		if hasDeadline && maxQueueWait <= 0 {
			// No usable budget left at all (deadline already within headroom).
			// Reject immediately rather than queue-then-cancel.
			return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 1}
		}
		if budgeted, ok := rl.(ratelimit.RPMBudgetedAdmission); ok && hasDeadline {
			result, err := budgeted.AdmitRPMWithBudget(ctx, keyInfo.ID, limit, maxQueueWait, notify)
			if err == nil {
				return rateLimitOutcome{Limit: limit, Remaining: result.Remaining, Queue: result}
			}
			if errors.Is(err, ratelimit.ErrQueueBudgetExceeded) {
				resetSec := result.EstimatedWaitSec
				if resetSec < 1 {
					resetSec = 1
				}
				return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: resetSec, Queue: result}
			}
			if errors.Is(err, ratelimit.ErrMinuteBucketFull) || errors.Is(err, ratelimit.ErrMinuteBucketWaitTimeout) {
				return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 60, Queue: result}
			}
			return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 1}
		}
		if waiting, ok := rl.(ratelimit.RPMWaitingNotifier); ok {
			result, err := waiting.AdmitRPMWithWait(ctx, keyInfo.ID, limit, notify)
			if err == nil {
				return rateLimitOutcome{Limit: limit, Remaining: result.Remaining, Queue: result}
			}
			if !errors.Is(err, ratelimit.ErrMinuteBucketFull) {
				return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 1}
			}
			return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 60, Queue: result}
		}
		result, err := admission.AdmitRPM(ctx, keyInfo.ID, limit)
		if err == nil {
			return rateLimitOutcome{Limit: limit, Remaining: result.Remaining, Queue: result}
		}
		if !errors.Is(err, ratelimit.ErrMinuteBucketFull) {
			return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 1}
		}
		return rateLimitOutcome{Blocked: true, Limit: limit, ResetSec: 60, Queue: result}
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

// queueBudgetHeadroom is the minimum time that must remain for the request
// to reach upstream after rate-limit admission. Queueing beyond this always
// ends in "context canceled" mid-flight, so it is never a useful wait.
const queueBudgetHeadroom = 5 * time.Second

// rateLimitQueueBudget derives how long a request may sit in the RPM queue:
// the request context's remaining deadline minus a fixed headroom reserved
// for the actual upstream call. A context without a deadline (background
// callers, tests) means "no budget" → 0 → legacy unlimited queueing.
// rateLimitQueueBudget returns the maximum time a request may sit in the
// RPM queue and whether the context carries a deadline. No deadline means
// "no budget" → hasDeadline=false and callers fall back to legacy queuing.
func rateLimitQueueBudget(ctx context.Context) (maxWait time.Duration, hasDeadline bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	return time.Until(deadline) - queueBudgetHeadroom, true
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
	if o.Queue.Limit > 0 {
		w.Header().Set("X-RateLimit-Queue-Limit", fmt.Sprintf("%d", o.Queue.Limit))
		w.Header().Set("X-RateLimit-Queue-Remaining", fmt.Sprintf("%d", o.Queue.QueueRemaining))
		if o.Queue.Position > 0 {
			w.Header().Set("X-RateLimit-Queue-Position", fmt.Sprintf("%d", o.Queue.Position))
		}
	}
}

func notifyRateLimitWait(w http.ResponseWriter, stream bool) func(ratelimit.AdmissionResult) {
	if !stream {
		return nil
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	return func(result ratelimit.AdmissionResult) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprintf(w, "event: rate_limit_waiting\ndata: %s\n\n", fmt.Sprintf(`{"type":"rate_limit_waiting","message":"rate limited; waiting for next time bucket","position":%d}`, result.Position))
		flusher.Flush()
	}
}
