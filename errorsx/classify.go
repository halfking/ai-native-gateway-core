package errorsx

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

type ErrorKind string

const (
	KindTransient      ErrorKind = "transient"
	KindTimeout        ErrorKind = "timeout"
	KindNetwork        ErrorKind = "network"
	KindRateLimit      ErrorKind = "rate_limit"
	KindAuth           ErrorKind = "auth"
	KindQuota          ErrorKind = "quota"
	KindUpstreamDown   ErrorKind = "upstream_down"
	KindCanceled       ErrorKind = "canceled"
	KindConcurrent     ErrorKind = "concurrent"
	KindAuthRevoked    ErrorKind = "auth_revoked"
	KindQuotaPeriodic  ErrorKind = "quota_periodic"
	KindQuotaBalance   ErrorKind = "quota_balance"
	KindQuotaPermanent ErrorKind = "quota_permanent"
	KindModelNotFound  ErrorKind = "model_not_found"
	KindStreamTimeout  ErrorKind = "stream_timeout"
	// KindToolCallIdMismatch: client echoed a tool_call_id that the
	// upstream did not recognise. The most common cause is the agent
	// framework losing the id field during streaming accumulation of
	// `delta.tool_calls` chunks, or generating a placeholder id when
	// the upstream response didn't carry one. Reported as a distinct
	// non-fatal, non-retryable kind so the gateway does not punish
	// the credential (this is a client bug, not an upstream failure).
	KindToolCallIdMismatch ErrorKind = "tool_call_id_mismatch"
	// KindContextLength: upstream rejected the request because the prompt
	// (or accumulated conversation history) exceeded the model's context
	// window. Distinct from KindTransient so the executor can:
	//   1. Surface a structured retry-after-trim path
	//      (routing/executor_chat.go attempts one trim+retry before bubbling
	//      the 4xx up to the client).
	//   2. Avoid the standard retryable-error fast path that would re-send
	//      the same oversized body to a different credential.
	// This matches minimax's behaviour: direct calls to api.minimaxi.com
	// silently slide-window trim a too-long conversation, but the proxy
	// path was historically returning the raw 400 because no client-side
	// trim was applied.
	KindContextLength      ErrorKind = "context_length_exceeded"
	KindUnsupportedFeature ErrorKind = "unsupported_feature"
	// KindContentFilter: upstream rejected the request based on content
	// moderation / safety policy (e.g. MiniMax 422 "new_sensitive (1026)",
	// OpenAI "content_filter", Anthropic content policy). This is
	// content-determined: the same prompt is rejected on every sibling
	// credential of the same provider, so the executor short-circuits
	// (does NOT retry other candidates) and the handler returns a 400 to
	// the client with the upstream reason + an actionable hint. The
	// credential is healthy — no cooling / circuit / state write.
	KindContentFilter ErrorKind = "content_filter"
)

// contextLengthRe matches upstream error bodies that signal "prompt too
// long for the model's context window". The patterns are deliberately
// broad because each provider phrases the same condition differently:
//
//   - OpenAI:     "This model's maximum context length is 8192 tokens..."
//   - Anthropic:  "prompt is too long", "input is too long"
//   - minimax:    "context_length_exceeded", "max_tokens exceed"
//   - deepseek:   "context window exceeded"
//   - zhipu/glm:  "上下文长度超出限制", "tokens too long"
//
// We keep the CJK alternative because a few domestic providers localise
// the error string rather than returning the canonical English form.
var contextLengthRe = regexp.MustCompile(
	`(?i)(context[ _-]?length[ _-]?exceeded|` +
		`maximum context length|` +
		`context[ _-]?window[ _-]?(exceeded|is)|` +
		`prompt is too long|` +
		`input is too long|` +
		`too many (input )?tokens|` +
		`tokens? exceed|` +
		`reduce the length|` +
		`maximum number of tokens)`,
)
var contextLengthCJKRe = regexp.MustCompile(
	`上下文(长度)?(超出|超过|超限)|` +
		`tokens? (过多|超限|超过)|` +
		`输入(过长|太长)|` +
		`超出(模型)?(最大)?(上下文|长度|限制)`,
)

// modelNotFoundRe is intentionally narrow (P5 of 2026-06-18-model-match-and-404-plan.md).
//
// We previously used 50-char window regexes (e.g. `model.{0,40}(not found)`) which
// produced false positives on body strings like:
//
//	"Your previous model training run was not found, retry with a new id"
//	"Model is not available in your region"
//	"Model glm-5.1 has been deprecated, please use glm-5.2"
//
// Such cases are NOT model_not_found (they're region/quota/deprecation errors
// that deserve a different status code and a different code path) and the
// gateway used to silently swallow them, surfacing a generic 404 to the
// caller. The new pattern uses \b word boundaries and an explicit identifier
// token between the noun and the "not found" phrase, so the match requires
// the body to literally be talking about a specific model/endpoint name.
//
// Removed phrases and why:
//   - "model.{0,40}not available"     — over-matches region/tier restrictions.
//   - "model.{0,40}deprecated|retired|sunset" — deprecation has its own
//     semantics (a working model being scheduled for removal) and should
//     NOT short-circuit routing to a 404. Future work: a dedicated
//     KindDeprecated kind for telemetry.
var modelNotFoundRe = regexp.MustCompile(
	`(?i)(` +
		`\b(model|endpoint)[\s:]+['"]?[a-z0-9._\-/:]{1,80}['"]?\s+(does not exist|is not found|not found|is unknown|unknown)\b|` +
		`\b(no such|unknown)\s+model\b` +
		`)`,
)
var modelNotFoundCJKRe = regexp.MustCompile(
	`模型不存在|模型.{0,10}不存在|模型.{0,10}未找到`,
)

var unsupportedFeatureRe = regexp.MustCompile(
	`(?i)((does not|doesn'?t) support (coding plan|tool|function|tools|function call)|` +
		`(tool|function)[- _]?call(ing|s)? (is )?not supported|` +
		`unsupported (parameter|model|feature).{0,20}(tools?|function|tool_choice)|` +
		`当前模型不支持)`,
)

// concurrentOverloadRe matches upstream error bodies that signal
// "service overloaded / too many concurrent requests". Providers like
// MiniMax surface concurrent-rate-limit problems as either:
//   - HTTP 429/503 with messages containing "concurrent", "too many",
//     "overloaded", "engine busy", "rpm/tpm", etc.
//   - SSE streams that close prematurely (EOF without [DONE]) under load.
//
// Both patterns must be classified as KindConcurrent so the breaker can
// apply the 5-minute cooling policy and immediately route to the next
// candidate credential instead of retrying the same overloaded one.
var concurrentOverloadRe = regexp.MustCompile(
	`(?i)(concurrent.{0,30}(limit|exceed|over|too many|reach|max)|` +
		`too many (concurrent|requests|connections)|` +
		`(engine|server|service|api) (overloaded|too busy|busy)|` +
		`(server|service|upstream) (is )?(overload|under pressure)|` +
		`(rpm|tpm).{0,20}(limit|exceed|reach|over)|` +
		`request(ed|s)? too (fast|frequent|many)|` +
		`slow down|try again later|backoff)`,
)
var concurrentOverloadCJKRe = regexp.MustCompile(
	`并发.{0,15}(超限|过大|过高|达到上限|超过限制)|` +
		`请求.{0,10}(过快|频繁|太多)|` +
		`服务.{0,10}(繁忙|过载|压力|降级)|` +
		`稍后重试|限流`,
)

// eofWithoutDoneRe is a Go-level signal: the SSE stream closed before
// the [DONE] sentinel. When combined with provider-known overload (or
// repeated across the same credential) this is treated as concurrent
// overload — upstream silently dropping connections under load.
var eofWithoutDoneRe = regexp.MustCompile(
	`(?i)(eof without|eof_without_done|stream closed before|premature close|unexpected eof)`,
)

// toolCallIdMismatchRe matches the upstream error payload for the
// "client echoed a tool_call_id the upstream did not recognise"
// scenario. MiniMax surfaces this as a 4xx body with code 2013
// and a message containing "tool call id" / "tool id" /
// "tool_result's tool id". Anthropic's equivalent would be a
// 4xx with "tool_use_id" — the regex below is permissive about
// the exact noun so it can flag both vendors without future edits.
var toolCallIdMismatchRe = regexp.MustCompile(
	`(?i)(tool[_ ]?(call[_ ]?id|use[_ ]?id|result.*tool[_ ]?id).{0,40}(not found|not exist|invalid|unknown|unknown id|does not exist|unrecogn)|2013)`,
)

// contentFilterRe matches upstream error bodies that signal a
// content-moderation / safety-policy rejection. Scoped to avoid false
// positives on legitimate "sensitive" tokens (e.g. "case-sensitive"):
//   - new_sensitive: MiniMax-specific moderation code token.
//   - content_filter / content_policy / content_moderation: OpenAI + others.
//   - policy_violation: generic policy term.
//   - sensitive followed by a 2-5 digit code in optional parens: the
//     MiniMax body shape "input new_sensitive (1026)" / "sensitive (2013)".
//   - prohibited/forbidden content/input/material: generic phrasing.
//   - CJK equivalents for domestic providers.
var contentFilterRe = regexp.MustCompile(
	`(?i)(new_sensitive|` +
		`content[_ -]?(filter|policy|moderation)|` +
		`policy[_ -]?violation|` +
		`sensitive[_ ]?\(?\d{2,5}\)?|` +
		`(prohibited|forbidden).{0,30}(content|input|material)|` +
		`安全(审查|策略)|内容(违规|敏感|不合规)|包含敏感)`,
)

func ClassifyError(err error, resp *http.Response) ErrorKind {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return KindCanceled
		}
		msg := err.Error()
		// Order matters: overload and EOF-without-done are checked
		// before generic timeouts because upstream-reported overload
		// messages often include words like "timeout" or "connection"
		// that would otherwise be mis-classified.
		if concurrentOverloadRe.MatchString(msg) || concurrentOverloadCJKRe.MatchString(msg) {
			return KindConcurrent
		}
		if eofWithoutDoneRe.MatchString(msg) {
			// EOF without [DONE] is most often a benign provider quirk
			// (e.g. MiniMax omits the [DONE] sentinel on successful
			// streams). Treat as KindStreamTimeout, which is retryable
			// and short-cooling — the executor's chunk-count heuristic
			// already distinguishes benign eof (with chunks sent) from
			// a real overload.
			return KindStreamTimeout
		}
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
			return KindTimeout
		}
		if strings.Contains(msg, "connection") || strings.Contains(msg, "refused") ||
			strings.Contains(msg, "no such host") || strings.Contains(msg, "reset") {
			return KindNetwork
		}
		if modelNotFoundRe.MatchString(msg) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.MatchString(msg) {
			return KindUnsupportedFeature
		}
		// 2026-07-03 (Bug #N, defense in depth): the body-driven kinds
		// below were previously only checked in ClassifyErrorWithBody /
		// ClassifyResponseBody, which require the raw body bytes. When
		// an upstream.Error is re-wrapped via fmt.Errorf (the legacy
		// `fmt.Errorf("upstream %d: %s", status, body)` pattern that
		// the executor's tryCandidate used to emit), the body is
		// collapsed into err.Error() and ClassifyError(err, nil) was
		// returning KindTransient for these cases. That broke the
		// IsClientBug / IsRetryable / writeCredentialStateOnError
		// branches downstream and polluted request_logs.error_kind
		// with "transient" for what is actually a client-side bug.
		//
		// Running the same regexes on the err.Error() string closes
		// the gap for any caller (current or future) that loses the
		// typed *upstream.Error while still keeping the body text in
		// the wrapped message. The patterns are model-agnostic on
		// purpose — they match OpenAI, Anthropic, MiniMax, deepseek,
		// and zhipu bodies alike.
		if toolCallIdMismatchRe.MatchString(msg) {
			return KindToolCallIdMismatch
		}
		if contextLengthRe.MatchString(msg) || contextLengthCJKRe.MatchString(msg) {
			return KindContextLength
		}
		return KindTransient
	}
	if resp == nil {
		return KindUpstreamDown
	}
	return ClassifyResponseStatus(resp)
}

// ClassifyResponseStatus maps an upstream HTTP response status code to
// an ErrorKind. Status-only (no body peek) so it's safe to call on a
// response whose body is still owned by another reader. Use
// ClassifyErrorWithBody when you also have the body bytes available
// and want overload/model-not-found signals from the payload.
func ClassifyResponseStatus(resp *http.Response) ErrorKind {
	switch {
	case resp.StatusCode == 429:
		// 429 with overload-shaped body is upgraded to KindConcurrent by
		// ClassifyErrorWithBody; a plain 429 stays as quota-style rate limit.
		return KindRateLimit
	case resp.StatusCode == 503, resp.StatusCode == 529:
		// 503 Service Unavailable and 529 (Anthropic-style Site Overloaded)
		// are commonly used to signal concurrent load.
		return KindConcurrent
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return KindAuth
	case resp.StatusCode == 402:
		return KindQuota
	case resp.StatusCode >= 500:
		return KindUpstreamDown
	default:
		return KindTransient
	}
}

// ClassifyErrorWithBody maps a (status, body) pair to an ErrorKind. It
// first inspects the body for concurrent-overload and model-not-found
// signals (which can be conveyed via JSON/SSE payloads rather than
// status codes), then falls back to status-based classification.
//
// Callers should pass the body bytes they've already read (or nil if
// the body was unreadable). The body is NOT consumed here; ownership
// stays with the caller.
func ClassifyErrorWithBody(status int, body []byte) ErrorKind {
	if len(body) > 0 {
		// 2026-07-04 V20 fix: exclude generic web-server 404 bodies from
		// model_not_found classification. Patterns like "404 page not found",
		// "404 Not Found", "The page you requested was not found" come from
		// Nginx, Apache, Caddy, Traefik, or cloud load balancers when the
		// upstream service is unreachable or misconfigured — not from LLM
		// providers. Without this guard, such responses are mis-classified as
		// KindModelNotFound, causing the router to skip cross-credential retry
		// and surface a 404 to the client when the real problem is routing or
		// infrastructure (should be KindUpstreamDown or KindEmpty).
		//
		// Exclusion heuristic: if body contains HTTP status line artifacts
		// ("404 Not Found", "404 page not found") OR generic web phrases
		// ("page you requested", "resource not found", "page not found")
		// within the first 512 bytes, do NOT apply model-specific patterns.
		bodyLower := strings.ToLower(string(body))
		if len(bodyLower) > 512 {
			bodyLower = bodyLower[:512]
		}
		// Some upstreams report account balance exhaustion as HTTP 403 instead
		// of 402. Classifying it as auth would quarantine a valid credential
		// until an operator intervenes, even after the account is topped up.
		if strings.Contains(bodyLower, "insufficient balance") ||
			strings.Contains(bodyLower, "billing_error") ||
			strings.Contains(bodyLower, "balance exhausted") ||
			strings.Contains(bodyLower, "quota exceeded") {
			return KindQuota
		}
		isGenericWebError := strings.Contains(bodyLower, "404 not found") ||
			strings.Contains(bodyLower, "404 page not found") ||
			strings.Contains(bodyLower, "page you requested") ||
			strings.Contains(bodyLower, "page not found") ||
			strings.Contains(bodyLower, "resource not found")

		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		// P5 (2026-06-18): model_not_found only on 400/404/422, matching
		// ClassifyResponseBody's status gate. A 5xx body that mentions
		// "model not found" (e.g. a misconfigured proxy returning 502 with
		// a downstream 404 embedded) must be KindUpstreamDown, not
		// KindModelNotFound — the failure is connectivity, not model existence.
		//
		// V20 (2026-07-04): additionally gate on !isGenericWebError. If the
		// body looks like a generic web server 404 (Nginx/Apache/LB), do NOT
		// classify as KindModelNotFound even if modelNotFoundRe matches.
		if !isGenericWebError &&
			(status == 400 || status == 404 || status == 422) &&
			(modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body)) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.Match(body) {
			return KindUnsupportedFeature
		}
		if toolCallIdMismatchRe.Match(body) {
			return KindToolCallIdMismatch
		}
		// Content-moderation / safety-policy rejection (MiniMax 422
		// "new_sensitive (1026)", OpenAI "content_filter", etc). Gate on
		// the typical moderation status codes so a 200 body that happens
		// to contain "sensitive" (e.g. a benign word) is not mis-flagged.
		if (status == 400 || status == 403 || status == 422 || status == 451) &&
			contentFilterRe.Match(body) {
			return KindContentFilter
		}
		if (status == 400 || status == 413 || status == 422) &&
			(contextLengthRe.Match(body) || contextLengthCJKRe.Match(body)) {
			return KindContextLength
		}
	}
	// 2026-06-13: protocol/shape 4xx codes (e.g. 405 Method Not Allowed,
	// 406 Not Acceptable, 415 Unsupported Media Type) are NOT transient —
	// retrying the same payload on a different credential would just
	// bounce off the same upstream rule. Map them to KindUnsupportedFeature
	// so IsClientBug returns true, the executor skips cross-redential
	// retry, and the credential is NOT cooled. 408 (Request Timeout) is
	// mapped to KindTimeout so the network-retry path still fires.
	//
	// 2026-07-08: REMOVED 422 from this list. 422 "Unprocessable Entity"
	// has far broader semantics than the protocol-shape codes above — it
	// is how MiniMax surfaces content-moderation rejections
	// ("new_sensitive (1026)"), how some providers surface context-length
	// errors, and how clients surface malformed-tool bodies. Each of those
	// is classified above via a body-pattern match (contentFilterRe,
	// contextLengthRe, unsupportedFeatureRe). A 422 that matches none of
	// those patterns is ambiguous and should fall through to
	// ClassifyResponseStatus (KindTransient by default) rather than being
	// force-cast to KindUnsupportedFeature, which previously caused
	// content-filter rejections to masquerade as "unsupported_feature"
	// and be retried across every credential.
	switch status {
	case 408:
		return KindTimeout
	case 405, 406, 409, 410, 411, 412, 415, 416, 417, 418, 421, 423, 424, 425, 426, 428, 431:
		return KindUnsupportedFeature
	}
	return ClassifyResponseStatus(&http.Response{StatusCode: status})
}

// ClassifyResponseBody inspects an error body fragment (e.g. SSE error
// chunk) for upstream-error signals. Returns the matched ErrorKind or
// "" if the body doesn't match any of the known patterns (caller should
// fall through to other classification).
//
// P5 of 2026-06-18-model-match-and-404-plan.md: model_not_found is only
// returned when the HTTP status is one of 400, 404, or 422. A 5xx body
// that happens to mention "model not found" (e.g. a misconfigured proxy
// returning a 502 with a downstream 404 embedded) is treated as
// KindUpstreamDown instead, because the failure is the gateway's
// connectivity, not the model's existence.
func ClassifyResponseBody(status int, body []byte) ErrorKind {
	if len(body) > 0 {
		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		if (status == 400 || status == 404 || status == 422) &&
			(modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body)) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.Match(body) {
			return KindUnsupportedFeature
		}
		if eofWithoutDoneRe.Match(body) {
			return KindStreamTimeout
		}
		if toolCallIdMismatchRe.Match(body) {
			return KindToolCallIdMismatch
		}
		if (status == 400 || status == 403 || status == 422 || status == 451) &&
			contentFilterRe.Match(body) {
			return KindContentFilter
		}
		if contextLengthRe.Match(body) || contextLengthCJKRe.Match(body) {
			return KindContextLength
		}
	}
	return ""
}

// IsConcurrentOverload returns true when the error reason matches known
// upstream concurrent-overload signals (rate-limit body text, etc.).
// NOTE: eof_without_done is NOT treated as concurrent overload here —
// many providers (MiniMax in particular) simply omit the [DONE] sentinel
// on otherwise successful streams. The executor handles eof_without_done
// separately, using chunk-count heuristics to distinguish a genuinely
// truncated stream from a benign missing-sentinel.
func IsConcurrentOverload(reason string) bool {
	if reason == "" {
		return false
	}
	return concurrentOverloadRe.MatchString(reason) || concurrentOverloadCJKRe.MatchString(reason)
}

func IsModelNotFound(kind ErrorKind) bool {
	return kind == KindModelNotFound
}

func IsRetryable(kind ErrorKind) bool {
	switch kind {
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindConcurrent, KindStreamTimeout:
		return true
	// KindContextLength is intentionally NOT in this list. The executor
	// handles context-length 4xx via a dedicated path: it tries one
	// client-side trim+retry (executor_chat.go around the 4xx handler) and
	// then surfaces the 4xx to the caller. Adding it here would route it
	// into the generic retry loop with the same oversized body, which
	// would just bounce off the same upstream limit.
	default:
		return false
	}
}

// IsContextLength returns true if the kind is a context-window exceeded
// signal. Callers use this to decide whether to attempt a one-shot
// client-side trim before bubbling the 4xx up.
func IsContextLength(kind ErrorKind) bool {
	return kind == KindContextLength
}

// IsContentFilter returns true if the kind signals an upstream content-
// moderation / safety-policy rejection. The executor uses this to
// short-circuit the candidate loop (the same content is rejected on
// every sibling credential) and the handler uses it to render a 400
// with the upstream reason + actionable hint. Intentionally NOT in
// IsClientBug (which retries the next candidate), NOT in IsRetryable,
// and NOT in shouldWriteCredentialState (the credential is healthy).
func IsContentFilter(kind ErrorKind) bool {
	return kind == KindContentFilter
}

func IsCredentialFatal(kind ErrorKind) bool {
	switch kind {
	case KindAuth, KindAuthRevoked, KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent:
		return true
	default:
		return false
	}
}

// IsClientBug returns true when the kind signals a client-side bug
// (e.g. KindToolCallIdMismatch) where writing credential cooling state
// would only punish the credential for the caller's mistake. The
// gateway should still surface the error to the caller and log the
// detail, but must NOT open a circuit or write availability_recover_at
// in the credentials table.
func IsClientBug(kind ErrorKind) bool {
	// 2026-07-03: Removed KindModelNotFound from this list.
	// model_not_found is typically a provider issue (model removed/renamed
	// upstream), not a client bug. It should trigger binding unavailability
	// via the dedicated mnf branch in executor.go, not skip state writes.
	switch kind {
	case KindToolCallIdMismatch, KindUnsupportedFeature, KindCanceled:
		return true
	default:
		return false
	}
}
