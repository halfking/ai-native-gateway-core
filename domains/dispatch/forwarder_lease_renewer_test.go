package dispatch

// forwarder_lease_renewer_test.go — R73 §3 #6 收口的 forwarder 级集成钉测：
// attempt() 持有 LeaseRenewer 型 governor 时启动续约循环；确定丢失把
// forwardFunc 的 ctx 取消（fail-closed），且续约严格先于释放停止。
// 非 LeaseRenewer governor 走原路径，行为不变。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestAttemptLeaseLostAbortsForwardFailClosed 走真实 attempt()：forwardFunc
// 阻塞在 ctx.Done 上，续约第一次就返回确定丢失 → fwdCtx 被取消、forward
// 以 ctx 错误返回（pre-first-byte 失败路径）。变异验证：废掉 abort 接线
// 本测试红。
func TestAttemptLeaseLostAbortsForwardFailClosed(t *testing.T) {
	g := &fakeLeaseGovernor{renewErr: func(int) error {
		return fmt.Errorf("%w: %w: expired", ErrLeaseLost, ErrGovernorUnavailable)
	}}

	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}, forwardCalls: map[int]int{}}
	entered := make(chan struct{})
	forwardAborted := make(chan struct{})
	f.forwardFn = func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
		close(entered)
		<-ctx.Done()
		close(forwardAborted)
		return ForwardOutcome{Err: ctx.Err()}
	}
	p := f.pipeline()
	defer p.Stop()

	cred := CredentialRef{CredentialID: 9, ProviderID: 3, Vendor: "fake", ConcurrencyMode: ModeConcurrency}
	cf := &credForwarder{cred: cred, gov: g, pipe: p}

	qrCtx, qrCancel := context.WithCancel(context.Background())
	defer qrCancel()
	qr := NewQueuedRequest("lease-lost-attempt", "t", "gpt4", qrCtx, "payload")
	ref := qr.reserveAttempt(cred)
	if _, ok := qr.commitReservedAttempt(ref.AttemptID); !ok {
		t.Fatal("commitReservedAttempt failed")
	}

	cf.wg.Add(1)
	go cf.attempt(qr, g)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardFunc never entered")
	}

	select {
	case <-forwardAborted:
	case <-time.After(2 * time.Second):
		t.Fatal("forward ctx was not canceled on definitive lease loss — fail-closed missing")
	}

	// 释放必须发生（slot 归还），且释放后续约已停：轮询到 released==1，
	// 再过 ≥3 个续约周期确认 renewals 不再增长。
	waitForWithin(t, "Release after abort", time.Second, func() bool {
		_, rel := g.count()
		return rel == 1
	})
	n1, _ := g.count()
	time.Sleep(25 * time.Millisecond)
	n2, _ := g.count()
	if n2 != n1 {
		t.Fatalf("renewals kept running after release: %d → %d", n1, n2)
	}

	// fail-closed 观测面：lease-lost 计数器至少 +1。
	if lost := leaseLostMetric(t); lost < 1 {
		t.Fatalf("dispatch_governor_lease_lost_total = %v, want ≥1", lost)
	}
}

// TestAttemptNonRenewerGovernorForwardCompletes 钉住非租约 governor 的
// 回归边界：无续约循环、forward 成功返回、正常 complete。
func TestAttemptNonRenewerGovernorForwardCompletes(t *testing.T) {
	gov := newConcurrencyGovernor(2)
	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}, forwardCalls: map[int]int{}}
	done := make(chan struct{})
	f.forwardFn = func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
		// 若 ctx 在 forward 前就被取消（本不该发生），这里可观测。
		if err := ctx.Err(); err != nil {
			t.Errorf("forward ctx already canceled: %v", err)
		}
		close(done)
		return ForwardOutcome{}
	}
	p := f.pipeline()
	defer p.Stop()

	cred := CredentialRef{CredentialID: 10, ProviderID: 3, Vendor: "fake", ConcurrencyMode: ModeConcurrency}
	cf := &credForwarder{cred: cred, gov: gov, pipe: p}

	qr := NewQueuedRequest("plain-attempt", "t", "gpt4", context.Background(), "payload")
	ref := qr.reserveAttempt(cred)
	if _, ok := qr.commitReservedAttempt(ref.AttemptID); !ok {
		t.Fatal("commitReservedAttempt failed")
	}

	cf.wg.Add(1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		cf.attempt(qr, gov)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardFunc never ran")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt did not return after successful forward")
	}
}
