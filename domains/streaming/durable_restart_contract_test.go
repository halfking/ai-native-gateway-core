package streaming

import (
	"context"
	"testing"
	"time"
)

// TestT0DurableRestartLeaseAndFencingContract is the runtime counterpart of
// the shared restart fixture: a live lease is not taken over, an expired lease
// is reclaimed once with a higher fencing token, and the stale owner cannot
// commit a second terminal result.
func TestT0DurableRestartLeaseAndFencingContract(t *testing.T) {
	h := newScenarioHarness(t)
	stale := h.createTask(t, "gateway-a")
	bindingA := newDurableStreamBinding(h.store, stale, time.Minute)

	h.worker.runOnce(context.Background())
	if h.exec.calls != 0 {
		t.Fatalf("live lease was taken over: upstream calls=%d", h.exec.calls)
	}
	if h.store.snapshotTask().FencingToken != 1 {
		t.Fatalf("live lease changed fencing token: %+v", h.store.snapshotTask())
	}

	h.store.expireLease()
	h.worker.runOnce(context.Background())
	if h.exec.calls != 1 {
		t.Fatalf("expired lease recovery calls=%d, want 1", h.exec.calls)
	}
	if got := h.store.snapshotTask().FencingToken; got != 2 {
		t.Fatalf("fencing token=%d, want 2 after takeover", got)
	}
	if h.store.terminalCount() != 1 {
		t.Fatalf("terminal commits=%d, want 1", h.store.terminalCount())
	}

	if err := bindingA.Complete(context.Background(), []byte("stale"), "text/event-stream"); err == nil {
		t.Fatal("stale owner terminal commit was accepted")
	}
	if h.store.terminalCount() != 1 || h.exec.calls != 1 {
		t.Fatalf("stale owner caused duplicate result: terminals=%d calls=%d", h.store.terminalCount(), h.exec.calls)
	}
}
