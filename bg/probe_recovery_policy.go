package bg

import (
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
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
//
// 2026-09-26 (P0-2 短梯长尾化): the network-class short chain used to cap at
// 60s forever — a persistently-down upstream was re-probed once a minute
// indefinitely (2026-09-25 实测：60s 封顶档滞留 45 对，last_err_code 全部为
// connection_error/503/timeout/500). With probe.network_chain_long_tail
// (default on) the ladder continues on the generic chain's long tail after
// the four short rungs: 5s→15s→30s→60s→5m→1h→2h→6h（封顶）. The first four
// rungs are untouched, so sub-hour blip recovery discovery keeps its speed;
// only sustained-outage pairs sink to the 6h cadence auth/404 already use.
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
		return networkProbeBackoff(attempt)
	}
	delay := ChainBackoffIndex(attempt, NodeProbeBackoffChain)
	if policy.Enabled && policy.Interval > delay {
		return policy.Interval
	}
	return delay
}

// networkProbeBackoff paces the network/transient class. Long tail on:
// attempts 1..4 walk the short chain (5s/15s/30s/60s), attempts beyond it
// continue on the generic chain's tail rungs (5m/1h/2h/6h, capped). Long
// tail off: the legacy short chain capped at 60s forever.
func networkProbeBackoff(attempt int) time.Duration {
	if settings.GetPlatformBool("probe.network_chain_long_tail", true) && attempt > len(NetworkProbeBackoffChain) {
		tail := NodeProbeBackoffChain[3:] // 5m/1h/2h/6h — generic chain minus its own 5s/30s/60s rungs
		return ChainBackoffIndex(attempt-len(NetworkProbeBackoffChain), tail)
	}
	return ChainBackoffIndex(attempt, NetworkProbeBackoffChain)
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
