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
	// then release the slot + notify waiters (this is what production
	// cycle() does via finishProbe after runOne completes).
	fp.setAvailable(credID, model, true)
	w.finishProbe(key)

	select {
	case got := <-gotCh:
		if !got {
			t.Fatal("reuse-path ProbeSync returned false after cycle marked pair available; expected true")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("reuse-path ProbeSync did not return after finishProbe")
	}

	// finishProbe must have released the slot: a retrying caller would
	// otherwise re-register a waiter that can never be closed.
	w.mu.Lock()
	_, stillBusy := w.inFlight[key]
	w.mu.Unlock()
	if stillBusy {
		t.Fatal("finishProbe left the inFlight slot held; a retry would attach a never-closed waiter")
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

	// Pair stays unavailable; just release the slot + notify (cycle
	// finished but the probe failed).
	fp.setAvailable(credID, model, false)
	w.finishProbe(key)

	select {
	case got := <-gotCh:
		if got {
			t.Fatal("reuse-path ProbeSync returned true when pair still unavailable; expected false")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("reuse-path ProbeSync did not return after finishProbe")
	}
}

// TestProbeSync_FreshJobNotifiesWaiters verifies the 2026-07-17 audit
// fix for the FRESH path: when a ProbeSync caller reserves the inFlight
// slot itself and runs probeDirect/probeGateway, any concurrent caller
// that attached as a syncWaiter must be released by the fresh-job's
// defer-finishProbe — NOT by burning its ctx budget.
//
// We can't run a real probeDirect (needs DB + network), so we stub
// probeDirect/probeGateway by intercepting at the stateProvider level:
// the fresh-job path will run probeDirect and likely return ok=false
// (no DB), but the key assertion is that the reuse waiter's channel
// gets closed promptly so it doesn't wait for ctx.
//
// Since we cannot easily monkeypatch probeDirect, this test instead
// drives the inFlight + syncWaiters plumbing directly to assert the
// finishProbe helper does the right thing for both readers.
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

	// finishProbe must close BOTH channels and clear the map entry.
	w.finishProbe(key)

	for i, ch := range []chan struct{}{ch1, ch2} {
		select {
		case <-ch:
		default:
			t.Errorf("waiter %d was not closed by finishProbe", i)
		}
	}

	// Map entry should be cleared so a later notify is a no-op.
	w.syncWaitersMu.Lock()
	remaining := len(w.syncWaiters[key])
	w.syncWaitersMu.Unlock()
	if remaining != 0 {
		t.Errorf("syncWaiters[key] not cleared; got %d entries", remaining)
	}

	// Second finishProbe must not panic on a nil/empty slice.
	w.finishProbe(key) // idempotent
}

// TestProbeSync_FinishProbeReleasesSlotAndNotifies pins the contract of
// finishProbe — the single completion hook shared by cycle() and the
// fresh-job goroutine. 2026-08-07 fix: the slot MUST be released and the
// waiter channels MUST be closed, so a retrying caller finds the slot
// free (fresh path) instead of registering a waiter that can never be
// closed.
func TestProbeSync_FinishProbeReleasesSlotAndNotifies(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	const credID = 14
	const model = "deepseek-v4-flash"
	key := fmt.Sprintf("%d|%s", credID, model)

	// Simulate an in-flight probe owned by cycle() or a fresh job.
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()
	ch1 := make(chan struct{})
	ch2 := make(chan struct{})
	w.syncWaitersMu.Lock()
	w.syncWaiters[key] = append(w.syncWaiters[key], ch1, ch2)
	w.syncWaitersMu.Unlock()

	w.finishProbe(key)

	for i, ch := range []chan struct{}{ch1, ch2} {
		select {
		case <-ch:
		default:
			t.Errorf("waiter %d was not closed by finishProbe", i)
		}
	}

	w.mu.Lock()
	_, stillBusy := w.inFlight[key]
	w.mu.Unlock()
	if stillBusy {
		t.Error("finishProbe did not release the inFlight slot; a retrying caller would register a never-closed waiter")
	}

	w.syncWaitersMu.Lock()
	remaining := len(w.syncWaiters[key])
	w.syncWaitersMu.Unlock()
	if remaining != 0 {
		t.Errorf("syncWaiters[key] not cleared; got %d entries", remaining)
	}
}

// TestProbeSync_ConcurrentWaitersReleasedByFinishProbe stresses the
// merged single-critical-section registration (2026-08-07 fix) under
// -race: N concurrent ProbeSync callers against a busy slot must all
// attach as reuse waiters (never duplicate fresh probes) and must all be
// released promptly by the owner's finishProbe.
func TestProbeSync_ConcurrentWaitersReleasedByFinishProbe(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	fp := newFakeStateProvider()
	w.stateProvider = fp

	const credID = 15
	const model = "gpt-5.6"
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()

	const n = 8
	cands := []credentialstate.NoCandidatesCandidate{
		{CredentialID: credID, RawModel: model},
	}
	returned := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.ProbeSync(context.Background(), cands, "default", "req-concurrent")
			returned <- struct{}{}
		}()
	}

	// All N must attach as reuse waiters (the slot is busy), never
	// launch a fresh probe.
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.syncWaitersMu.Lock()
		nw := len(w.syncWaiters[key])
		w.syncWaitersMu.Unlock()
		if nw >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d waiters, got %d", n, nw)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Owner finishes: mark available, release the slot + notify.
	fp.setAvailable(credID, model, true)
	w.finishProbe(key)

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("not all concurrent ProbeSync callers returned after finishProbe")
	}
	wg.Wait()
}

// TestProbeSync_FinishProbeAtomicityDoesNotCloseNewRound pins the 2026-08-07
// fix that pins the contract: finishProbe must drain the in-flight slot
// AND detach this round's waiter list under the same critical section,
// so a fresh ProbeSync caller that registers a new waiter AFTER the
// slot is released is never woken by the previous round's notify —
// otherwise its channel would close without a corresponding state-cache
// write and the caller would hang forever.
//
// The test exercises the exact race the fix targets:
//  1. Caller A starts cycle / fresh probe for key — slot reserved.
//  2. Caller B (the reuse path) attaches a waiter to key.
//  3. Caller A's finishProbe runs.
//  4. Right after the slot is released, a NEW caller C (the next
//     ProbeSync fresh probe) reserves the slot again and registers its
//     own waiter.
//  5. Caller B's waiter MUST close (it was the previous round's
//     waiter). Caller C's waiter MUST NOT close — the previous
//     notify must have detached the previous round's waiter list
//     atomically with the slot release, so the second round's waiter
//     must remain pending until the new finishProbe runs.
//
// We sequence the calls deterministically rather than rely on timing:
// the test models the contract directly.
func TestProbeSync_FinishProbeAtomicityDoesNotCloseNewRound(t *testing.T) {
	w := &NodeProbeWorker{
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		syncWaiters: make(map[string][]chan struct{}),
	}
	const credID = 16
	const model = "kimi-k2.6"
	key := fmt.Sprintf("%d|%s", credID, model)

	// Round 1: reserve the slot + register B as the reuse waiter.
	w.mu.Lock()
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()
	chB := make(chan struct{})
	w.syncWaitersMu.Lock()
	w.syncWaiters[key] = append(w.syncWaiters[key], chB)
	w.syncWaitersMu.Unlock()

	// finishProbe must drain the slot AND the round-1 waiter list in
	// one critical section. After this call, both inFlight[key] and
	// syncWaiters[key] must be empty.
	w.finishProbe(key)

	// Round 2 starts: a new ProbeSync caller reserves the slot and
	// registers its own waiter BEFORE any later notify fires.
	w.mu.Lock()
	if _, busy := w.inFlight[key]; busy {
		t.Fatal("inFlight[key] should be free after finishProbe")
	}
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()
	chC := make(chan struct{})
	w.syncWaitersMu.Lock()
	w.syncWaiters[key] = append(w.syncWaiters[key], chC)
	w.syncWaitersMu.Unlock()

	// Round-1 waiter MUST close (it was the previous round's waiter).
	select {
	case <-chB:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("round-1 waiter was not closed by finishProbe")
	}

	// Round-2 waiter MUST NOT close — no finishProbe has run for
	// round 2 yet.
	select {
	case <-chC:
		t.Fatal("round-2 waiter was closed by the previous finishProbe — slot-release and waiter-detach are NOT atomic")
	default:
	}

	// Drain syncWaiters for round 2 explicitly to avoid leaking
	// channels into other tests (we are not using t.Parallel).
	w.mu.Lock()
	delete(w.inFlight, key)
	chs := append([]chan struct{}(nil), w.syncWaiters[key]...)
	delete(w.syncWaiters, key)
	w.mu.Unlock()
	for _, ch := range chs {
		close(ch)
	}
}
