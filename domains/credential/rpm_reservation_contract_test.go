// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES
package credential

// rpm_reservation_contract_test.go —— P1-3「预算预约-释放」的**现状契约门**。
//
// # 这道门为什么是「现状契约」而不是「实现 magpie 的三态」
//
// 计划书 §6-P1-3 要求「**先出结论再出码**」。本轮读完现状后的结论是：
//
//	**本仓没有 magpie 意义上的「key 级花费/令牌预算」**，因此
//	「预留—释放—拒绝」三态**不引入**（详见
//	docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md §3.9）。
//
// 但结论里有一句话是**必须被钉住的**：
//
//	「**RPM 名额一经预约，请求失败也不退还**（且这是故意的）」。
//
// 原因：RPM 限的是「**上游收到了多少请求**」—— 预约成功那一刻请求就会发出去，
// 之后上游调用失败也**已经打到了厂商** ⇒ 退还名额等于把限流悄悄放宽。
// ★ 这条如果没人守，将来一句「失败应该退还」的「优化」就会**静默地**把限流放松，
// **且没有任何测试会红**（现有测试只间接推断，没有直接观测窗口状态）。
//
// # 三条判据
//
//	G1 ★ **并发下不双花**：N 个 goroutine 同时抢同一 (provider, credential) 的
//	    RPM 名额 ⇒ **恰好** limit 个成功、其余全被拒（窗口是原子的）。
//	    ★ 这是全包**此前没有任何测试**覆盖的一格（既有并发测试只覆盖信号量层）。
//	G2 ★ **RPM 拒绝 ⇒ 每一个并发名额都归还**（global / pool / credential）。
//	    ★ 漏掉一个 ⇒ 每次被拒的候选都泄漏一个名额 ⇒ 全局并发会**慢慢饿死**
//	    （而且症状极难归因：表现为「流量低了却一直排队」）。
//	G3 ★ **失败不退还**：预约成功 → 执行方释放并发名额（模拟请求失败）⇒
//	    并发名额**确实还了**、而 RPM 名额**确实没还**。
//
// # 门与被测对象的关系（防「自证」）
//
// ★ 这道门是**包内测试**（package credential），因为它必须**直接观测** RPM 窗口的
// 内部状态（`MemoryRPMLimiter.shard(...).credsRPM[key].timestamps`）——
// 只从外部看「下一次是否被拒」是**间接推断**，推断不到「还了几个」。
// ★ 但**被测集合与期望都不来自被测对象**：limit、worker 数、期望的成功数
// 全部是本文件里的字面量 ⇒ 改实现改不掉断言。

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// newRPMLimiterContractLimiter 造一个**确定性**的 Limiter：
//
//	① 各层容量都是 1（最小可用 ⇒ 任何泄漏都会立刻显形）；
//	② **强制**使用内存 RPM 实现 —— 生产可能走 Redis
//	   （`NewRPMLimiterFromEnv` 会看 `RPM_REDIS_URL` / `LLM_GATEWAY_REDIS_ADDR`），
//	   ⇒ 若不强制，本门会**随环境变量**变红/变绿 ⇒ 那就不是门了，是环境的影子。
func newRPMLimiterContractLimiter(t *testing.T) *Limiter {
	t.Helper()
	l := NewWithLimits(1, 1, 1, 1)
	t.Cleanup(l.Stop)
	l.rpmLimiter = NewMemoryRPMLimiter()
	return l
}

// rpmWindowEntries 返回该 (provider, credential) 当前窗口内的预约条数。
//
// ★ 直接读内部状态（而不是「下一次是否被拒」）—— 后者只能间接推断。
func rpmWindowEntries(t *testing.T, l *Limiter, providerID, credentialID int) int {
	t.Helper()
	m, ok := l.rpmLimiter.(*MemoryRPMLimiter)
	if !ok {
		t.Fatalf("rpmLimiter 不是内存实现（门的环境不干净）：%T", l.rpmLimiter)
	}
	key := fmt.Sprintf("%d/%d", providerID, credentialID)
	s := m.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.credsRPM[key]
	if !ok {
		return 0
	}
	return len(w.timestamps)
}

// assertNoConcurrencySlotLeaked 断言三层并发名额都已归还。
func assertNoConcurrencySlotLeaked(t *testing.T, l *Limiter, providerID, credentialID int) {
	t.Helper()
	if got := l.global.Used(); got != 0 {
		t.Fatalf("global 名额泄漏 %d 个", got)
	}
	if got := l.Pool(providerID).Used(); got != 0 {
		t.Fatalf("pool 名额泄漏 %d 个", got)
	}
	if got := l.Credential(providerID, credentialID).Used(); got != 0 {
		t.Fatalf("credential 名额泄漏 %d 个", got)
	}
}

// TestRPMLimiterConcurrentReservationsDoNotDoubleSpend G1：
// 并发抢 RPM 名额 ⇒ **恰好** limit 个成功。
//
// ★ 这里的「恰好」是本门唯一的强断言：窗口不是原子的就会出现
// 「成功数 > limit」（双花）或「成功数 < limit」（名额被白白浪费）。
func TestRPMLimiterConcurrentReservationsDoNotDoubleSpend(t *testing.T) {
	l := newRPMLimiterContractLimiter(t)

	const limit = 3
	const workers = 64
	rpmLimit := limit

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	rejected := 0

	// ★ 全部 goroutine 同时起跑（否则并发窗口太窄，测不出竞态）。
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			release, err := l.AcquireAll(
				context.Background(), 1, 2, fmt.Sprintf("ident-%03d", i), 0, 0, &rpmLimit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rejected++
				return
			}
			allowed++
			release()
		}(i)
	}
	close(start)
	wg.Wait()

	if allowed != limit {
		t.Fatalf("并发下成功预约数 = %d，want %d（双花或名额浪费）", allowed, limit)
	}
	if rejected != workers-limit {
		t.Fatalf("被拒数 = %d，want %d（总数对不上 ⇒ 有 goroutine 因别的原因失败）",
			rejected, workers-limit)
	}
	// 窗口里应当正好躺着 limit 条。
	if got := rpmWindowEntries(t, l, 1, 2); got != limit {
		t.Fatalf("窗口条目数 = %d，want %d", got, limit)
	}
	// ★ 并发名额不能因为拒绝路径而泄漏。
	assertNoConcurrencySlotLeaked(t, l, 1, 2)
}

// TestRPMLimiterRejectionReleasesEveryConcurrencySlot G2：
// RPM 拒绝 ⇒ **每一个**已占用的并发名额都要归还。
func TestRPMLimiterRejectionReleasesEveryConcurrencySlot(t *testing.T) {
	l := newRPMLimiterContractLimiter(t)
	rpmLimit := 1

	release, err := l.AcquireAll(context.Background(), 1, 2, "ident-a", 0, 0, &rpmLimit)
	if err != nil {
		t.Fatalf("第一次预约应成功：%v", err)
	}
	release()

	// 第二次必然被 RPM 拒绝（窗口里已经有一条）。
	_, err = l.AcquireAll(context.Background(), 1, 2, "ident-b", 0, 0, &rpmLimit)
	if err == nil {
		t.Fatal("第二次预约应被 RPM 拒绝")
	}

	assertNoConcurrencySlotLeaked(t, l, 1, 2)
}

// TestRPMLimiterReservationIsNotRefundedWhenRequestFails G3：
// **请求失败也不退还 RPM 名额**（而并发名额照还）。
//
// ★ 这条是「现状契约」：它把「失败不退还」从「现有测试的间接推断」变成
// **直接观测** ⇒ 将来若有人加一个退款路径，本门会当场红，
// 而不是「悄悄把限流放松、且没有任何测试会红」。
func TestRPMLimiterReservationIsNotRefundedWhenRequestFails(t *testing.T) {
	l := newRPMLimiterContractLimiter(t)
	rpmLimit := 1

	release, err := l.AcquireAll(context.Background(), 1, 2, "ident-a", 0, 0, &rpmLimit)
	if err != nil {
		t.Fatalf("第一次预约应成功：%v", err)
	}
	if got := rpmWindowEntries(t, l, 1, 2); got != 1 {
		t.Fatalf("预约后窗口条目 = %d，want 1", got)
	}

	// 模拟「请求失败」：执行方照样调用 release（释放并发名额）。
	release()

	// ① 并发名额确实还了。
	assertNoConcurrencySlotLeaked(t, l, 1, 2)
	// ② ★ RPM 名额**没有**还 —— 预约那一刻请求就会发出去，
	//    上游失败时它**已经打到了厂商** ⇒ 退还会把限流悄悄放宽。
	if got := rpmWindowEntries(t, l, 1, 2); got != 1 {
		t.Fatalf("请求失败后 RPM 名额被退还了（窗口条目 = %d，want 1）⇒ "+
			"限流被悄悄放宽；★ 若确实要改成退还，必须同时改这条契约与计划书的 P1-3 结论",
			got)
	}
}
