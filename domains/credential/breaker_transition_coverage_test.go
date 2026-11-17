package credential

// ★★ 熔断器状态迁移补齐覆盖（2026-11-18 · Worker 交付包）
//
// ── 这份文件为什么存在 ────────────────────────────────────────────────
//
// `domains/credential/breaker.go` 是全仓唯一的熔断状态机（4 个状态 × 若干条
// 入口路径）。既有测试（breaker_test.go / breaker_recovery_test.go /
// breaker_reset_test.go / breaker_no_available_channel_test.go /
// state_sync_test.go）覆盖了主干，但**下列迁移路径此前没有直测**：
//
//	① OPEN → QUARANTINED      （RecoveryPermanent 分支不判前态，:660-662）
//	② QUARANTINED → OPEN      （quota reset 对齐降级，:491 / :511-513）
//	③ OPEN → OPEN 原地重入    （顺延冷却，:674-759）
//	④ Manager.PeekAllowed 命中不存在的 breaker（:978-983）
//	⑤ Manager.PeekAllowed 在 OPEN 态的返回语义（:335-338）
//	⑥ Manager.RecordFailureWithBillingMode 的非 free 分支（:1032-1038）
//	⑦ Manager 层包装：RecordFailureWithReset / RecordModelScopedFailure
//	   （:1042-1052）
//	⑧ exponentialCooling 的溢出/回绕防护（:792-802）
//	⑨ Allow() 的并发竞态：最多一个探针被放行（claimProbe 全程持锁，:299-319）
//	⑩ Manager.Stats() 的边界（:986-995 / :878-904）
//	⑪ State.String() 的 quarantined 与未知值分支（:43-49）
//
// ── 纪律 ──────────────────────────────────────────────────────────────
//
//	· 本文件**只加测试**，不改任何生产代码（breaker.go / state_sync.go 一律只读）。
//	· 表驱动 + t.Run（仓内惯例），只用标准库 testing，不引入新依赖。
//	· 每条断言都钉**不变量**并配**负控**（前提在场）——负控防的是「断言空过」。
//	· 时间敏感处走**同包未导出字段**覆写（合法手段），不依赖真实网络/数据库，
//	  也不依赖真实长等待（冷却窗口全部用「覆写 coolingExpires」推进）。

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ── ① OPEN → QUARANTINED ─────────────────────────────────────────────

// TestOpenBreakerQuarantinesOnPermanentFailure 钉住「已开路再吃永久性失败 ⇒
// 进隔离」这条迁移（breaker.go:660-662）。
//
// 为什么这条路径值得单独钉：RecoveryPermanent 分支**不判前态**——它只按
// policy.RecoveryType 落状态，所以前态是 OPEN 还是 CLOSED 都会落到
// QUARANTINED。若将来有人给该分支加上「只有 CLOSED 才隔离」的前置条件，
// 「已开路的凭据吃到配额耗尽 ⇒ 升级为隔离（人工恢复）」这条运营语义就会
// 静默丢失（凭据会一直挂在 OPEN 上自动恢复，而不是等人处理）。
func TestOpenBreakerQuarantinesOnPermanentFailure(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.SetObserver(collectedObserver(&events))

	// 前提在场：先把 breaker 开路（阈值恒为 2，:74-76）。
	m.RecordFailure(1, 1, KindTransient)
	m.RecordFailure(1, 1, KindTransient)
	if got := m.Get(1, 1).State(); got != StateOpen {
		t.Fatalf("premise broken: breaker should be open, got %s", got)
	}

	// 配额族（RecoveryPermanent，:131）⇒ 落隔离。
	m.RecordFailure(1, 1, KindQuota)
	m.RecordFailure(1, 1, KindQuota)

	b := m.Get(1, 1)
	if got := b.State(); got != StateQuarantined {
		t.Fatalf("open breaker must quarantine on permanent failure, got %s", got)
	}
	if b.Allow() {
		t.Fatal("quarantined breaker must reject requests")
	}

	// 迁移事件必须带 from=open ⇒ 观察者能区分「升级为隔离」与「首次隔离」。
	var sawOpenToQuarantined bool
	for _, ev := range events {
		if ev.From == StateOpen && ev.To == StateQuarantined {
			sawOpenToQuarantined = true
		}
	}
	if !sawOpenToQuarantined {
		t.Fatalf("expected an open→quarantined transition event, got %v", events)
	}
}

// TestOpenBreakerStaysOpenOnRecoverableFailure 是 ① 的**负控**：同样是
// 「已开路再吃一次失败」，但失败族是**可自恢复**的（KindAuth =
// RecoveryExponential，:130）⇒ 状态必须仍是 OPEN（原地顺延冷却），不得
// 落隔离。
//
// 为什么必须钉：隔离＝人工恢复，只有真·永久性失败（配额/额度）才配得上；
// 若把「已开路」误当成「永久性」，一次 auth 抖动就会把可自恢复的凭据钉死在
// 隔离态上（需要管理员手工清）。
func TestOpenBreakerStaysOpenOnRecoverableFailure(t *testing.T) {
	m := NewManager()
	m.RecordFailure(2, 2, KindTransient)
	m.RecordFailure(2, 2, KindTransient)
	if got := m.Get(2, 2).State(); got != StateOpen {
		t.Fatalf("premise broken: breaker should be open, got %s", got)
	}

	m.RecordFailure(2, 2, KindAuth)

	if got := m.Get(2, 2).State(); got != StateOpen {
		t.Fatalf("a recoverable failure must not quarantine an open breaker, got %s", got)
	}
}

// ── ② QUARANTINED → OPEN ─────────────────────────────────────────────

// TestQuarantinedBreakerDowngradesToOpenOnQuotaReset 钉住 ②：隔离态吃到
// 「带有效未来 resetAt 的配额失败」⇒ 降级为 OPEN 且冷却对齐上游重置时刻
// （breaker.go:455-462 → :471-514）。
//
// 这条路径相对既有测试（breaker_reset_test.go:72
// TestQuotaResetDowngradesQuarantine）的新增断言在**观察者事件**上：
// :511-513 的 notify 必须把 from=quarantined / to=open 连同**新的冷却截止
// 时刻**一起报出去——DB 侧（state_sync.go:143）靠这个 CoolingUntil 把
// cooling_until 列刷成新的对齐时刻；漏报就会让列里停留在隔离前的旧值。
func TestQuarantinedBreakerDowngradesToOpenOnQuotaReset(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.SetObserver(collectedObserver(&events))

	m.RecordFailure(3, 3, KindQuota)
	m.RecordFailure(3, 3, KindQuota)
	b := m.Get(3, 3)
	if got := b.State(); got != StateQuarantined {
		t.Fatalf("premise broken: breaker should be quarantined, got %s", got)
	}

	m.RecordFailureWithReset(3, 3, KindQuota, time.Now().Add(45*time.Minute), true)

	if got := b.State(); got != StateOpen {
		t.Fatalf("quarantine must downgrade to open on quota reset evidence, got %s", got)
	}
	// 冷却对齐到上游声明的重置时刻（钳在 30s~2h，:477-484）。
	cooling := readCooling(b)
	if cooling < 40*time.Minute || cooling > 50*time.Minute {
		t.Fatalf("cooling should align to the upstream reset (~45min), got %v", cooling)
	}
	// 降级后不继承隔离前的指数档（:494 coolingCycle=0）。
	b.mu.Lock()
	cycle := b.coolingCycle
	b.mu.Unlock()
	if cycle != 0 {
		t.Fatalf("a reset-aligned open must not inherit the exponential ladder, got cycle %d", cycle)
	}

	// 观察者必须看到 quarantined→open，且带新的冷却截止时刻。
	var sawDowngrade bool
	for _, ev := range events {
		if ev.From == StateQuarantined && ev.To == StateOpen {
			sawDowngrade = true
			if ev.CoolingUntil.IsZero() {
				t.Fatalf("the downgrade event must carry the aligned cooling deadline, got %v", ev)
			}
			if !ev.CoolingUntil.After(time.Now()) {
				t.Fatalf("the aligned cooling deadline must be in the future, got %v", ev.CoolingUntil)
			}
		}
	}
	if !sawDowngrade {
		t.Fatalf("expected a quarantined→open downgrade event, got %v", events)
	}
}

// ── ③ OPEN → OPEN 原地重入 ───────────────────────────────────────────

// TestOpenBreakerReentryExtendsCoolingWindow 钉住 ③：已开路的 breaker 再吃
// 满阈值失败 ⇒ 仍是 OPEN，但冷却窗口被顺延（breaker.go:674-759）。
//
// 为什么值得钉：顺延靠的是「原地重入」分支——它不新开一轮，而是把
// openSince / coolingExpires 刷新。若这段被改成「已 OPEN 时不再刷新冷却」，
// 一次持续故障就会退化成「第一次开路的窗口到期后立刻半开探针」⇒ 降级凭据
// 被反复放行。
//
// 顺延幅度钉在阶梯上：KindTransient 第一次开路走 R31 升级后的 UpstreamDown
// 档（Initial 30s，:707-729），第二轮 60s ⇒ 阶梯 ×2。
//
// ★ 相对既有测试（state_sync_test.go:95
// TestBreakerOpenReentryNotifiesCoolingExtension）的新增断言：既有那条钉的是
// **观察者事件**里的 CoolingUntil；这里钉 breaker **自身**的窗口长度、
// 冷却档序号与 open_since 刷新（即状态机内部的顺延语义本身）。
func TestOpenBreakerReentryExtendsCoolingWindow(t *testing.T) {
	m := NewManager()
	m.RecordFailure(4, 4, KindTransient)
	m.RecordFailure(4, 4, KindTransient) // → 第一次开路

	b := m.Get(4, 4)
	if got := b.State(); got != StateOpen {
		t.Fatalf("premise broken: breaker should be open, got %s", got)
	}
	b.mu.Lock()
	firstWindow := b.coolingExpires.Sub(b.openSince)
	firstCycle := b.coolingCycle
	firstOpenSince := b.openSince
	b.mu.Unlock()
	if firstWindow != 30*time.Second {
		t.Fatalf("premise broken: the first open window should be the escalated 30s step, got %v", firstWindow)
	}

	// 已开路再吃一次失败 ⇒ 原地重入（顺延冷却）。
	m.RecordFailure(4, 4, KindTransient)

	if got := b.State(); got != StateOpen {
		t.Fatalf("re-entry must stay open, got %s", got)
	}
	b.mu.Lock()
	secondWindow := b.coolingExpires.Sub(b.openSince)
	secondCycle := b.coolingCycle
	secondOpenSince := b.openSince
	b.mu.Unlock()

	if secondWindow <= firstWindow {
		t.Fatalf("re-entry must extend the cooling window: first=%v second=%v", firstWindow, secondWindow)
	}
	if secondWindow != 2*firstWindow {
		t.Fatalf("the cooling ladder should double per cycle: first=%v second=%v", firstWindow, secondWindow)
	}
	if secondCycle != firstCycle+1 {
		t.Fatalf("the cooling cycle should advance by one: first=%d second=%d", firstCycle, secondCycle)
	}
	if secondOpenSince.Before(firstOpenSince) {
		t.Fatalf("re-entry must refresh open_since: first=%v second=%v", firstOpenSince, secondOpenSince)
	}
}

// ── ④⑤ Manager.PeekAllowed 的返回语义 ───────────────────────────────

// TestManagerPeekAllowedSemantics 钉住 ④⑤：Manager.PeekAllowed 的返回语义
// （:978-983 委托 Breaker.PeekAllowed :325-342）。
//
// 四条判据：
//
//	· 命中**不存在**的 breaker ⇒ true（无 breaker＝没有熔断约束，:982）
//	· OPEN 且**冷却未到** ⇒ false（:338 now.After(coolingExpires)）
//	· OPEN 且**冷却已过** ⇒ true（同一判据的另一半）
//	· QUARANTINED ⇒ false（:329-330，隔离是人工恢复态，不随时间自愈）
//
// 为什么值得钉：PeekAllowed 是**只读预检**（不消费探针槽，:321-324），
// sync-retry 的 allCircuitOpen 预扫靠它决定要不要换凭据。它返回 true 而
// 实际 Allow() 会拒绝（或反之）⇒ 请求面会出现「预检说能走、真走时却被
// 熔断拒」的错配。
func TestManagerPeekAllowedSemantics(t *testing.T) {
	m := NewManager()

	// ④ 命中不存在的 breaker ⇒ true。
	if !m.PeekAllowed(9, 9) {
		t.Fatal("PeekAllowed on a missing breaker must be true (no breaker = no circuit constraint)")
	}
	// 负控：PeekAllowed **不得**创建 breaker（只读预检不许有副作用）。
	if m.Get(9, 9) != nil {
		t.Fatal("PeekAllowed must not create a breaker")
	}

	// ⑤ OPEN 且冷却未到 ⇒ false。
	m.RecordFailure(5, 5, KindTransient)
	m.RecordFailure(5, 5, KindTransient)
	b := m.Get(5, 5)
	if got := b.State(); got != StateOpen {
		t.Fatalf("premise broken: breaker should be open, got %s", got)
	}
	if m.PeekAllowed(5, 5) {
		t.Fatal("an open breaker inside its cooling window must not be peek-allowed")
	}

	// 冷却已过 ⇒ true，且 PeekAllowed **不得**迁移状态。
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if !m.PeekAllowed(5, 5) {
		t.Fatal("an open breaker past its cooling deadline must be peek-allowed")
	}
	if got := b.State(); got != StateOpen {
		t.Fatalf("PeekAllowed must not transition the state, got %s", got)
	}

	// 隔离态 ⇒ false（不随时间自愈）。
	m.RecordFailure(6, 6, KindQuota)
	m.RecordFailure(6, 6, KindQuota)
	if got := m.Get(6, 6).State(); got != StateQuarantined {
		t.Fatalf("premise broken: breaker should be quarantined, got %s", got)
	}
	if m.PeekAllowed(6, 6) {
		t.Fatal("a quarantined breaker must never be peek-allowed")
	}
}

// ── ⑥ Manager.RecordFailureWithBillingMode 的非 free 分支 ────────────

// TestManagerRecordFailureWithBillingModeProfileSelection 钉住 ⑥：
// Manager.RecordFailureWithBillingMode 的**非 free 分支**（:1032-1038）。
//
// 判据不是「标记位」而是**冷却档**：同一个失败族，free 档与付费档的冷却
// 时长差一个数量级（KindConcurrent：付费 2min vs 免费 5s）。若 billingMode
// 判错（把付费误当免费 ⇒ 白扔 4 倍容量；或把免费误当付费 ⇒ 免费池被误伤），
// 光看状态位（都是 open）分辨不出来。
//
// 表驱动覆盖 EqualFold + TrimSpace 的全部判定形态（:1034）。
func TestManagerRecordFailureWithBillingModeProfileSelection(t *testing.T) {
	cases := []struct {
		name        string
		billingMode string
		wantFree    bool
		wantCooling time.Duration
	}{
		{name: "per_token is paid", billingMode: "per_token", wantFree: false, wantCooling: 2 * time.Minute},
		{name: "empty falls back to paid", billingMode: "", wantFree: false, wantCooling: 2 * time.Minute},
		{name: "paid is paid", billingMode: "paid", wantFree: false, wantCooling: 2 * time.Minute},
		{name: "free is free", billingMode: "free", wantFree: true, wantCooling: 5 * time.Second},
		{name: "uppercase free is free", billingMode: "FREE", wantFree: true, wantCooling: 5 * time.Second},
		{name: "padded free is free", billingMode: "  free  ", wantFree: true, wantCooling: 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			m.RecordFailureWithBillingMode(7, 7, KindConcurrent, tc.billingMode)
			m.RecordFailureWithBillingMode(7, 7, KindConcurrent, tc.billingMode)

			b := m.Get(7, 7)
			if got := b.State(); got != StateOpen {
				t.Fatalf("premise broken: breaker should be open, got %s", got)
			}
			if got := b.IsFreeTier(); got != tc.wantFree {
				t.Fatalf("free_tier flag = %v, want %v (billing_mode=%q)", got, tc.wantFree, tc.billingMode)
			}
			cooling := readCooling(b)
			if diff := cooling - tc.wantCooling; diff > 2*time.Second || diff < -2*time.Second {
				t.Fatalf("cooling = %v, want ~%v (billing_mode=%q)", cooling, tc.wantCooling, tc.billingMode)
			}
		})
	}
}

// ── ⑦ Manager 层包装 ─────────────────────────────────────────────────

// TestManagerRecordFailureWithResetWrapper 钉住 ⑦ 的第一条：Manager 层的
// RecordFailureWithReset 包装（:1042-1045）把「带 reset 证据的配额失败」
// 落到**冷却对齐**的 OPEN（而不是阈值累计）。
//
// 负控：ok=false ⇒ 回落旧语义（阈值累计 ⇒ 一次不成气候、两次隔离）。
func TestManagerRecordFailureWithResetWrapper(t *testing.T) {
	m := NewManager()
	m.RecordFailureWithReset(8, 8, errorsx.KindQuotaPeriodic, time.Now().Add(10*time.Minute), true)

	b := m.Get(8, 8)
	if got := b.State(); got != StateOpen {
		t.Fatalf("a reset-aligned quota failure must open immediately, got %s", got)
	}
	cooling := readCooling(b)
	if cooling < 9*time.Minute || cooling > 11*time.Minute {
		t.Fatalf("cooling should align to the upstream reset (~10min), got %v", cooling)
	}

	// 负控：ok=false 回落旧语义 ⇒ 单次不成气候。
	m2 := NewManager()
	m2.RecordFailureWithReset(9, 9, KindQuota, time.Time{}, false)
	if got := m2.Get(9, 9).State(); got != StateClosed {
		t.Fatalf("premise broken: an unhinted quota failure must stay closed below threshold, got %s", got)
	}
	m2.RecordFailureWithReset(9, 9, KindQuota, time.Time{}, false)
	if got := m2.Get(9, 9).State(); got != StateQuarantined {
		t.Fatalf("the legacy quota path must still quarantine, got %s", got)
	}
}

// TestManagerRecordModelScopedFailureWrapper 钉住 ⑦ 的第二条：Manager 层的
// RecordModelScopedFailure 包装（:1049-1052）。
//
// 三条判据：
//
//	· 模型级拒收**不计数、不迁移**（凭据连坐被移除）
//	· 包装层**会创建** breaker（内部走 GetOrCreate）——若不存在就返回，
//	  调用方的失败记录会凭空消失
//	· 负控：同一条凭据随后吃真实失败**仍能正常跳闸**（计数没被污染）
func TestManagerRecordModelScopedFailureWrapper(t *testing.T) {
	m := NewManager()
	m.RecordModelScopedFailure(10, 10, errorsx.KindModelNotFound, "gpt-5-fake")

	b := m.Get(10, 10)
	if b == nil {
		t.Fatal("Manager.RecordModelScopedFailure must create the breaker (GetOrCreate)")
	}
	if got := b.State(); got != StateClosed {
		t.Fatalf("a model-scoped rejection must not trip the credential breaker, got %s", got)
	}
	if got := b.ConsecutiveFailures(); got != 0 {
		t.Fatalf("a model-scoped rejection must not accumulate consecutive failures, got %d", got)
	}
	if !m.Allow(10, 10) {
		t.Fatal("the credential must stay usable for sibling models")
	}

	// 负控：真实失败仍能正常跳闸（证明模型级拒收没有污染计数）。
	m.RecordFailure(10, 10, KindTransient)
	m.RecordFailure(10, 10, KindTransient)
	if got := b.State(); got != StateOpen {
		t.Fatalf("real failures must still trip the breaker, got %s", got)
	}
}

// TestManagerRecordModelScopedFailureReleasesProbe 钉住 ⑦ 的第三条：半开态
// 下的模型级拒收要**归还探针槽**（:1049-1052 → Breaker.RecordModelScopedFailure
// :532-545 → ReleaseProbe :351-361）。
//
// 为什么重要：探针请求打在已死模型上，若不归还槽位，凭据会卡在「探针被占」
// 的半开态（breaker.go:290-297 记录的那起事故：槽位泄漏 ⇒ 熔断器卡死
// 数小时，只能重启进程）。
func TestManagerRecordModelScopedFailureReleasesProbe(t *testing.T) {
	m := NewManager()
	b := m.GetOrCreate(11, 11)
	b.mu.Lock()
	b.state.Store(int32(StateHalfOpen))
	b.halfOpenProbes.Store(1)
	b.nextProbeAt = time.Now().Add(halfOpenProbeTimeout)
	b.mu.Unlock()

	m.RecordModelScopedFailure(11, 11, errorsx.KindModelDeprecated, "glm-5.2")

	if !m.Allow(11, 11) {
		t.Fatal("the probe slot must be released so the next request can re-probe")
	}
}

// ── ⑧ exponentialCooling 的溢出/回绕防护 ────────────────────────────

// TestExponentialCoolingClampsWithoutOverflow 钉住 ⑧：指数退避的溢出/回绕
// 防护（breaker.go:792-802，R34 修复）。
//
// 为什么它是硬门：朴素写法 `initial << (cycle-1)` 在 int64 里会**回绕**，
// 回绕出的负时长会击穿 `> max` 的钳位（负数不大于 max）⇒ coolingExpires
// 落在过去 ⇒ 熔断器不再保护这条凭据。「float64 + 先钳后转」是唯一的防线。
//
// 负控两个边界一起钉：
//
//	· initial=0 走 `v <= 0` 分支返回 **max**（不是 0）——这是实现的既有
//	  语义，钉住它是为了让将来改公式的人看见这个边界
//	· max=0 ⇒ 返回 0（钳位目标本身是零）
func TestExponentialCoolingClampsWithoutOverflow(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name    string
		initial time.Duration
		max     time.Duration
		cycle   int
		want    time.Duration
	}{
		{name: "cycle one equals initial", initial: 30 * time.Second, max: 30 * time.Minute, cycle: 1, want: 30 * time.Second},
		{name: "cycle two doubles", initial: 30 * time.Second, max: 30 * time.Minute, cycle: 2, want: 60 * time.Second},
		{name: "cycle three doubles again", initial: 30 * time.Second, max: 30 * time.Minute, cycle: 3, want: 120 * time.Second},
		{name: "clamps at max", initial: 30 * time.Second, max: 30 * time.Minute, cycle: 40, want: 30 * time.Minute},
		{name: "int64 overflow would wrap negative", initial: time.Duration(math.MaxInt64), max: day, cycle: 2, want: day},
		{name: "huge initial clamps at max", initial: time.Duration(1) << 62, max: day, cycle: 5, want: day},
		{name: "negative cycle clamps to step zero", initial: 30 * time.Second, max: 30 * time.Minute, cycle: 0, want: 30 * time.Second},
		{name: "zero initial returns max", initial: 0, max: time.Hour, cycle: 1, want: time.Hour},
		{name: "zero max returns zero", initial: time.Second, max: 0, cycle: 1, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exponentialCooling(tc.initial, tc.max, tc.cycle)
			if got != tc.want {
				t.Fatalf("exponentialCooling(%v, %v, %d) = %v, want %v",
					tc.initial, tc.max, tc.cycle, got, tc.want)
			}
			if got < 0 {
				t.Fatalf("the cooling duration must never be negative, got %v", got)
			}
		})
	}
}

// ── ⑨ Allow() 的并发竞态 ─────────────────────────────────────────────

// TestAllowConcurrentProbeAdmissionIsExclusive 钉住 ⑨：并发 Allow() 下
// **最多只有一个探针被放行**（claimProbe 全程持 b.mu，:305-319）。
//
// 为什么是硬门：半开态只有一个探针槽。若两个请求同时被放行，两条坏请求
// 会同时打上去，「一次探针验证恢复」的语义就破了（且坏凭据拿到双倍流量）。
// breaker.go:299-304 的注释明确记着：无锁快路径（Add(1)==1）会与超时回收
// 路径竞争出**两个**探针 ⇒ 这条测试是那段注释的回归门。
//
// 用例形态：
//
//	① 已半开 + 槽位空   ⇒ 32 个并发 Allow 恰好 1 个 true
//	② 已半开 + 槽位被占 ⇒ 32 个并发 Allow 全部 false
//	③ 开路且冷却已过（OPEN→HALF_OPEN 迁移本身也在竞争里）⇒ 恰好 1 个 true
//	④ 负控：闭合态 ⇒ 全部 true（闭合态没有探针语义）
//
// ★ 本条必须用 `-race` 跑才有意义（race detector 是并发语义的门）。
func TestAllowConcurrentProbeAdmissionIsExclusive(t *testing.T) {
	const goroutines = 32

	run := func(t *testing.T, b *Breaker) int {
		t.Helper()
		var allowed atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // 放大竞争窗口，让所有 goroutine 同时进入 Allow()
				if b.Allow() {
					allowed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		return int(allowed.Load())
	}

	t.Run("half open with a free slot admits exactly one probe", func(t *testing.T) {
		b := New(12, 12)
		b.mu.Lock()
		b.state.Store(int32(StateHalfOpen))
		b.halfOpenProbes.Store(0)
		b.mu.Unlock()
		if got := run(t, b); got != 1 {
			t.Fatalf("exactly one probe must be admitted, got %d", got)
		}
	})

	t.Run("half open with an occupied slot admits nobody", func(t *testing.T) {
		b := New(13, 13)
		b.mu.Lock()
		b.state.Store(int32(StateHalfOpen))
		b.halfOpenProbes.Store(1)
		b.nextProbeAt = time.Now().Add(halfOpenProbeTimeout)
		b.mu.Unlock()
		if got := run(t, b); got != 0 {
			t.Fatalf("an occupied probe slot must admit nobody, got %d", got)
		}
	})

	t.Run("open breaker past its cooling deadline admits exactly one probe", func(t *testing.T) {
		b := New(14, 14)
		b.RecordFailure(KindTransient)
		b.RecordFailure(KindTransient)
		b.mu.Lock()
		b.coolingExpires = time.Now().Add(-time.Second)
		b.mu.Unlock()
		if got := run(t, b); got != 1 {
			t.Fatalf("exactly one probe must be admitted after the cooling deadline, got %d", got)
		}
		if got := b.State(); got != StateHalfOpen {
			t.Fatalf("the breaker should be half_open, got %s", got)
		}
	})

	t.Run("closed breaker admits every caller", func(t *testing.T) {
		b := New(15, 15)
		if got := run(t, b); got != goroutines {
			t.Fatalf("a closed breaker must admit all %d callers, got %d", goroutines, got)
		}
	})
}

// ── ⑩ Manager.Stats() 的边界 ──────────────────────────────────────────

// TestManagerStatsBoundaries 钉住 ⑩：Manager.Stats()（:986-995）与
// Breaker.Stats()（:878-904）的边界形态。
//
// 四条判据：
//
//	· **零 breaker** ⇒ 返回空切片且**非 nil**（JSON 侧是 [] 而不是 null；
//	  管理面接口直接序列化它）
//	· 单 breaker 的**字段完整性**：6 个常驻字段恒在
//	· 条件字段按状态出现（失败/开路/恢复各有各的字段）
//	· 负控：从未失败过 ⇒ 条件字段一个都不许出现（干净凭据的读面不许带
//	  任何失败痕迹）
func TestManagerStatsBoundaries(t *testing.T) {
	t.Run("an empty manager returns an empty non-nil slice", func(t *testing.T) {
		m := NewManager()
		stats := m.Stats()
		if stats == nil {
			t.Fatal("Manager.Stats must return a non-nil slice (JSON [] not null)")
		}
		if len(stats) != 0 {
			t.Fatalf("expected 0 breakers, got %d", len(stats))
		}
	})

	t.Run("a clean breaker exposes only the always-present fields", func(t *testing.T) {
		m := NewManager()
		m.GetOrCreate(16, 16)
		stats := m.Stats()
		if len(stats) != 1 {
			t.Fatalf("expected 1 breaker, got %d", len(stats))
		}
		got := stats[0]
		for _, key := range []string{"key", "state", "consecutive_failures", "total_failures", "cooling_cycle", "free_tier"} {
			if _, ok := got[key]; !ok {
				t.Fatalf("stats missing the always-present field %q: %v", key, got)
			}
		}
		if got["key"] != "16/16" {
			t.Fatalf("stats key = %v, want %q", got["key"], "16/16")
		}
		if got["state"] != "closed" {
			t.Fatalf("stats state = %v, want %q", got["state"], "closed")
		}
		for _, key := range []string{"last_failure_at", "last_error_kind", "recovered_at", "open_since", "cooling_expires"} {
			if _, ok := got[key]; ok {
				t.Fatalf("a never-failed breaker must not expose %q: %v", key, got)
			}
		}
	})

	t.Run("an open breaker exposes the failure and cooling fields", func(t *testing.T) {
		m := NewManager()
		m.RecordFailure(17, 17, KindTransient)
		m.RecordFailure(17, 17, KindTransient)
		stats := m.Stats()
		if len(stats) != 1 {
			t.Fatalf("expected 1 breaker, got %d", len(stats))
		}
		got := stats[0]
		if got["state"] != "open" {
			t.Fatalf("stats state = %v, want %q", got["state"], "open")
		}
		for _, key := range []string{"last_failure_at", "last_error_kind", "open_since", "cooling_expires"} {
			if _, ok := got[key]; !ok {
				t.Fatalf("an open breaker must expose %q: %v", key, got)
			}
		}
		if got["last_error_kind"] != string(KindTransient) {
			t.Fatalf("stats last_error_kind = %v, want %q", got["last_error_kind"], string(KindTransient))
		}
		// 负控：翻红即作废旧恢复证据（:677）⇒ 开路态不许出现 recovered_at。
		if _, ok := got["recovered_at"]; ok {
			t.Fatalf("an open breaker must not expose a stale recovered_at: %v", got)
		}
	})

	t.Run("a recovered breaker exposes recovered_at", func(t *testing.T) {
		m := NewManager()
		m.RecordFailure(18, 18, KindTransient)
		m.RecordFailure(18, 18, KindTransient)
		m.RecordSuccess(18, 18)
		stats := m.Stats()
		if len(stats) != 1 {
			t.Fatalf("expected 1 breaker, got %d", len(stats))
		}
		got := stats[0]
		if _, ok := got["recovered_at"]; !ok {
			t.Fatalf("a recovered breaker must expose recovered_at: %v", got)
		}
		if got["state"] != "closed" {
			t.Fatalf("stats state = %v, want %q", got["state"], "closed")
		}
	})
}

// ── ⑪ State.String() ─────────────────────────────────────────────────

// TestStateStringQuarantinedAndUnknown 钉住 ⑪：State.String() 的 quarantined
// 与未知值分支（:43-49）。
//
// ★ 既有覆盖：credential_test.go:352 的 TestState_String 已经把 5 个取值
// （closed/open/half_open/quarantined/unknown）逐字钉过。本文件**不改**
// 那条既有测试，这里补的是它没覆盖的另一半：
//
//	· quarantined 标签**不得**与 open 混用（管理面按 state 字符串分流，
//	  两个标签混用会让「隔离=人工恢复」与「开路=自动恢复」的处理路径串台）
//	· 未知值（含负数）必须落到 "unknown" 而不是空串/数字（:49）——未知态
//	  一旦被序列化成别的形状，消费端的兜底分支就分不出来了
//	· String() 与 Stats() 读面同源（:884 用的是 State().String()）⇒ 隔离态
//	  在管理面读到的必须是 "quarantined"
func TestStateStringQuarantinedAndUnknown(t *testing.T) {
	cases := []struct {
		name  string
		state State
		want  string
	}{
		{name: "quarantined", state: StateQuarantined, want: "quarantined"},
		{name: "unknown high value", state: State(99), want: "unknown"},
		{name: "unknown negative value", state: State(-1), want: "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Fatalf("State(%d).String() = %q, want %q", tc.state, got, tc.want)
			}
		})
	}

	// 隔离标签不得与开路标签混用。
	if StateQuarantined.String() == StateOpen.String() {
		t.Fatal("quarantined and open must not share a label")
	}

	// 读面同源：隔离态在 Stats() 里必须显示为 "quarantined"。
	m := NewManager()
	m.RecordFailure(19, 19, KindQuota)
	m.RecordFailure(19, 19, KindQuota)
	stats := m.Stats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 breaker, got %d", len(stats))
	}
	if got := stats[0]["state"]; got != "quarantined" {
		t.Fatalf("stats state = %v, want %q", got, "quarantined")
	}
}
