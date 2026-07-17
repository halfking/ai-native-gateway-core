package bg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

// TestProbeSync_NilWorker verifies that calling ProbeSync on a nil
// receiver returns false without panicking.
func TestProbeSync_NilWorker(t *testing.T) {
	var w *NodeProbeWorker
	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 1, RawModel: "gpt-4"},
	}
	got := w.ProbeSync(context.Background(), cands, "default", "req-1")
	if got {
		t.Errorf("nil worker should return false, got true")
	}
}

// TestProbeSync_EmptyCandidates verifies the early-exit path.
func TestProbeSync_EmptyCandidates(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	cases := []struct {
		name string
		in   []credentialstate.NoCandidatesCandidate
	}{
		{"nil", nil},
		{"empty", []credentialstate.NoCandidatesCandidate{}},
		{"all-zero-cred-id", []credentialstate.NoCandidatesCandidate{
			{CredentialID: 0, RawModel: "gpt-4"},
			{CredentialID: 0, RawModel: "minimax-m3"},
		}},
		{"all-blank-model", []credentialstate.NoCandidatesCandidate{
			{CredentialID: 1, RawModel: ""},
			{CredentialID: 2, RawModel: "   "},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := w.ProbeSync(context.Background(), tc.in, "default", "req-1")
			if got {
				t.Errorf("ProbeSync(%s) returned true; expected false", tc.name)
			}
		})
	}
}

// TestProbeSync_DedupReuse verifies that two concurrent ProbeSync calls
// for the SAME (cred,model) pair both attach as syncWaiters instead of
// launching duplicate probes.
func TestProbeSync_DedupReuse(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}

	key := "42|gpt-4"
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()

	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 42, RawModel: "gpt-4"},
	}

	firstReturned := make(chan struct{})
	firstStart := time.Now()
	go func() {
		_ = w.ProbeSync(context.Background(), cands, "default", "req-A")
		close(firstReturned)
	}()

	time.Sleep(20 * time.Millisecond)

	w.syncWaitersMu.Lock()
	waiters := w.syncWaiters[key]
	w.syncWaitersMu.Unlock()
	if len(waiters) != 1 {
		t.Errorf("expected 1 waiter after first ProbeSync, got %d", len(waiters))
	}

	secondReturned := make(chan struct{})
	go func() {
		_ = w.ProbeSync(context.Background(), cands, "default", "req-B")
		close(secondReturned)
	}()
	time.Sleep(20 * time.Millisecond)

	w.syncWaitersMu.Lock()
	waiters = w.syncWaiters[key]
	w.syncWaitersMu.Unlock()
	if len(waiters) != 2 {
		t.Errorf("expected 2 waiters after second ProbeSync, got %d", len(waiters))
	}

	for _, ch := range waiters {
		close(ch)
	}

	select {
	case <-firstReturned:
	case <-time.After(1 * time.Second):
		t.Fatal("first ProbeSync did not return after waiters closed")
	}
	select {
	case <-secondReturned:
	case <-time.After(1 * time.Second):
		t.Fatal("second ProbeSync did not return after waiters closed")
	}

	if elapsed := time.Since(firstStart); elapsed > 500*time.Millisecond {
		t.Errorf("ProbeSync took %v; expected <500ms after waiter close", elapsed)
	}
}

// TestProbeSync_ContextCancelPropagatesToReuse verifies that a cancelled
// caller context aborts the wait without waiting for the in-flight probe.
func TestProbeSync_ContextCancelPropagatesToReuse(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}

	key := "7|minimax-m3"
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()

	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 7, RawModel: "minimax-m3"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		_ = w.ProbeSync(ctx, cands, "default", "req-cancel")
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ProbeSync did not return after ctx cancel")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("ProbeSync took %v after ctx cancel; expected <300ms", elapsed)
	}
}

// TestProbeSync_ContextCancelVsDeadline ensures that client cancel
// (context.Canceled) and deadline (context.DeadlineExceeded) both
// return false cleanly without panicking.
func TestProbeSync_ContextCancelVsDeadline(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}

	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 9, RawModel: "gpt-4o"},
	}

	w.mu.Lock()
	w.inFlight["9|gpt-4o"] = struct{}{}
	w.mu.Unlock()

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		got := w.ProbeSync(ctx, cands, "default", "req-c")
		if got {
			t.Error("expected false on cancel")
		}
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		got := w.ProbeSync(ctx, cands, "default", "req-d")
		if got {
			t.Error("expected false on deadline")
		}
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
		}
	})
}