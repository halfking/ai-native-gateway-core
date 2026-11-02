package errorsx

// RecoveryProjection is the low-coupling, log-oriented view of recovery
// semantics for an error kind. GenericRetryable deliberately mirrors
// IsRetryable; CandidateFailover describes the separate candidate-loop path.
// The projection does not perform retries, failover, or state changes.
type RecoveryProjection struct {
	GenericRetryable  bool   `json:"generic_retryable"`
	CandidateFailover bool   `json:"candidate_failover"`
	TransparentResume bool   `json:"transparent_resume"`
	EffectiveAction   string `json:"effective_action"`
	Reason            string `json:"reason"`
}

const (
	RecoveryActionCandidateFailover = "candidate_failover"
	RecoveryActionGenericRetry      = "generic_retry"
	RecoveryActionTerminal          = "terminal"
)

// ProjectRecovery returns the recovery semantics that callers should attach to
// logs and observations. In particular, empty_response and
// no_available_channel are intentionally candidate-loop failovers, not members
// of the generic IsRetryable set. This keeps IsRetryable's existing contract
// unchanged while making the effective behavior observable.
//
// ★ 2026-11-02（第八轮 · F-1）：本函数此前**漏登记**了 `KindModelUnavailable`
// ⇒ 同一个 kind 在**两个闭集**里互相矛盾：
// · `action_policy.go` 的 kind switch 把它判成 `ActionSwitchNode`（换一个兄弟候选）；
// · `domains/streaming/attempt_outcome.go` 把它判成 `TaskActionRetryNow`（候选走查可恢复）;
// · 而本函数不认它 ⇒ `EffectiveAction="terminal"` / `Reason="not_recoverable"`
// ⇒ ⇒ `EffectiveRetryable=false` ⇒ **客户端错误信封里的 `retryable:false`**
// （`domains/streaming/handler.go` 的 5629 / 5871 / 5888 / 5942 四处）与
// `candidate_failure_logs.retryable=false` + context 里的
// `effective_action:"terminal" / reason:"not_recoverable"`。
// ⇒ 后果：①运维读 `candidate_failure_logs`（本审计档 §六 取证就读这张表）
// 会以为「这个模型没人服务」，而实际上那次尝试**被故障转移救回来了**
// （网关换兄弟凭据后成功）；②agent 客户端看到 `retryable:false` 就放弃
// 这一轮，而**这一对 (凭据,模型) 是绑定级、探针阶梯会自愈的**。
//
// ★ 口径依据（为什么它是「可重试」，而 KindRequestRejected 不是）：
// **404 = 绑定级证据，不是请求侧证据。** 它排除「请求本身不合法」
// （两种读法「这个账号里没有这个模型」/「这个凭据的端点路径配错了」都
// 指向这一对）⇒ 客户端**重发同一个请求是有意义的**：
// · 兄弟凭据可能服务同一个模型（211 实测：943 个在册模型里 366 个由 ≥2 个
// 凭据服务、198 个由 ≥4 个服务）⇒ 跨凭据故障转移有实质价值；
// · 绑定级冷却之后，这一对会被候选查询排除 ⇒ 下一次尝试落
// `KindNoAvailableChannel`（**本来就**是 candidate_failover ⇒ 客户端
// 本来就会被告知可重试）⇒ 两者的客户端口径必须一致。
//
// ★ 而 `KindRequestRejected`（400/413/422）**故意不**进这个分支：它是
// **请求侧**语义错误 ⇒ 重发**同一个请求**必然再败 ⇒ 客户端重试无用
// （网关自己已经换过兄弟候选了）。同理 `KindAuth` 也不进：凭据级不可用
// 换的是**凭据**不是请求，重试只会把无效 key 再打一遍。
// 这两个是**显式登记的排除**，不是遗漏 ⇒ 见
// errorsx/recovery_projection_closed_set_test.go 的闭集门。
func ProjectRecovery(kind ErrorKind) RecoveryProjection {
	projection := RecoveryProjection{
		GenericRetryable: IsRetryable(kind),
		EffectiveAction:  RecoveryActionTerminal,
		Reason:           "not_recoverable",
	}

	if kind == KindEmptyResponse || kind == KindNoAvailableChannel || kind == KindModelUnavailable {
		projection.CandidateFailover = true
		projection.TransparentResume = true
		projection.EffectiveAction = RecoveryActionCandidateFailover
		if kind == KindEmptyResponse {
			projection.Reason = "empty_response_candidate_failover"
		} else if kind == KindNoAvailableChannel {
			projection.Reason = "no_available_channel_candidate_failover"
		} else {
			projection.Reason = "model_unavailable_candidate_failover"
		}
		return projection
	}

	// 2026-09-01 (P1-1 24h-audit round2): circuit_open / fp_slot_saturated are
	// gateway-side admission signals logged before any upstream call. They are
	// not upstream quality problems: retrying the upstream from the generic
	// retry loop would bypass the breaker / slot that just rejected the
	// attempt. Project as terminal with an explicit reason so
	// candidate_failure_logs context says "gateway admission" instead of the
	// misleading default "not_recoverable" (which implies an upstream verdict).
	if kind == KindCircuitOpen || kind == KindFpSlotSaturated {
		projection.EffectiveAction = RecoveryActionTerminal
		projection.Reason = "gateway_admission_rejection"
		return projection
	}

	if projection.GenericRetryable {
		projection.TransparentResume = true
		projection.EffectiveAction = RecoveryActionGenericRetry
		projection.Reason = "generic_retryable_upstream_failure"
	}
	return projection
}

// EffectiveRetryable reports whether the gateway should still recover this
// kind — either by the generic retry loop or by walking the next candidate.
// Admin/client retryable flags must use this, not IsRetryable alone:
// empty_response is outside IsRetryable by design but is a candidate failover.
//
// ★ 2026-11-02（第八轮 · F-1）：同理适用于 `model_unavailable`（404 =
// 绑定级证据 ⇒ 换兄弟候选，且这一对会被探针阶梯自愈 ⇒ 客户端重试有意义），
// 与 `no_available_channel`。**不是** `request_rejected` / `auth`
// （那两个是请求侧 / 凭据级不可用 ⇒ 客户端重试无用 ⇒ 显式登记的排除）。
func EffectiveRetryable(kind ErrorKind) bool {
	p := ProjectRecovery(kind)
	return p.GenericRetryable || p.CandidateFailover
}
