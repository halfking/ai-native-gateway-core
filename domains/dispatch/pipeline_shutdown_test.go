package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubmitAfterStopReturnsShutdown pins the audit-2026-08-13 D5 fix:
// after Stop, a Submit that races past the entry check must be refused
// admission with ErrShutdown (previously getOrCreateModelQueue handed back
// a throwaway cap-1 queue whose send "succeeded" into a channel nobody
// drained, so Submit blocked forever on qr.ResultCh).
func TestSubmitAfterStopReturnsShutdown(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DispatcherWorkers = 1
	cfg.MaxQueueDepth = 8
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
		HotCfg:           hotCfg,
	})
	p.Start()
	p.Stop()

	qr := NewQueuedRequest("late", "t", "m", context.Background(), nil)
	done := make(chan error, 1)
	go func() {
		_, err := p.Submit(context.Background(), qr)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdown) {
			t.Fatalf("Submit after Stop = %v, want ErrShutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit after Stop blocked forever (throwaway-queue admission bug D5)")
	}
}

// TestStopWaitsForInFlightForward pins the audit-2026-08-13 D3 fix: the
// per-credential forwarder loop goroutine is tracked in the pipeline-wide
// WaitGroup, so Stop only returns after in-flight forward attempts have
// finished. Previously Stop cancelled the forwarder but p.wg.Wait()
// returned immediately, leaving attempts running across Stop.
func TestStopWaitsForInFlightForward(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DispatcherWorkers = 1
	cfg.MaxQueueDepth = 8
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	forwardStarted := make(chan struct{})
	releaseForward := make(chan struct{})
	forwardDone := make(chan struct{})

	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			close(forwardStarted)
			<-releaseForward
			close(forwardDone)
			return ForwardOutcome{}
		},
		AllowModelChange: false,
		HotCfg:           hotCfg,
	})
	p.Start()

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_, _ = p.Submit(context.Background(), NewQueuedRequest("inflight", "t", "m", context.Background(), nil))
	}()

	select {
	case <-forwardStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("forward never started")
	}

	stopDone := make(chan struct{})
	go func() {
		p.Stop()
		close(stopDone)
	}()

	// Stop must still be waiting while the attempt is parked.
	select {
	case <-stopDone:
		close(releaseForward)
		t.Fatal("Stop returned while a forward attempt was still in flight (D3)")
	case <-forwardDone:
		close(releaseForward)
	case <-time.After(500 * time.Millisecond):
		close(releaseForward)
	}

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after forwards drained")
	}
	<-subDone
}

// TestNoForwarderSpawnedAfterStop pins the audit-2026-08-13 D4 fix:
// getOrCreateForwarder refuses to spawn a forwarder after shutdown, so no
// goroutine with an uncancellable context.Background() leaks. tryEnqueueCred
// reports failed admission via the "shutdown" overflow metric.
func TestNoForwarderSpawnedAfterStop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DispatcherWorkers = 1
	cfg.MaxQueueDepth = 8
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
		HotCfg:           hotCfg,
	})
	p.Start()
	p.Stop()

	if got := p.getOrCreateForwarder(cred(42, ModeConcurrency, 1)); got != nil {
		t.Fatalf("getOrCreateForwarder after Stop = %v, want nil (D4 leak)", got)
	}
	if p.tryEnqueueCred(cred(42, ModeConcurrency, 1), NewQueuedRequest("x", "t", "m", context.Background(), nil)) {
		t.Fatal("tryEnqueueCred after Stop = true, want admission refused")
	}
}
