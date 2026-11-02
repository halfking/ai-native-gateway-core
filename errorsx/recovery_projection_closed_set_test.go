package errorsx

import "testing"

// ─────────────────────────────────────────────────────────────────────────
// 本文件是 2026-11-02 **第八轮（F-1 修）**的落地门：**处置 ⇄ 恢复投影 的
// 闭集门**（行为级，不靠人手对表）。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 同一个 ErrorKind 在仓里有**三个**闭集要登记：
//
//	① 处置策略    errorsx/action_policy.go        ⇒ ActionSwitchNode /
//	               domains/streaming/attempt_outcome.go ⇒ TaskActionRetryNow
//	② 恢复投影    errorsx/recovery_projection.go   ⇒ CandidateFailover /
//	               GenericRetryable ⇒ EffectiveRetryable（**客户端信封的
//	               `retryable` 标志** + candidate_failure_logs.retryable）
//	③ 归责        errorsx/penalty.go / nodehealth / credential writer
//
// ★ 2026-11-02 的 F-1 就是一个**跨闭集漏登记**：第一轮新增
//   `KindModelUnavailable`（裸 404 = 绑定级）时，
//   ① 登记了（action_policy ⇒ ActionSwitchNode；attempt_outcome ⇒
//   TaskActionRetryNow），但 ② **没登记** ⇒ 恢复投影说
//   `terminal` / `not_recoverable` ⇒ 客户端拿到 `retryable:false`，
//   而网关自己**正在**换兄弟候选把它救回来 ⇒
//   · 运维读 `candidate_failure_logs`（本审计档 §六 取证就读这张表）会以为
//     「这个模型没人服务」，而实际上那次尝试**被故障转移救回来了**；
//   · agent 客户端看到 `retryable:false` 就放弃这一轮，而这一对
//     (凭据,模型) 是**绑定级**、探针阶梯会自愈的。
//
// ── 这道门断言什么 ────────────────────────────────────────────────────
//
//	门 A（闭集 · 正向）：凡是策略动作是「换一个兄弟候选」（ActionSwitchNode）
//	  且**不是终态**的 kind，必须让客户端看见「可重试」
//	  （EffectiveRetryable == true）——除非它在**显式登记的排除集**里，
//	  且排除必须写明理由。
//	  ★ 「显式登记的排除」是这道门的关键：像 `request_rejected`（请求侧
//	    语义错误 ⇒ 重发同一请求必然再败）与 `auth`（凭据级不可用 ⇒ 换的是
//	    凭据不是请求）这两个 kind，网关**确实**会换兄弟候选，但客户端重试
//	    无用 ⇒ 它们被**有意**排除。登记成排除 = 变成「必须显式承认的决定」，
//	    而不是「碰巧没登记」。
//
//	门 B（闭集 · 反向）：凡是让客户端看见「可重试」的 kind，策略动作必须
//	  落在「可恢复」那一族（ActionSwitchNode / ActionWaitRecovery /
//	  ActionRetrySameNode）⇒ 防止有人把某个 kind 随手改成可重试。
//
//	门 C（恒绿护栏）：排除集与可重试集**都必须非空**，且排除集里每个 kind
//	  都**必须带理由** ⇒ 门不能靠「把排除集清空」蒙混过关。
//
// ── 量具口径 ───────────────────────────────────────────────────────────
//
// 用 `DecideNextAction` **驱动真实策略**取每个 kind 的动作，而不是抄一张
// 人手维护的表 ⇒ 新增 kind 时这道门自动覆盖，不需要改表。
// 上下文取「有兄弟候选 + 还有尝试预算 + 同节点重试预算为 0」⇒ 走到
// kind switch 里的最终动作。

// recoveryProjectionExcludedKinds 是**显式登记的排除集**：这些 kind 的
// 策略动作是「换一个兄弟候选」，但**客户端重试无用**，所以不告诉客户端
// 「可重试」。
//
// ★ 每个条目**必须**写明理由——这是「有意排除」与「忘了登记」的分界线。
var recoveryProjectionExcludedKinds = map[ErrorKind]string{
	// 请求侧语义错误（400/413/422）：重发**同一个请求**必然再败 ⇒
	// 客户端重试无用（网关自己已经换过兄弟候选了）。
	KindRequestRejected: "请求侧语义错误：重发同一请求必然再败 ⇒ 客户端重试无用",
	// 凭据级不可用（401/403）：网关换的是**凭据**不是请求；重试只会把
	// 无效的 key 再打一遍 ⇒ 客户端重试无用（该修的是凭据）。
	KindAuth: "凭据级不可用：网关换的是凭据不是请求 ⇒ 客户端重试只会重打无效 key",
}

// policyActionForKind 驱动真实策略，返回该 kind 在「有兄弟候选 + 还有
// 尝试预算」下的处置动作。
func policyActionForKind(kind ErrorKind) NextAction {
	return DecideNextAction(DecisionContext{
		Kind:               kind,
		RemainingAttempts:  1,
		HasAlternateNode:   true,
		MaxSameNodeRetries: 0,
	}).Action
}

// TestRecoveryProjectionClosedSet_SwitchNodeKindsMustBeClientRetryable 是
// 门 A：凡是「网关会换一个兄弟候选」且非终态的 kind，必须落在
// {可重试} ∪ {显式排除} 之内。
func TestRecoveryProjectionClosedSet_SwitchNodeKindsMustBeClientRetryable(t *testing.T) {
	for _, kind := range AllErrorKinds() {
		if terminalActionKind(kind) {
			continue // 终态：网关不换候选，客户端也不该重试
		}
		if policyActionForKind(kind) != ActionSwitchNode {
			continue
		}
		if EffectiveRetryable(kind) {
			continue
		}
		if reason, excluded := recoveryProjectionExcludedKinds[kind]; excluded {
			if reason == "" {
				t.Errorf("%s 在排除集里但**没写理由** ⇒ 排除必须是显式承认的决定", kind)
			}
			continue
		}
		t.Errorf("%s：策略动作是 ActionSwitchNode（网关会换一个兄弟候选），但 EffectiveRetryable=false ⇒ "+
			"客户端会拿到 retryable:false，而网关自己正在换候选把它救回来。\n"+
			"  ⇒ 要么把它登记进 ProjectRecovery 的候选故障转移分支（客户端可重试），\n"+
			"     要么把它写进 recoveryProjectionExcludedKinds 并写明「客户端重试无用」的理由。",
			kind)
	}
}

// TestRecoveryProjectionClosedSet_ClientRetryableMustBeRecoverableAction
// 是门 B：凡是让客户端看见「可重试」的 kind，策略动作必须落在可恢复族。
func TestRecoveryProjectionClosedSet_ClientRetryableMustBeRecoverableAction(t *testing.T) {
	recoverable := map[NextAction]bool{
		ActionSwitchNode:    true,
		ActionWaitRecovery:  true,
		ActionRetrySameNode: true,
	}
	for _, kind := range AllErrorKinds() {
		if !EffectiveRetryable(kind) {
			continue
		}
		action := policyActionForKind(kind)
		if !recoverable[action] {
			t.Errorf("%s：EffectiveRetryable=true（客户端被告知可重试），但策略动作是 %q（不在可恢复族 %v 里）⇒ "+
				"两处口径矛盾", kind, action, recoverable)
		}
	}
}

// TestRecoveryProjectionClosedSet_ExclusionSetIsExplicit 是门 C：排除集
// 必须非空、每条必须带理由、且**不得**包含本该可重试的 kind。
func TestRecoveryProjectionClosedSet_ExclusionSetIsExplicit(t *testing.T) {
	if len(recoveryProjectionExcludedKinds) == 0 {
		t.Fatal("排除集是空的 ⇒ 这道门会退化成「什么都能排除」⇒ 必须显式登记")
	}
	for kind, reason := range recoveryProjectionExcludedKinds {
		if reason == "" {
			t.Errorf("%s 在排除集里但没写理由", kind)
		}
		if EffectiveRetryable(kind) {
			t.Errorf("%s 同时在排除集里、又被告知客户端可重试 ⇒ 两个口径互相矛盾", kind)
		}
		if terminalActionKind(kind) {
			t.Errorf("%s 是**终态** kind ⇒ 它根本不会走到 ActionSwitchNode ⇒ 放进排除集没有意义（排除集只登记「网关会换候选但客户端不该重试」的 kind）", kind)
		}
	}
}

// TestRecoveryProjectionClosedSet_HasTeeth 是**门有牙**的自检：把
// `KindModelUnavailable` 从候选故障转移分支里摘掉（模拟「第一轮只登记了
// 处置闭集、忘了登记恢复闭集」），门 A 必须红。
//
// ★ 为什么要有这条：门 A 断言的是「所有 switch_node kind 都被覆盖」，
// 而排除集允许人为放行 ⇒ 没有这条自检，门 A 可能因为「把 kind 加进排除集」
// 而一直绿 ⇒ 门就失去了牙齿。
func TestRecoveryProjectionClosedSet_HasTeeth(t *testing.T) {
	// 断言前提成立：model_unavailable 确实是「网关会换兄弟候选」的 kind。
	if policyActionForKind(KindModelUnavailable) != ActionSwitchNode {
		t.Fatalf("前提不成立：KindModelUnavailable 的策略动作是 %q，不是 ActionSwitchNode ⇒ 本用例失去意义",
			policyActionForKind(KindModelUnavailable))
	}
	// 它**必须**落在可重试一侧（而不是被排除集放行）。
	if _, excluded := recoveryProjectionExcludedKinds[KindModelUnavailable]; excluded {
		t.Fatal("KindModelUnavailable 被登记进排除集 ⇒ 它必须是可重试一侧（404 = 绑定级 ⇒ 客户端重试有意义）")
	}
	if !EffectiveRetryable(KindModelUnavailable) {
		t.Fatal("KindModelUnavailable 的 EffectiveRetryable=false ⇒ 正是 F-1 那个缺陷本身")
	}
	// 恒绿护栏：可重试一侧必须**非空**，否则「什么都没登记」也会绿。
	if !EffectiveRetryable(KindEmptyResponse) || !EffectiveRetryable(KindNoAvailableChannel) {
		t.Fatal("既有的两个候选故障转移 kind 失效 ⇒ 门的前提被破坏")
	}
}
