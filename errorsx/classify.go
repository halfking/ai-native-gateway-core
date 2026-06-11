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
	KindTransient       ErrorKind = "transient"
	KindTimeout         ErrorKind = "timeout"
	KindNetwork         ErrorKind = "network"
	KindRateLimit       ErrorKind = "rate_limit"
	KindAuth            ErrorKind = "auth"
	KindQuota           ErrorKind = "quota"
	KindUpstreamDown    ErrorKind = "upstream_down"
	KindCanceled        ErrorKind = "canceled"
	KindConcurrent      ErrorKind = "concurrent"
	KindAuthRevoked     ErrorKind = "auth_revoked"
	KindQuotaPeriodic   ErrorKind = "quota_periodic"
	KindQuotaBalance    ErrorKind = "quota_balance"
	KindQuotaPermanent  ErrorKind = "quota_permanent"
	KindModelNotFound   ErrorKind = "model_not_found"
	KindStreamTimeout   ErrorKind = "stream_timeout"
)

var modelNotFoundRe = regexp.MustCompile(
	`(?i)(model.{0,40}(does not exist|not found|is unknown|unknown model|not available)|`+
		`(no such|unknown) model|`+
		`endpoint.{0,40}(does not exist|not found)|`+
		`(does not|doesn'?t) support (coding plan|tool|function|tools|function call)|`+
		`(tool|function)[- _]?call(ing|s)? (is )?not supported|`+
		`unsupported (parameter|model|feature).{0,20}(tools?|function|tool_choice)|`+
		`model.{0,40}(deprecated|retired|sunset))`,
)
var modelNotFoundCJKRe = regexp.MustCompile(
	`模型不存在|模型.{0,10}不存在|模型.{0,10}未找到|当前模型不支持`,
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
	`(?i)(concurrent.{0,30}(limit|exceed|over|too many|reach|max)|`+
		`too many (concurrent|requests|connections)|`+
		`(engine|server|service|api) (overloaded|too busy|busy)|`+
		`(server|service|upstream) (is )?(overload|under pressure)|`+
		`(rpm|tpm).{0,20}(limit|exceed|reach|over)|`+
		`request(ed|s)? too (fast|frequent|many)|`+
		`slow down|try again later|backoff)`,
)
var concurrentOverloadCJKRe = regexp.MustCompile(
	`并发.{0,15}(超限|过大|过高|达到上限|超过限制)|`+
		`请求.{0,10}(过快|频繁|太多)|`+
		`服务.{0,10}(繁忙|过载|压力|降级)|`+
		`稍后重试|限流`,
)

// eofWithoutDoneRe is a Go-level signal: the SSE stream closed before
// the [DONE] sentinel. When combined with provider-known overload (or
// repeated across the same credential) this is treated as concurrent
// overload — upstream silently dropping connections under load.
var eofWithoutDoneRe = regexp.MustCompile(
	`(?i)(eof without|eof_without_done|stream closed before|premature close|unexpected eof)`,
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
			// EOF without [DONE] in the error message is the SSE
			// closure-before-completion signal. Providers like MiniMax
			// use this instead of 5xx when they're overloaded; treat
			// it as concurrent overload so the credential gets the
			// longer 5-minute cooling.
			return KindConcurrent
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
		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		if modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body) {
			return KindModelNotFound
		}
	}
	return ClassifyResponseStatus(&http.Response{StatusCode: status})
}

// ClassifyResponseBody inspects an error body fragment (e.g. SSE error
// chunk) for concurrent-overload signals. Returns KindConcurrent when
// the body indicates the upstream is overloaded, otherwise "" (caller
// should fall through to other classification).
func ClassifyResponseBody(body []byte) ErrorKind {
	if len(body) > 0 {
		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		if modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body) {
			return KindModelNotFound
		}
		if eofWithoutDoneRe.Match(body) {
			return KindStreamTimeout
		}
	}
	return ""
}

// IsConcurrentOverload returns true when an SSE stream closed prematurely
// (EOF before [DONE]) under conditions typical of upstream concurrent
// overload. Used by the executor to escalate eof_without_done errors to
// KindConcurrent so the credential gets the longer 5-minute cooling.
func IsConcurrentOverload(reason string) bool {
	if reason == "" {
		return false
	}
	return concurrentOverloadRe.MatchString(reason) || concurrentOverloadCJKRe.MatchString(reason) ||
		eofWithoutDoneRe.MatchString(reason)
}

func IsModelNotFound(kind ErrorKind) bool {
	return kind == KindModelNotFound
}

func IsRetryable(kind ErrorKind) bool {
	switch kind {
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindConcurrent, KindStreamTimeout:
		return true
	default:
		return false
	}
}

func IsCredentialFatal(kind ErrorKind) bool {
	switch kind {
	case KindAuth, KindAuthRevoked, KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent:
		return true
	default:
		return false
	}
}
