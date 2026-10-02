package dispatch

// lease_renewer_lastarmed_test.go — R32（2026-10-02，P2-C 钉测）：lastArmed
// 的 fail-closed 截止语义进 CI 确定性承重。此前该语义只有轮 31 文档记载的
// 一次性手工变异背书（12h 审计 P2-C：回退 degradedAt / 冻结 lastArmed 两个
// 变异体对既有 4 测试全绿——零 CI 承重）。
//
// 时钟缝：credForwarder.now（生产 nil → time.Now）。测试在续约拍之间同步
// 推进手动时钟，使「自锚起算是否越过 TTL」与真实节拍解耦：节拍只决定
// 「哪一拍看到越线」，假时钟决定「是否越线」。
//
// 三变异隔离面（全部应红）：
//   M3 撤整 TTL 门（首失败即中止）→ T1/T2/T3 的「TTL 内不中止」断言红；
//   M1 冻结 lastArmed（成功后不重置，锚停在入口）→ T2/T3 的观察窗红
//      （入口起算已 ≥ TTL，正确实现按末次成功锚安静）；
//   M2 截止回退为首失败起算（degradedAt 语义）→ T3 专属：末次成功与
//      首次失败之间的假时间差（8ms）撑出窗 (S+TTL, F+TTL)，推进落进窗内
//      时正确实现中止、M2 安静 → 红。

import (
	"context"
	"sync"
	"testing"
	"time"
)

// manualClock 是续约循环的手动时钟。
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// runRenewLoopWithClock 与 runRenewLoop 同形，但注入手动时钟并返回缓冲的
// 续约信号通道（逐拍消费 → 「第 n 拍」可寻址）。
func runRenewLoopWithClock(t *testing.T, g *fakeLeaseGovernor, mc *manualClock) (fwdCtx context.Context, aborted chan struct{}, renewed <-chan int, stop func()) {
	t.Helper()
	cf := &credForwarder{cred: CredentialRef{CredentialID: 1}, pipe: nil, now: mc.Now}
	ctx, cancel := context.WithCancel(context.Background())
	aborted = make(chan struct{})
	stopCh := make(chan struct{})
	stopped := make(chan struct{})
	renewedCh := make(chan int, 256)
	g.renewedCh = renewedCh
	go func() {
		defer close(stopped)
		cf.leaseRenewLoop(ctx, stopCh, g, &QueuedRequest{}, 5*time.Millisecond, func() {
			select {
			case <-aborted:
			default:
				close(aborted)
			}
			cancel()
		})
	}()
	stop = func() {
		close(stopCh)
		<-stopped
		cancel()
	}
	return ctx, aborted, renewedCh, stop
}

// waitForCall 消费直到第 n 拍完成（缓冲通道保证信号不丢）。
func waitForCall(t *testing.T, renewed <-chan int, n int) {
	t.Helper()
	for {
		select {
		case c := <-renewed:
			if c == n {
				return
			}
			if c > n {
				t.Fatalf("renewal call = %d, want %d (signal gap)", c, n)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for renewal call %d", n)
		}
	}
}

func assertNotAborted(t *testing.T, aborted chan struct{}, what string) {
	t.Helper()
	select {
	case <-aborted:
		t.Fatalf("abort fired while lease still armed (%s)", what)
	case <-time.After(30 * time.Millisecond):
	}
}

func assertAborted(t *testing.T, aborted chan struct{}, what string) {
	t.Helper()
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected fail-closed abort (%s)", what)
	}
}

// T1：入口 Acquire 即武装 + 整 TTL 门。全程失败、假时钟近静止 → 前 2 拍
// 不得中止（M3 红）；推进 +TTL 后 → 下一拍必须中止。
func TestLastArmed_EntryArmThenTTLDeadline(t *testing.T) {
	g := &fakeLeaseGovernor{renewErr: func(int) error { return errTransientRenew }}
	mc := newManualClock()
	fwdCtx, aborted, renewed, stop := runRenewLoopWithClock(t, g, mc)
	defer stop()

	waitForCall(t, renewed, 2)
	assertNotAborted(t, aborted, "T1: within TTL of entry Acquire")

	mc.Advance(16 * time.Millisecond) // interval=5ms ⇒ TTL=15ms
	waitForCall(t, renewed, 3)
	assertAborted(t, aborted, "T1: TTL elapsed since entry Acquire")
	if err := fwdCtx.Err(); err == nil {
		t.Fatal("abort must cancel the forward context")
	}
}

// T2：成功重置锚（隔离 M1 冻结）。拍 1-5 成功、每拍之间 +4ms（入口起算
// 逐拍累加：拍 5 成功落在 20ms，已 ≥ TTL，而正确锚=拍 5 距今 0ms）；拍 6
// 首败（不推进）→ 不得中止（M1 以入口为锚 20ms ≥ TTL 在此中止 → 红；
// M3 同红）；再推进 +16ms 越过拍 5 锚的 TTL → 下一拍中止。
func TestLastArmed_SuccessResetsDeadline(t *testing.T) {
	g := &fakeLeaseGovernor{}
	mc := newManualClock()
	_, aborted, renewed, stop := runRenewLoopWithClock(t, g, mc)
	defer stop()

	for i := 1; i <= 5; i++ {
		waitForCall(t, renewed, i)
		mc.Advance(4 * time.Millisecond) // 成功拍逐个落在 4/8/12/16/20ms
	}

	g.setRenewErr(func(int) error { return errTransientRenew })
	waitForCall(t, renewed, 6)
	assertNotAborted(t, aborted, "T2: within TTL of last successful renewal (M1/M3 isolation)")

	mc.Advance(16 * time.Millisecond)
	assertAborted(t, aborted, "T2: TTL elapsed since last successful arm")
}

// T3：锚是末次成功而非首次失败（隔离 M2 degradedAt 回退）。拍 1-3 成功、
// 逐拍 +4ms（S=拍 3 成功时刻=T0+12ms）；推进 +8ms 后拍 4 首败（F=T0+20ms）
// → 正确锚距 8ms < TTL 安静（M1 入口锚 20ms ≥ TTL 在此中止 → 红；M3 红）；
// 推进 +10ms（now=T0+30ms）落进窗 (S+TTL, F+TTL)=(T0+27, T0+35)——正确实现
// 距 S 18ms ≥ TTL → 下一拍中止；M2 距 F 仅 10ms < TTL → 安静 → 红。
func TestLastArmed_AnchorIsLastSuccessNotFirstFailure(t *testing.T) {
	g := &fakeLeaseGovernor{}
	mc := newManualClock()
	_, aborted, renewed, stop := runRenewLoopWithClock(t, g, mc)
	defer stop()

	for i := 1; i <= 3; i++ {
		waitForCall(t, renewed, i)
		mc.Advance(4 * time.Millisecond) // 成功拍逐个落在 4/8/12ms
	}
	mc.Advance(8 * time.Millisecond)

	g.setRenewErr(func(int) error { return errTransientRenew })
	waitForCall(t, renewed, 4)
	assertNotAborted(t, aborted, "T3: first failure within TTL of last success (M1/M3 isolation)")

	mc.Advance(10 * time.Millisecond)
	assertAborted(t, aborted, "T3: TTL elapsed since last success (M2 isolation)")
}
