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

// ★ 2026-11-02（第十二轮）：排除集**已搬进生产代码**
//   （errorsx/recovery_projection.go 的 recoveryProjectionExcludedKinds）
//   ⇒ 本文件**不再**持有自己的副本 ⇒ 门禁读的是**生产的那一份**，
//   且该表带**排除资格判据**（recoveryProjectionExclusionEligible）⇒
//   往表里加一行并不能放行一个不符合资格的 kind。

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
		if reason, excluded := RecoveryProjectionExclusionReason(kind); excluded {
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
// 必须非空、每条必须带理由、**必须符合排除资格**、且**不得**包含本该
// 可重试的 kind。
//
// ★ 2026-11-02（第十二轮）：排除集已搬进**生产代码**，本门读的是生产的
//
//	那一份（errorsx/recovery_projection.go 的 recoveryProjectionExcludedKinds）。
func TestRecoveryProjectionClosedSet_ExclusionSetIsExplicit(t *testing.T) {
	if len(recoveryProjectionExcludedKinds) == 0 {
		t.Fatal("排除集是空的 ⇒ 这道门会退化成「什么都能排除」⇒ 必须显式登记")
	}
	for _, kind := range RecoveryProjectionExcludedKinds() {
		reason, _ := recoveryProjectionExclusionReasonIn(recoveryProjectionExcludedKinds, kind)
		if reason == "" {
			t.Errorf("%s 在排除集里但没写理由", kind)
		}
		if EffectiveRetryable(kind) {
			t.Errorf("%s 同时在排除集里、又被告知客户端可重试 ⇒ 两个口径互相矛盾", kind)
		}
		if terminalActionKind(kind) {
			t.Errorf("%s 是**终态** kind ⇒ 它根本不会走到 ActionSwitchNode ⇒ 放进排除集没有意义（排除集只登记「网关会换候选但客户端不该重试」的 kind）", kind)
		}
		if !recoveryProjectionExclusionEligible(kind) {
			t.Errorf("%s 在排除集里，但**不符合排除资格**（既不是请求侧语义错误、也不是凭据级不可用）⇒ "+
				"排除资格判据只对这两类开放 ⇒ 绑定级 kind 被排除正是 F-1 那一类缺陷", kind)
		}
	}
}

// TestRecoveryProjectionClosedSet_EveryExclusionIsJustified 是**门 D**
// （第十二轮起改成**逐 kind**）：排除集里的**每一个**条目都必须通过
// 四道独立断言 ——
//
//	① 它确实落在「网关会换兄弟候选」那一族（否则「排除」没有对象）；
//	② 它有**非空理由**（排除必须是显式承认的决定）；
//	③ 它**符合排除资格**（请求侧语义错误 / 凭据级不可用）；
//	④ 它的**投影行为**确实是「终态 + 客户端不可重试」。
//
// ★ 为什么必须逐 kind：第八轮那版门 D 只查 `model_unavailable` 一个 kind
//
//	⇒ 其余每一个 kind 都可以「往排除集里加一行」蒙混过关。
func TestRecoveryProjectionClosedSet_EveryExclusionIsJustified(t *testing.T) {
	excluded := RecoveryProjectionExcludedKinds()
	if len(excluded) == 0 {
		t.Fatal("排除集是空的 ⇒ 逐 kind 断言失去对象")
	}
	for _, kind := range excluded {
		// ① 它是「网关会换兄弟候选」的 kind ⇒ 排除有对象。
		if policyActionForKind(kind) != ActionSwitchNode {
			t.Errorf("%s 的策略动作是 %q，不是 ActionSwitchNode ⇒ 它不属于「网关会换候选但客户端不该重试」这一族，放进排除集没有意义",
				kind, policyActionForKind(kind))
		}
		// ② 非空理由。
		reason, ok := RecoveryProjectionExclusionReason(kind)
		if !ok {
			t.Errorf("%s 被登记进排除集，但访问器**不认** ⇒ 理由为空或不符合排除资格", kind)
			continue
		}
		if reason == "" {
			t.Errorf("%s 在排除集里但没写理由", kind)
		}
		// ③ 符合排除资格。
		if !recoveryProjectionExclusionEligible(kind) {
			t.Errorf("%s 不符合排除资格（既不是请求侧语义错误、也不是凭据级不可用）", kind)
		}
		// ④ 投影行为：终态 + 客户端不可重试（**行为级**）。
		p := ProjectRecovery(kind)
		if p.CandidateFailover || p.TransparentResume {
			t.Errorf("%s 的投影带着候选故障转移/透明续跑 ⇒ 它不该在排除集里（projection=%+v）", kind, p)
		}
		if p.EffectiveAction != RecoveryActionTerminal || p.Reason != "not_recoverable" {
			t.Errorf("%s 的投影不是「终态 + not_recoverable」⇒ 排除集与投影口径矛盾（projection=%+v）", kind, p)
		}
		if EffectiveRetryable(kind) {
			t.Errorf("%s 被告知客户端可重试 ⇒ 与排除集矛盾", kind)
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
	if _, excluded := RecoveryProjectionExclusionReason(KindModelUnavailable); excluded {
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

// TestRecoveryProjectionClosedSet_ExclusionEligibilityIsEnforced 是**门 E**
// （第十二轮新增）：**排除资格判据真的被施加**，而不仅仅是写在注释里。
//
// ★ 这道门直接堵住第八轮那版门禁的漏法：往（当时的）测试内 map 里加一行
//   - 一句非空理由，就能对**任意** kind 放行。
//     ⇒ 现在即使有人往**生产**登记表里塞一个不合格的 kind，
//     recoveryProjectionExclusionReasonIn 也会**拒绝**它
//     ⇒ 门 A 随即对它变红。
func TestRecoveryProjectionClosedSet_ExclusionEligibilityIsEnforced(t *testing.T) {
	// 判据本身：绑定级 kind **不得**具备排除资格。
	for _, kind := range []ErrorKind{KindModelUnavailable, KindModelNotFound, KindModelDeprecated} {
		if recoveryProjectionExclusionEligible(kind) {
			t.Errorf("%s 是绑定级 kind，却被判成「可排除」⇒ 排除资格判据被放宽了 ⇒ "+
				"F-1 那一类缺陷（绑定级 kind 被告知客户端不可重试）失去结构性防线", kind)
		}
	}
	// 接线：**假登记表**里塞一个不合格的 kind ⇒ 查找必须拒绝它。
	fake := []recoveryProjectionExclusion{
		{kind: KindModelUnavailable, reason: "随便写一句理由"},
	}
	if reason, ok := recoveryProjectionExclusionReasonIn(fake, KindModelUnavailable); ok {
		t.Fatalf("不合格的 kind 被放行了（reason=%q）⇒ 排除资格判据**没有**被施加 ⇒ "+
			"往生产登记表里加一行就能对任意 kind 放行", reason)
	}
	// 接线：合格的两个 kind 仍然被放行（判据没有过严）。
	for _, kind := range []ErrorKind{KindRequestRejected, KindAuth} {
		if _, ok := recoveryProjectionExclusionReasonIn(recoveryProjectionExcludedKinds, kind); !ok {
			t.Errorf("%s 符合排除资格却被访问器拒绝 ⇒ 判据过严", kind)
		}
	}
}
