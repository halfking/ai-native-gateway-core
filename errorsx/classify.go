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
)

var modelNotFoundRe = regexp.MustCompile(
	`(?i)(model.{0,40}(does not exist|not found|is unknown|unknown model|not available)|` +
		`(no such|unknown) model|` +
		`endpoint.{0,40}(does not exist|not found)|` +
		`(does not|doesn'?t) support (coding plan|tool|function|tools|function call)|` +
		`(tool|function)[- _]?call(ing|s)? (is )?not supported|` +
		`unsupported (parameter|model|feature).{0,20}(tools?|function|tool_choice)|` +
		`model.{0,40}(deprecated|retired|sunset))`,
)

func ClassifyError(err error, resp *http.Response) ErrorKind {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return KindCanceled
		}
		msg := err.Error()
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
	switch {
	case resp.StatusCode == 429:
		return KindRateLimit
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

func ClassifyResponseBody(body []byte) ErrorKind {
	if len(body) > 0 && modelNotFoundRe.Match(body) {
		return KindModelNotFound
	}
	return ""
}

func IsModelNotFound(kind ErrorKind) bool {
	return kind == KindModelNotFound
}

func IsRetryable(kind ErrorKind) bool {
	switch kind {
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindRateLimit, KindConcurrent:
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
