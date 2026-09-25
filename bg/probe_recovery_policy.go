package bg

import (
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// NetworkProbeBackoffChain is the short retry ladder for transient, timeout,
// and network-class failures. The long 1h/2h/6h tail left those nodes red
// long after the upstream recovered.
var NetworkProbeBackoffChain = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// ProbeBackoffForKind returns the next retry delay for an automatic probe.
// Quota uses the fixed policy cadence; transport errors stay on the short
// chain; everything else keeps the historical 7-step ladder, with the
// policy interval (3m rate-limit / 5m concurrent / 15m no-channel) acting
// as a floor so a 429 is never re-probed after 5s.
func ProbeBackoffForKind(kind errorsx.ErrorKind, attempt int) time.Duration {
	policy := errorsx.AutomaticProbePolicyFor(kind)
	if policy.Fixed && policy.Interval > 0 {
		return policy.Interval
	}
	switch kind {
	case errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindUpstreamDown, errorsx.KindStreamTimeout,
		errorsx.KindUpstreamOverloaded, errorsx.KindEmptyResponse,
		errorsx.KindUpstreamContextLoss:
		return ChainBackoffIndex(attempt, NetworkProbeBackoffChain)
	}
	delay := ChainBackoffIndex(attempt, NodeProbeBackoffChain)
	if policy.Enabled && policy.Interval > delay {
		return policy.Interval
	}
	return delay
}

// ProbeBackoffForErrCode maps a probe/audit error code onto ProbeBackoffForKind.
func ProbeBackoffForErrCode(errCode string, attempt int) time.Duration {
	// 2026-09-17: a 404 the DIRECT round returned twice is the upstream's
	// "this credential does not serve this model" verdict. Re-probing it on
	// the ordinary ladder (5s..6h) made every request_failure trigger walk the
	// whole chain again — the eternal-churn shape seen on apigpt (14 models ×
	// 7 attempts/24h). Park the pair at the model-not-served re-check horizon
	// instead; the first (unconfirmed) 404 keeps the generic ladder so a
	// one-off aggregator blip still re-verifies quickly.
	if isModelNotServedProbeError(errCode) && attempt >= 2 {
		return modelNotServedRecheckInterval
	}
	return ProbeBackoffForKind(classifyProbeErrCode(errCode), attempt)
}

// probeBackoffForDirectOutcome is the root-cause-aware wrapper around
// ProbeBackoffForErrCode (2026-09-25, "错误的探测要搞清楚是协议问题还是节点的
// 问题"): a PROTOCOL-shaped failure is a probe-contract mismatch (wrong
// protocol path / body shape / catalog entry against this provider). Re-probing
// cannot heal it — only a config fix can — so from the second attempt on it
// parks at the same long horizon as model-not-served instead of walking the
// generic ladder. The first attempt keeps the generic ladder: a one-off
// aggregator blip can 400/422 once and must still re-verify quickly.
// Node- and gateway-caused failures keep the existing err-code policy
// (gateway-side is separately overridden to the fixed 15m delay by callers).
func probeBackoffForDirectOutcome(errCode string, rootCause ProbeRootCause, attempt int) time.Duration {
	if rootCause == ProbeRootCauseProtocol && attempt >= 2 {
		return modelNotServedRecheckInterval
	}
	return ProbeBackoffForErrCode(errCode, attempt)
}

func classifyProbeErrCode(code string) errorsx.ErrorKind {
	c := strings.ToLower(strings.TrimSpace(code))
	switch c {
	case "timeout", "stream_timeout":
		return errorsx.KindTimeout
	case "network_error", "network", "dns_error", "connection_error", "http_000":
		return errorsx.KindNetwork
	case "quota_periodic", "periodic_exhausted":
		return errorsx.KindQuotaPeriodic
	case "quota_balance", "balance_exhausted":
		return errorsx.KindQuotaBalance
	case "quota_permanent", "permanently_exhausted":
		return errorsx.KindQuotaPermanent
	case "quota":
		return errorsx.KindQuota
	case "rate_limit", "http_429":
		return errorsx.KindRateLimit
	case "upstream_down":
		return errorsx.KindUpstreamDown
	case "empty_response":
		return errorsx.KindEmptyResponse
	}
	if strings.HasPrefix(c, "http_5") {
		return errorsx.KindUpstreamDown
	}
	if strings.HasPrefix(c, "http_0") {
		return errorsx.KindNetwork
	}
	// Unknown / auth / pin-unsupported codes: no automatic policy, plain
	// generic ladder. Returning an empty kind keeps the caller honest — it
	// is not "auth", we simply have no transport evidence.
	return ""
}
