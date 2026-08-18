package dispatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrdinaryDispatchRestartDoesNotResumeInFlightExecution(t *testing.T) {
	var forwardCalls atomic.Int32
	forwardStarted := make(chan struct{})
	releaseForward := make(chan struct{})

	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 1)}},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			if forwardCalls.Add(1) == 1 {
				close(forwardStarted)
				<-releaseForward
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}

	first := f.pipeline()
	first.Start()
	submitDone := make(chan struct{})
	go func() {
		_, _ = first.Submit(context.Background(), NewQueuedRequest("ordinary-inflight", "tenant-a", "m", context.Background(), nil))
		close(submitDone)
	}()
	select {
	case <-forwardStarted:
	case <-time.After(2 * time.Second):
		first.Stop()
		t.Fatal("ordinary dispatch did not reach in-flight forward")
	}

	stopDone := make(chan struct{})
	go func() {
		first.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		close(releaseForward)
		t.Fatal("Stop returned before the in-flight forward drained")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseForward)
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the forward was released")
	}
	<-submitDone
	if got := forwardCalls.Load(); got != 1 {
		t.Fatalf("first pipeline forward calls = %d, want 1", got)
	}

	second := f.pipeline()
	second.Start()
	defer second.Stop()
	select {
	case <-time.After(50 * time.Millisecond):
	}
	if got := forwardCalls.Load(); got != 1 {
		t.Fatalf("restart resumed ordinary dispatch: forward calls = %d", got)
	}
}
