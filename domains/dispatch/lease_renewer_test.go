package dispatch

// lease_renewer_test.go — R73 §3 #6 收口钉测（round 31）：redis_enforce 的
// TTL 准入租约在长流场景下由 forwarder 侧续约循环保活，续约失败按既定
// 策略 fail-closed（确定丢失立即中止；瞬态故障超过一个整 TTL 未恢复同样
// 中止）。同时钉住：非租约型 governor 不启动循环、停止先于释放。
//
// 钉测承重口径与 R30 预算收口一致：变异验证（废掉 fail-closed → 红 →
// 还原 → 绿）由主代理在提交前实跑。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// fakeLeaseGovernor 是 Governor + LeaseRenewer 的可编程测试替身：Renew
// 行为由 renewErr(call) 决定（call 从 1 起），每次调用向 renewedCh 发信号。
type fakeLeaseGovernor struct {
	mu        sync.Mutex
	renewals  int
	released  int
	renewErr  func(call int) error
	renewedCh chan int
}

func (g *fakeLeaseGovernor) Mode() string { return ModeConcurrency }
func (g *fakeLeaseGovernor) Acquire(context.Context, *QueuedRequest, time.Time) error {
	return nil
}
func (g *fakeLeaseGovernor) Release(*QueuedRequest) {
	g.mu.Lock()
	g.released++
	g.mu.Unlock()
}
func (g *fakeLeaseGovernor) RenewInterval() time.Duration { return 5 * time.Millisecond }
func (g *fakeLeaseGovernor) Renew(_ context.Context, _ *QueuedRequest) error {
	g.mu.Lock()
	g.renewals++
	call := g.renewals
	g.mu.Unlock()
	if g.renewedCh != nil {
		select {
		case g.renewedCh <- call:
		default:
		}
	}
	if g.renewErr == nil {
		return nil
	}
	return g.renewErr(call)
}

func (g *fakeLeaseGovernor) count() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.renewals, g.released
}

var errTransientRenew = fmt.Errorf("%w: miniredis down", ErrGovernorUnavailable)
var errDefinitiveRenew = fmt.Errorf("%w: %w: expired", ErrLeaseLost, ErrGovernorUnavailable)

func runRenewLoop(t *testing.T, g *fakeLeaseGovernor) (fwdCtx context.Context, aborted chan struct{}, stop func()) {
	t.Helper()
	cf := &credForwarder{cred: CredentialRef{CredentialID: 1}, pipe: nil}
	ctx, cancel := context.WithCancel(context.Background())
	aborted = make(chan struct{})
	stopCh := make(chan struct{})
	stopped := make(chan struct{})
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
	return ctx, aborted, stop
}

func waitForWithin(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLeaseRenewLoopRenewsUntilStopped(t *testing.T) {
	g := &fakeLeaseGovernor{}
	fwdCtx, aborted, stop := runRenewLoop(t, g)

	waitForWithin(t, "at least 3 renewals", time.Second, func() bool {
		n, _ := g.count()
		return n >= 3
	})
	if err := fwdCtx.Err(); err != nil {
		t.Fatalf("fwdCtx canceled without lease loss: %v", err)
	}
	stop()
	select {
	case <-aborted:
		t.Fatal("happy-path renewal must not abort")
	default:
	}
}

func TestLeaseRenewLoopFailClosedOnDefinitiveLeaseLoss(t *testing.T) {
	g := &fakeLeaseGovernor{renewErr: func(int) error { return errDefinitiveRenew }}
	fwdCtx, aborted, stop := runRenewLoop(t, g)
	defer stop()

	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("definitive lease loss must abort the attempt (fail-closed)")
	}
	if err := fwdCtx.Err(); err == nil {
		t.Fatal("abort must cancel the forward context")
	}
	// 确定丢失走一次 Renew 即中止，不再盲等。
	if n, _ := g.count(); n != 1 {
		t.Fatalf("renewals = %d, want exactly 1 (abort on first definitive loss)", n)
	}
}

func TestLeaseRenewLoopFailClosedAfterTTLWithoutRenewal(t *testing.T) {
	g := &fakeLeaseGovernor{renewErr: func(int) error { return errTransientRenew }}
	fwdCtx, aborted, stop := runRenewLoop(t, g)
	defer stop()

	// interval=5ms ⇒ TTL 窗口=15ms：t=5/10/15ms 三次瞬态失败，第 4 拍
	// （t≈20ms）elapsed≥TTL → fail-closed。1s 上限防挂死。
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("transient degradation past one full lease TTL must abort (fail-closed)")
	}
	if err := fwdCtx.Err(); err == nil {
		t.Fatal("abort must cancel the forward context")
	}
	if n, _ := g.count(); n < 3 {
		t.Fatalf("renewals = %d, want ≥3 before the TTL window closes", n)
	}
}

func TestLeaseRenewLoopTransientRecoveryResetsDegradation(t *testing.T) {
	g := &fakeLeaseGovernor{renewErr: func(call int) error {
		if call <= 2 {
			return errTransientRenew
		}
		return nil
	}}
	_, aborted, stop := runRenewLoop(t, g)

	waitForWithin(t, "at least 6 renewals (2 failed + recovery)", time.Second, func() bool {
		n, _ := g.count()
		return n >= 6
	})
	select {
	case <-aborted:
		t.Fatal("recovery within the TTL window must reset degradation; abort fired")
	default:
	}
	stop()
}

func TestRedisEnforceGovernorImplementsLeaseRenewer(t *testing.T) {
	g := &redisEnforceGovernor{ttl: 30 * time.Second}
	renewer, ok := Governor(g).(LeaseRenewer)
	if !ok {
		t.Fatal("redisEnforceGovernor must implement LeaseRenewer (R73 §3 #6 wiring)")
	}
	if got := renewer.RenewInterval(); got != 10*time.Second {
		t.Fatalf("RenewInterval = %v, want ttl/3 = 10s", got)
	}
	// 非 TTL 型 governor 不得实现该能力：本地并发/速率桶没有租约语义。
	if _, ok := Governor(newConcurrencyGovernor(2)).(LeaseRenewer); ok {
		t.Fatal("concurrency governor must not implement LeaseRenewer")
	}
	if _, ok := Governor(newRPMGovernor(60)).(LeaseRenewer); ok {
		t.Fatal("rpm governor must not implement LeaseRenewer")
	}
	if _, ok := Governor(newNoopGovernor()).(LeaseRenewer); ok {
		t.Fatal("noop governor must not implement LeaseRenewer")
	}
}

func TestRenewErrorContractOnRealGovernor(t *testing.T) {
	// 契约钉：Renew 的「确定丢失」必须同时可被 ErrLeaseLost 与
	// ErrGovernorUnavailable 识别（后者保住既有 fail-closed 分类）。
	if !errors.Is(errDefinitiveRenew, ErrLeaseLost) ||
		!errors.Is(errDefinitiveRenew, ErrGovernorUnavailable) {
		t.Fatal("definitive-loss error must wrap both ErrLeaseLost and ErrGovernorUnavailable")
	}
	if errors.Is(errTransientRenew, ErrLeaseLost) {
		t.Fatal("transient error must NOT wrap ErrLeaseLost")
	}
}

// TestStartLeaseRenewerNoopForPlainGovernor 钉住能力发现：非租约 governor
// 返回 nil stop，attempt 的释放路径不付任何开销。
func TestStartLeaseRenewerNoopForPlainGovernor(t *testing.T) {
	cf := &credForwarder{cred: CredentialRef{CredentialID: 1}, pipe: nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stop := cf.startLeaseRenewer(ctx, cancel, newConcurrencyGovernor(1), &QueuedRequest{}); stop != nil {
		stop()
		t.Fatal("plain governor must yield nil stop (no renewal loop)")
	}
}

func leaseLostMetric(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := metricGovernorLeaseLost.Write(m); err != nil {
		t.Fatalf("read lease-lost metric: %v", err)
	}
	return m.GetCounter().GetValue()
}
