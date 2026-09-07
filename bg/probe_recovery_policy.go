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
	return ProbeBackoffForKind(classifyProbeErrCode(errCode), attempt)
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
