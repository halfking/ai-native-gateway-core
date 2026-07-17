package bg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

// fakeStateProvider is a controllable StateProvider for ProbeSync tests.
// IsAvailable returns whatever the test set via setAvailable.
type fakeStateProvider struct {
	mu        sync.Mutex
	available map[string]bool // key: "<credID>|<model>"
	enabled   bool
	calls     int
}

func newFakeStateProvider() *fakeStateProvider {
	return &fakeStateProvider{available: map[string]bool{}, enabled: true}
}

func (f *fakeStateProvider) setAvailable(credID int, model string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.available[fmt.Sprintf("%d|%s", credID, model)] = ok
}

func (f *fakeStateProvider) GetState(_ context.Context, _ int, _ string) (*credentialstate.State, error) {
	return nil, nil
}

func (f *fakeStateProvider) IsAvailable(_ context.Context, credID int, model string) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.available[fmt.Sprintf("%d|%s", credID, model)], ""
}

func (f *fakeStateProvider) Enabled() bool { return f.enabled }

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

// TestProbeSync_ReusePathDetectsRecovery verifies the 2026-07-17 audit
// fix: when a ProbeSync caller attaches as a syncWaiter (because the
// (cred,model) was already in-flight), and the in-flight probe then
// marks the pair available, the waiting caller must observe recovery
// (return true) rather than fall through to a spurious 503.
//
// Before the fix the reuse path always returned false even when
// cycle() / another fresh-job had written availability=true.
func TestProbeSync_ReusePathDetectsRecovery(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	fp := newFakeStateProvider()
	w.stateProvider = fp

	// Simulate a background cycle() that already started probing cred=11.
	const credID = 11
	const model = "glm-5.2"
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()

	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: credID, RawModel: model},
	}

	// Start ProbeSync — it should attach a reuse waiter and block.
	gotCh := make(chan bool, 1)
	go func() {
		gotCh <- w.ProbeSync(context.Background(), cands, "default", "req-reuse")
	}()

	// Give it time to attach the waiter.
	time.Sleep(20 * time.Millisecond)

	// Simulate cycle() finishing: mark available in the state cache,
	// then notify waiters (this is what the production code does in
	// cycle() after runOne completes).
	fp.setAvailable(credID, model, true)
	w.notifySyncWaiters(key)

	select {
	case got := <-gotCh:
		if !got {
			t.Fatal("reuse-path ProbeSync returned false after cycle marked pair available; expected true")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("reuse-path ProbeSync did not return after notifySyncWaiters")
	}

	// Sanity: IsAvailable was actually consulted (otherwise the fix is
	// not exercised and the test would pass for the wrong reason).
	if fp.calls == 0 {
		t.Error("StateProvider.IsAvailable was never consulted on the reuse path")
	}
}

// TestProbeSync_ReusePathStillReturnsFalseWhenStillUnavailable verifies
// the fix does NOT over-fire: if the in-flight probe finishes and the
// pair is still unavailable, reuse-path callers still get false.
func TestProbeSync_ReusePathStillReturnsFalseWhenStillUnavailable(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	fp := newFakeStateProvider()
	w.stateProvider = fp

	const credID = 12
	const model = "minimax-m3"
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()

	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: credID, RawModel: model},
	}

	gotCh := make(chan bool, 1)
	go func() {
		gotCh <- w.ProbeSync(context.Background(), cands, "default", "req-still-down")
	}()
	time.Sleep(20 * time.Millisecond)

	// Pair stays unavailable; just notify (cycle finished but probe failed).
	fp.setAvailable(credID, model, false)
	w.notifySyncWaiters(key)

	select {
	case got := <-gotCh:
		if got {
			t.Fatal("reuse-path ProbeSync returned true when pair still unavailable; expected false")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("reuse-path ProbeSync did not return after notifySyncWaiters")
	}
}

// TestProbeSync_FreshJobNotifiesWaiters verifies the 2026-07-17 audit
// fix for the FRESH path: when a ProbeSync caller reserves the inFlight
// slot itself and runs probeDirect/probeGateway, any concurrent caller
// that attached as a syncWaiter must be released by the fresh-job's
// defer-notifySyncWaiters — NOT by burning its ctx budget.
//
// We can't run a real probeDirect (needs DB + network), so we stub
// probeDirect/probeGateway by intercepting at the stateProvider level:
// the fresh-job path will run probeDirect and likely return ok=false
// (no DB), but the key assertion is that the reuse waiter's channel
// gets closed promptly so it doesn't wait for ctx.
//
// Since we cannot easily monkeypatch probeDirect, this test instead
// drives the inFlight + syncWaiters plumbing directly to assert the
// notifySyncWaiters helper does the right thing for both readers.
func TestProbeSync_FreshJobNotifiesWaiters(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	const credID = 13
	const model = "gpt-5.6-luna"
	key := fmt.Sprintf("%d|%s", credID, model)

	// Attach two waiters as if two concurrent ProbeSync callers arrived
	// while cycle() / a fresh job owned the slot.
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()
	ch1 := make(chan struct{})
	ch2 := make(chan struct{})
	w.syncWaitersMu.Lock()
	w.syncWaiters[key] = append(w.syncWaiters[key], ch1, ch2)
	w.syncWaitersMu.Unlock()

	// notifySyncWaiters must close BOTH channels and clear the map entry.
	w.notifySyncWaiters(key)

	for i, ch := range []chan struct{}{ch1, ch2} {
		select {
		case <-ch:
		default:
			t.Errorf("waiter %d was not closed by notifySyncWaiters", i)
		}
	}

	// Map entry should be cleared so a later notify is a no-op.
	w.syncWaitersMu.Lock()
	remaining := len(w.syncWaiters[key])
	w.syncWaitersMu.Unlock()
	if remaining != 0 {
		t.Errorf("syncWaiters[key] not cleared; got %d entries", remaining)
	}

	// Second notify must not panic on a nil/empty slice.
	w.notifySyncWaiters(key) // idempotent
}