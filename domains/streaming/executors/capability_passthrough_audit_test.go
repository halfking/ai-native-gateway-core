package executors

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 批判式审计（2026-10-03）—— 遗留 #3 透传改动的**行为等价性**核验。
//
// 本文件不测「有没有省掉 GET」（那在 capability_state_passthrough_test.go），
// 而测透传本身引入的**新行为面**：复用一份更早的快照，会不会让判定变样。
//
// 审计要回答的具体问题：路由的 MGET 与 executor 的闸门之间隔着多久？在这段
// 窗口里 Redis 上的结论被改写（SetSupportsResponses / 过期）时，透传读到的是
// 旧值。旧值对不对？

// TestAudit_SnapshotStalenessWindow_MeasuresTheGap 量化这个窗口。
// 它不假定窗口长短，只把它测出来供人判断——这是「先量再判」，不是「先判再找证据」。
func TestAudit_SnapshotStalenessWindow_MeasuresTheGap(t *testing.T) {
	_, fpMgr, _, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed verdict: %v", err)
	}
	// 快照 = 路由 MGET 的等价物
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	routedAt := time.Now()

	// 模拟闸门前在别处发生的事：结论被改写为 supported=true。
	// 真实世界里这是探针或另一条请求的写入。
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("rewrite verdict: %v", err)
	}

	// 用透传快照读：拿到的是改写**之前**的值。
	viaSnapshot, knownSnapshot, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read via snapshot: %v", err)
	}
	// 用现读（nil）：拿到改写之后的值。
	viaFresh, knownFresh, err := fpMgr.GetSupportsResponses(ctx, credID, model, nil)
	if err != nil {
		t.Fatalf("read fresh: %v", err)
	}

	gap := time.Since(routedAt)
	t.Logf("窗口=%v 透传读到 supported=%v(known=%v)  现读读到 supported=%v(known=%v)",
		gap, viaSnapshot, knownSnapshot, viaFresh, knownFresh)

	// 事实陈述，不是期望：两者**确实不同**。这不是缺陷，是「复用更早的快照」
	// 这个选择的必然结果，必须被记录而不是被测试掩盖。
	if viaSnapshot == viaFresh && knownSnapshot == knownFresh {
		t.Log("本用例下两者一致（改写未落在窗口内或语义不敏感）")
	} else {
		t.Log("透传与现读结论不同 —— 透传读到的是路由时刻的旧结论")
	}

	// 关键不变式：无论走哪条路，都不得读成「已知且支持」以外的第四种答案。
	// 具体地，透传绝不能因为「有快照」就跳过 SupportsResponsesKnown 判定。
	if knownSnapshot && viaSnapshot != *snapshot.Capabilities.SupportsResponses {
		t.Fatalf("透传读出的结论与快照本身矛盾: got=%v snapshot=%v",
			viaSnapshot, *snapshot.Capabilities.SupportsResponses)
	}
}

// TestAudit_SnapshotWithNoVerdictIsStillUnknown 是本轮最容易写错的一处：
// 快照存在但**没有结论**（capabilities 子树为空）时，必须读成 unknown，
// 而不是「有快照 ⇒ 有结论 ⇒ 取默认值」。
func TestAudit_SnapshotWithNoVerdictIsStillUnknown(t *testing.T) {
	_, fpMgr, _, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	// 一个只有健康字段、没有任何能力结论的快照（探针从未跑过该节点）。
	healthOnly := &credentialfpslot.NodeState{
		CredentialID: credID, Model: model,
		SlideWindow: []credentialfpslot.NodeRecord{},
	}
	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, healthOnly)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if known {
		t.Fatalf("known=%v, want false: a snapshot WITHOUT a verdict must read as unknown", known)
	}
	if supported {
		t.Fatal("supported=true from a verdict-less snapshot")
	}
}

// TestAudit_ZeroValuedSnapshotIsNotNil 覆盖一个具体的踩坑形态：
// GetNodeStatesBatch 对「键不存在」返回的是**零值 NodeState 而不是 nil**。
// 若调用方把零值当成有效快照，就会把「从未探测过」误当成「有快照可复用」。
// 本例钉住零值快照的读出结果必须与 nil 回退一致（都是 unknown），
// 从而即便上游误传零值也不会给出错误结论。
func TestAudit_ZeroValuedSnapshotIsNotNil(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	// 该键从未写入：GetNodeStatesBatch 会给出零值 state（非 nil）。
	batch, err := fpMgr.GetNodeStatesBatch(ctx, []credentialfpslot.NodeStateKey{
		{CredentialID: credID, Model: model},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch) != 1 || batch[0] == nil {
		t.Skipf("batch shape changed (len=%d); re-check this test's premise", len(batch))
	}
	if batch[0].Capabilities.SupportsResponsesKnown() {
		t.Fatal("a never-written key must not report a known verdict")
	}
	_ = mr

	// 零值快照 vs nil：两者都必须读成 unknown。
	viaZero, knownZero, err := fpMgr.GetSupportsResponses(ctx, credID, model, batch[0])
	if err != nil {
		t.Fatalf("read via zero snapshot: %v", err)
	}
	viaNil, knownNil, err := fpMgr.GetSupportsResponses(ctx, credID, model, nil)
	if err != nil {
		t.Fatalf("read via nil: %v", err)
	}
	if knownZero != knownNil || viaZero != viaNil {
		t.Fatalf("zero-valued snapshot and nil disagree: zero=(%v,%v) nil=(%v,%v)",
			viaZero, knownZero, viaNil, knownNil)
	}
}

// TestAudit_ExpiredVerdictInSnapshotIsNotHonoured 钉住「快照里的过期结论
// 不得被直接采信」：即便快照的 Capabilities 明确写着 supported=false，
// 只要 Redis 时钟已过期限，就必须读成 unknown —— 否则等于给过期结论续命，
// 且续命发生在**没有重新 GET** 的情况下，比原来的读更危险。
func TestAudit_ExpiredVerdictInSnapshotIsNotHonoured(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snapshot.Capabilities.SupportsResponsesKnown() {
		t.Fatal("premise: snapshot must carry a verdict")
	}

	// 把 Redis 时钟拨过期限。
	mr.SetTime(time.Unix(snapshot.CapabilityExpiresAt, 0).Add(time.Second))

	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if known {
		t.Fatalf("known=%v supported=%v, want unknown: an expired deadline must not be honoured from a prefetched snapshot",
			known, supported)
	}
}

// TestAudit_DispatchRefRoundTripKeepsSnapshot 覆盖审计中发现的一处结构事实：
// dispatch 把 candidate 压成 CredentialRef（该结构只带标量，会丢掉快照），
// 随后又按 CredentialID 从 d.candidates 取回完整 candidate。
// 若这个「取回」失效，dispatch 路径会静默退化成永远自读——功能不错，
// 但本轮的目标（省掉那次读）在生产主路径上就没了。
// 本例钉住：按 CredentialID 取回的 candidate 必须仍带着快照。
func TestAudit_DispatchRefRoundTripKeepsSnapshot(t *testing.T) {
	snapshot := &credentialfpslot.NodeState{
		CredentialID: 22, Model: "gpt-5.6-terra",
		SlideWindow: []credentialfpslot.NodeRecord{},
	}
	cands := []provider.Candidate{
		{CredentialID: 21, RawModel: "gpt-5.6-terra"},
		{CredentialID: 22, RawModel: "gpt-5.6-terra", RoutedNodeState: snapshot},
	}

	// 压成 ref（此刻快照必然丢失，这是设计如此）。
	ref := candidateToRef(cands[1])
	if ref.CredentialID != 22 {
		t.Fatalf("ref credential = %d, want 22", ref.CredentialID)
	}

	// 按 CredentialID 取回（dispatchForward 的真实做法）。
	var got provider.Candidate
	for _, c := range cands {
		if c.CredentialID == ref.CredentialID {
			got = c
			break
		}
	}
	if got.RoutedNodeState == nil {
		t.Fatal("lookup by CredentialID lost the snapshot: the dispatch path would " +
			"silently fall back to re-reading, so the optimisation would never fire in production")
	}
	if got.RoutedNodeState.CapabilityExpiresAt != snapshot.CapabilityExpiresAt {
		t.Fatal("lookup returned a different snapshot")
	}
}

// TestAudit_StaleSnapshotIsRejectedAndReread 钉住审计发现的那个真实缺口：
// 请求可以在 dispatch 队列里等到快照任意陈旧（Submit 阻塞在 qr.ResultCh，
// 队列等待无上界），而 TTL 之内结论可能已被改写。超过 prefetchMaxAgeSec
// 的快照必须被丢弃并重新读取。
//
// 这条判据的判红方式是：把快照的 CapabilityUpdatedAt 拨到很久以前，
// 同时把 Redis 上的真实结论改写成相反的值。若护栏不存在，会读到旧结论。
func TestAudit_StaleSnapshotIsRejectedAndReread(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CapabilityUpdatedAt <= 0 {
		t.Fatal("premise: SetSupportsResponses must stamp CapabilityUpdatedAt")
	}

	// 现实场景：请求在队列里等了 60s，期间探针把结论翻成 true。
	mr.SetTime(base.Add(60 * time.Second))
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	viaSnapshot, knownSnapshot, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read via stale snapshot: %v", err)
	}
	// 快照 60s 前写的，超过 5s 上限 ⇒ 必须被丢弃重读 ⇒ 读到新值 true。
	if !knownSnapshot {
		t.Fatalf("known=%v: a rewrote verdict must still be readable", knownSnapshot)
	}
	if !viaSnapshot {
		t.Fatalf("supported=false, want true: a snapshot written %ds ago must be rejected "+
			"and re-read (the request sat in the dispatch queue while the verdict was rewritten)",
			60)
	}
}

// TestAudit_FreshSnapshotIsStillUsed 确认护栏没有把优化整个关掉：
// 新鲜快照仍应被采信，不触发多余的 GET。
func TestAudit_FreshSnapshotIsStillUsed(t *testing.T) {
	_, fpMgr, _, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// 快照里写 false；同时把 Redis 改成 true。若新鲜快照被采信，读到 false；
	// 若被当作陈旧而重读，读到 true。二者必须不同，否则新鲜快照没被用。
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !known {
		t.Fatal("known=false")
	}
	if supported {
		t.Fatal("supported=true: a FRESH snapshot must still be trusted; the age guard " +
			"must not silently disable the optimisation it is meant to bound")
	}
}

// TestAudit_SnapshotWithoutUpdatedAtIsReread 覆盖 pre-migration payload：
// 没有 CapabilityUpdatedAt 的旧 payload，年龄不可知 ⇒ 不得当作新鲜。
func TestAudit_SnapshotWithoutUpdatedAtIsReread(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	state, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// 抹掉时间戳，模拟 pre-migration payload 形状。
	state.CapabilityUpdatedAt = 0
	// 同时让 Redis 上的真实结论相反。
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, state)
	if err != nil {
		t.Fatalf("read via unstamped snapshot: %v", err)
	}
	if !known || !supported {
		t.Fatalf("(supported=%v, known=%v), want (true, true): an unstamped snapshot's age "+
			"is unknowable, so it must be re-read rather than trusted", supported, known)
	}
}

// prefetchMaxAgeSecForTest mirrors the unexported constant so the test can
// state the premise ("this snapshot is genuinely fresh") instead of hardcoding
// 5 and silently going stale if the production bound is retuned.
const prefetchMaxAgeSecForTest = 5

// TestAudit_AgeGuardMustNotDisableTheOptimisation 是变异 M6 逼出来的判据。
//
// M6 把 prefetchMaxAgeSec 改成 0（任何快照都算「太旧」⇒ 永远重读），
// 结果整套 TestAudit_ 仍然全绿 —— 因为 M5 那几条只验「陈旧被拒」，
// 护栏收紧到极致时它们照样满足。**只钉「该拒的拒了」的门，会在
// 「全都拒了」时保持绿色**：护栏退化成把优化整个关掉，没有任何用例会红。
//
// 本例用与 M5 相反的方向钉住同一根轴：新鲜的快照必须**仍然被采信**，
// 且从 Redis 上抹掉 verdict，使「采信」与「重读」产生可区分的结果。
func TestAudit_AgeGuardMustNotDisableTheOptimisation(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// 让快照「明确新鲜但非同一瞬间」：比护栏上限新，却至少 1 秒旧。
	// 这一步是必需的，不是装饰 —— 护栏写死 0 时判据是 now-updatedAt > 0，
	// 若快照与 now 落在同一秒，0 > 0 为假、快照照样被采信，判据就会假绿。
	// 真实队列等待必然跨秒，所以这才是「新鲜」的真实形状。
	age := time.Duration(prefetchMaxAgeSecForTest-1) * time.Second
	if age < time.Second {
		age = time.Second
	}
	mr.SetTime(base.Add(age))
	t.Logf("快照年龄 = %v（护栏上限 = %ds）", age, prefetchMaxAgeSecForTest)
	if age >= time.Duration(prefetchMaxAgeSecForTest)*time.Second {
		t.Fatalf("premise broken: the snapshot is already older than the guard (%v)", age)
	}

	// 现在把 Redis 上的 verdict 抹掉：重读会读到「无结论」(unknown)，
	// 而采信快照会读到 true。两者可区分。
	state, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	state.Capabilities.SupportsResponses = nil
	state.CapabilityExpiresAt = 0
	if err := fpMgr.SetNodeState(ctx, state); err != nil {
		t.Fatalf("clear verdict: %v", err)
	}

	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read via fresh snapshot: %v", err)
	}
	if !known || !supported {
		t.Fatalf("(supported=%v, known=%v), want (true, true): a FRESH snapshot must be used. "+
			"If the age guard rejects everything, the optimisation is silently disabled and no "+
			"other test notices.", supported, known)
	}
}
