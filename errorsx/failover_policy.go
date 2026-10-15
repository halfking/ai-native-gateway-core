package errorsx

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FailoverScope describes how much of the candidate pool an upstream failure
// should remove synchronously from the current request.
type FailoverScope string

const (
	ScopeNone       FailoverScope = "none"
	ScopeModel      FailoverScope = "credential_model"
	ScopeCredential FailoverScope = "credential"
)

// FailoverDecision is the side-effect-free result of classifying one upstream
// response. State writers and probe queues consume this contract separately.
type FailoverDecision struct {
	Kind         ErrorKind
	Scope        FailoverScope
	Fuse         bool
	Permanent    bool
	RetryAfter   time.Duration
	EnqueueProbe bool
	ProbeFanout  int
	FrontendWait time.Duration
	ReasonCode   string
}

const (
	DefaultRateLimitCooldown  = 30 * time.Second
	DefaultTransientCooldown  = 2 * time.Second
	DefaultOverloadRetryDelay = 5 * time.Second
	DefaultFrontendWait       = 100 * time.Millisecond
	DefaultProbeFanout        = 3
)

// DecideFailover maps an upstream response to synchronous routing actions.
// clientOrigin must be false for provider responses; a client-originated 401
// must never eject an account from the shared pool.
func DecideFailover(status int, body []byte, retryAfterHeader string, clientOrigin bool) FailoverDecision {
	kind := ClassifyErrorWithBody(status, body)
	decision := FailoverDecision{
		Kind:         kind,
		Scope:        ScopeNone,
		RetryAfter:   DefaultTransientCooldown,
		FrontendWait: DefaultFrontendWait,
		ReasonCode:   string(kind),
	}
	if clientOrigin {
		decision.Kind = KindAuth
		decision.ReasonCode = "client_auth_failure"
		return decision
	}

	// P0-C：Scope / Fuse / Permanent 一律来自 PenaltyOf（归责矩阵 SSOT）。
	// EnqueueProbe / RetryAfter / ReasonCode 仍按 kind 细分（探针策略与归责正交）。
	pen := PenaltyOf(kind)
	decision.Scope = FailoverScopeOf(pen)
	decision.Fuse = pen.Fuse
	decision.Permanent = pen.Permanent

	switch kind {
	case KindAuth, KindAuthRevoked:
		decision.EnqueueProbe = false
		decision.ReasonCode = "credential_auth_failed"
	case KindQuotaPermanent, KindQuotaPeriodic, KindQuotaBalance, KindQuota:
		decision.EnqueueProbe = false
		decision.ReasonCode = "credential_quota_exhausted"
	case KindRateLimit:
		decision.RetryAfter = parseRetryAfter(retryAfterHeader, DefaultRateLimitCooldown)
		decision.EnqueueProbe = true
		decision.ProbeFanout = 1
		decision.ReasonCode = "credential_rate_limited"
	case KindConcurrent:
		decision.RetryAfter = 5 * time.Second
		decision.EnqueueProbe = true
		decision.ProbeFanout = DefaultProbeFanout
		decision.ReasonCode = "provider_concurrent_overload"
	case KindUpstreamOverloaded:
		decision.RetryAfter = parseRetryAfter(retryAfterHeader, DefaultOverloadRetryDelay)
		decision.EnqueueProbe = true
		decision.ProbeFanout = 1
		decision.ReasonCode = "provider_upstream_overloaded"
	case KindNoAvailableChannel:
		decision.RetryAfter = 15 * time.Minute
		decision.EnqueueProbe = true
		decision.ProbeFanout = 1
		decision.FrontendWait = 0
		decision.ReasonCode = "no_available_channel"
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindStreamTimeout:
		decision.EnqueueProbe = true
		decision.ProbeFanout = DefaultProbeFanout
		decision.ReasonCode = "upstream_transient_failure"
	case KindModelNotFound, KindModelDeprecated, KindUnsupportedFeature, KindContextLength, KindContentFilter, KindToolCallIdMismatch, KindClientBug:
		// P0-C：client_bug / shape 族 PenaltyNone → ScopeNone（不再 ScopeModel）。
		// 探针仍不入队（同 2026-09-18 flap 修复）。
		decision.EnqueueProbe = false
		decision.FrontendWait = 0
		decision.ReasonCode = "model_or_request_not_eligible"
	default:
		if pen.Target == PenaltyNone {
			decision.EnqueueProbe = false
			decision.FrontendWait = 0
		} else {
			decision.EnqueueProbe = true
			decision.ProbeFanout = DefaultProbeFanout
		}
	}
	return decision
}

func parseRetryAfter(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// IsUpstreamAuthFailure keeps the provider/client boundary explicit for callers
// that receive a net/http response before the body classifier runs.
func IsUpstreamAuthFailure(status int, clientOrigin bool) bool {
	return !clientOrigin && (status == http.StatusUnauthorized || status == http.StatusForbidden)
}
