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

// recoveryProjectionExclusion 是排除登记表的一个条目。
type recoveryProjectionExclusion struct {
	kind   ErrorKind
	reason string
}

// recoveryProjectionExcludedKinds 是**显式登记的排除集**：这些 kind 的
// 策略动作是「换一个兄弟候选」（网关确实会换），但**客户端重试无用**，
// 所以不告诉客户端「可重试」。
//
// ★ 2026-11-02（第十二轮）：这张表此前**只存在于测试文件里**
//
//	（recovery_projection_closed_set_test.go 的一个 map）⇒
//	①生产代码里**没有 SSOT** —— 下一个复核者读 `ProjectRecovery` 只能看到
//	正向的候选故障转移分支，看不到「哪些 kind 被有意排除、为什么」；
//	②往那张 map 里加一行 + 一句非空理由，就能对**任意** kind 放行
//	⇒ F-1 那一类缺陷（处置说「换兄弟候选」、恢复投影说「不可恢复」）
//	可以再次静默通过。
//	⇒ 现在搬进生产代码，并把「排除资格」做成**生产侧判据**
//	（recoveryProjectionExclusionEligible）⇒ 登记表**不能**放行一个
//	不符合资格的 kind。
//
// ★ 每条**必须**写明理由 —— 这是「有意排除」与「忘了登记」的分界线。
var recoveryProjectionExcludedKinds = []recoveryProjectionExclusion{
	{
		// 请求侧语义错误（400/413/422）：重发**同一个请求**必然再败 ⇒
		// 客户端重试无用（网关自己已经换过兄弟候选了）。
		kind:   KindRequestRejected,
		reason: "请求侧语义错误：重发同一请求必然再败 ⇒ 客户端重试无用",
	},
	{
		// 2026-11-10（审计 F50）：限流形态正文落在**请求侧状态族**
		// （400/404/413/414/422）上 ⇒ 与 request_rejected 同族。
		// 上游已经拒收了这次请求（状态码是它对本次请求的裁决）⇒ 重发
		// 同一个请求必然再败 ⇒ 客户端重试无用。
		//
		// ★ 排除资格：它是**请求侧**语义错误（IsRequestSideError = true）
		//   ⇒ 符合 recoveryProjectionExclusionEligible ⇒ 可以被排除。
		kind:   KindRateLimitObserved,
		reason: "请求侧拒绝（上游用 4xx 回了限流形态正文）：重发同一请求必然再败 ⇒ 客户端重试无用",
	},
	{
		// 凭据级不可用（401/403）：网关换的是**凭据**不是请求；重试只会把
		// 无效的 key 再打一遍 ⇒ 客户端重试无用（该修的是凭据）。
		kind:   KindAuth,
		reason: "凭据级不可用：网关换的是凭据不是请求 ⇒ 客户端重试只会重打无效 key",
	},
}

// recoveryProjectionExclusionEligible 是**排除资格**判据：只有
// ①**请求侧**语义错误（重发同一个请求必然再败）
// ②**凭据级**不可用（换的是凭据不是请求）
// 这两类 kind 才允许被排除在「客户端可重试」之外。
//
// ★ 这条判据把 F-1 那一类缺陷**结构性地**堵死：绑定级 kind
//
//	（model_unavailable / model_not_found / model_deprecated …）
//	既不是请求侧、也不是凭据级 ⇒ **不能**被排除
//	⇒ 客户端必须被告知「可重试」。
func recoveryProjectionExclusionEligible(kind ErrorKind) bool {
	if IsRequestSideError(kind) {
		return true
	}
	return PenaltyOf(kind).Target == PenaltyCredential
}

// recoveryProjectionExclusionReasonIn 在给定登记表里查该 kind，**并施加
// 排除资格判据**。
//
// ★ 抽成「登记表可传入」的纯函数，是为了让门禁能在**不改动全局状态**的
//
//	前提下证明「资格判据真的被施加了」（造一张含不合格条目的假表即可）。
func recoveryProjectionExclusionReasonIn(registry []recoveryProjectionExclusion, kind ErrorKind) (string, bool) {
	for _, entry := range registry {
		if entry.kind != kind {
			continue
		}
		if entry.reason == "" {
			return "", false
		}
		if !recoveryProjectionExclusionEligible(kind) {
			return "", false
		}
		return entry.reason, true
	}
	return "", false
}

// RecoveryProjectionExclusionReason 返回该 kind 是否被**显式排除**在
// 「客户端可重试」之外，以及排除的理由。
//
// ★ 排除**必须**同时满足两件事：①在登记表里；②符合排除资格
//
//	（recoveryProjectionExclusionEligible）⇒ **登记表本身不能**放行一个
//	不符合资格的 kind —— 哪怕它被写进了表里。
//	⇒ 这一条是 F-1 那类缺陷的结构性防线（不是靠测试表自律）。
func RecoveryProjectionExclusionReason(kind ErrorKind) (string, bool) {
	return recoveryProjectionExclusionReasonIn(recoveryProjectionExcludedKinds, kind)
}

// RecoveryProjectionExcludedKinds 返回登记表的**全部** kind（含不符合
// 排除资格的）⇒ 门禁据此逐条核对「资格 + 理由 + 行为」。
func RecoveryProjectionExcludedKinds() []ErrorKind {
	out := make([]ErrorKind, 0, len(recoveryProjectionExcludedKinds))
	for _, entry := range recoveryProjectionExcludedKinds {
		out = append(out, entry.kind)
	}
	return out
}

// ProjectRecovery returns the recovery semantics that callers should attach to
// logs and observations. In particular, empty_response and
// no_available_channel are intentionally candidate-loop failovers, not members
// of the generic IsRetryable set. This keeps IsRetryable's existing contract
// unchanged while making the effective behavior observable.
//
// ★ 2026-11-02（第十二轮）：**显式排除集**（recoveryProjectionExcludedKinds）
//
//	在这里**生效**，而不只是文档 ⇒ 这张表是 load-bearing 的 SSOT：
//	被登记的 kind **必须**投影成「终态 + 客户端不可重试」。
//	★ 两个现有条目（request_rejected / auth）本来就既不在候选故障转移分支、
//	  也不在 IsRetryable 集合里 ⇒ 这次改动**逐字节**不改变它们的投影。
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
// recoveryProjectionExcludedKinds 与 errorsx/recovery_projection_closed_set_test.go
// 的闭集门。
func ProjectRecovery(kind ErrorKind) RecoveryProjection {
	projection := RecoveryProjection{
		GenericRetryable: IsRetryable(kind),
		EffectiveAction:  RecoveryActionTerminal,
		Reason:           "not_recoverable",
	}

	// ★ 2026-11-02（第十二轮）：显式排除**优先**于任何分支 ⇒ 登记的 kind
	//   永远投影成「终态 + 客户端不可重试」，不会因为将来某个分支把它
	//   收进去而悄悄变成「可重试」。
	//   ★ `GenericRetryable` 必须**一并压成 false** —— 否则排除只是
	//   early-return：`EffectiveRetryable` = GenericRetryable || CandidateFailover
	//   ⇒ 只要 IsRetryable(kind) 为真，这个 kind 仍然会被告知「可重试」
	//   ⇒ 排除就**没有真的排除**（这是本轮写门禁时自己踩到的：变异臂
	//   证明 early-return 版本对当前两个条目是冗余的 ⇒ 排除分支形同虚设）。
	if _, excluded := RecoveryProjectionExclusionReason(kind); excluded {
		projection.GenericRetryable = false
		projection.CandidateFailover = false
		projection.TransparentResume = false
		projection.EffectiveAction = RecoveryActionTerminal
		projection.Reason = "not_recoverable"
		return projection
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
