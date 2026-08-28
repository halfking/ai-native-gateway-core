package errorsx

import "time"

// AutomaticProbeScope determines whether a scheduled recovery check targets a
// single credential-model binding or a credential-wide state such as quota.
type AutomaticProbeScope string

const (
	AutomaticProbeNone       AutomaticProbeScope = "none"
	AutomaticProbeModel      AutomaticProbeScope = "credential_model"
	AutomaticProbeCredential AutomaticProbeScope = "credential"
)

// AutomaticProbePolicy is the common decision used by automatic audit and
// recovery schedulers. Manual diagnostic probes deliberately do not use it.
type AutomaticProbePolicy struct {
	Enabled  bool
	Scope    AutomaticProbeScope
	Interval time.Duration
	Fixed    bool
}

// AutomaticProbePolicyFor returns fail-closed scheduling behavior. The normal
// transient retry chain is owned by node probe state; Interval is its first
// scheduled hop or a fixed quota recovery cadence.
func AutomaticProbePolicyFor(kind ErrorKind) AutomaticProbePolicy {
	switch kind {
	case KindQuotaPeriodic:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeCredential, Interval: 5 * time.Minute, Fixed: true}
	case KindQuotaBalance, KindQuotaPermanent, KindQuota:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeCredential, Interval: 2 * time.Minute, Fixed: true}
	case KindRateLimit:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeModel, Interval: 3 * time.Minute}
	case KindConcurrent:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeModel, Interval: 5 * time.Minute}
	case KindNoAvailableChannel:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeModel, Interval: 15 * time.Minute}
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindStreamTimeout,
		KindUpstreamOverloaded, KindEmptyResponse, KindUpstreamContextLoss:
		return AutomaticProbePolicy{Enabled: true, Scope: AutomaticProbeModel, Interval: 5 * time.Second}
	default:
		return AutomaticProbePolicy{Scope: AutomaticProbeNone}
	}
}

func IsAutomaticProbeEligible(kind ErrorKind) bool { return AutomaticProbePolicyFor(kind).Enabled }
