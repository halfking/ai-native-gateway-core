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
	case KindRateLimitObserved:
		// ★ 2026-11-10（审计 F50）：本 kind 是「上游**说了**限流」，不是
		// 「这个**节点**限流了」⇒ **不扇出**自动探针。
		//
		// ★ 为什么要**显式**写这一行（而不是靠 default 兜底）：本文件的
		//   default 分支返回 AutomaticProbeNone，所以今天不写也安全；但
		//   **同一张表**里 KindRateLimit 就在上一行、带着「3 分钟 +
		//   模型粒度」的探针 ⇒ 只靠 default 意味着「新 kind 继承了
		//   限流族的探针语义」这件事**没有任何证据可查**。
		//   ⇒ 显式登记成「不探针」，把「不惩罚节点」这条决定钉在表上。
		//
		// ★ 归责见 errorsx.PenaltyOf：PenaltyNone ⇒ 绑定不翻红 ⇒
		//   探针阶梯**没有**任何东西需要探 ⇒ 扇出探针只会白打上游。
		return AutomaticProbePolicy{Scope: AutomaticProbeNone}
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
