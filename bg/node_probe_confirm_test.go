package bg

import (
	"context"
	"testing"
	"time"
)

// Wave 3 B2③: ProbeSync caps same-credential concurrent direct probes at
// nodeProbePerCredSyncConcurrency while the global fanout still bounds the
// worker-wide total.

func TestCredSemaphore_PerCredentialCap(t *testing.T) {
	w := NewNodeProbeWorker(nil, nil, nil, "", "", nil)
	sem := w.credSemaphore(42)

	// Fill the per-credential budget.
	sem <- struct{}{}
	sem <- struct{}{}
	if len(sem) != nodeProbePerCredSyncConcurrency {
		t.Fatalf("semaphore length = %d, want %d after two acquires", len(sem), nodeProbePerCredSyncConcurrency)
	}
	select {
	case sem <- struct{}{}:
		t.Fatal("third same-credential acquire must block")
	default:
	}

	// A different credential is unaffected.
	other := w.credSemaphore(43)
	select {
	case other <- struct{}{}:
		<-other
	default:
		t.Fatal("different credential must have its own budget")
	}

	// Release restores capacity and both ids share one channel instance.
	<-sem
	if w.credSemaphore(42) != sem {
		t.Fatal("credSemaphore must return the same channel per credential")
	}
	select {
	case sem <- struct{}{}:
		<-sem
	default:
		t.Fatal("release must restore capacity")
	}
}

// Wave 3 B2②: ProbeConfirm's verdict matrix — degrade only lands when BOTH
// pings fail; any success (or a nil worker) is a transient blip.

func TestProbeConfirm_VerdictMatrix(t *testing.T) {
	newWorker := func(round func(ctx context.Context, credID int, model string) nodeProbeRoundResult) *NodeProbeWorker {
		w := NewNodeProbeWorker(nil, nil, nil, "", "", nil)
		w.probeConfirmRound = round
		return w
	}

	t.Run("both pings fail confirms broken", func(t *testing.T) {
		w := newWorker(func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
			return nodeProbeRoundResult{ok: false}
		})
		if !w.ProbeConfirm(context.Background(), 7, "m") {
			t.Fatal("two failed pings must confirm broken")
		}
	})

	t.Run("first ping success is transient", func(t *testing.T) {
		calls := 0
		w := newWorker(func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
			calls++
			return nodeProbeRoundResult{ok: true}
		})
		if w.ProbeConfirm(context.Background(), 7, "m") {
			t.Fatal("a successful ping must be transient")
		}
		if calls != 1 {
			t.Fatalf("confirm must stop after the first success, ran %d rounds", calls)
		}
	})

	t.Run("second ping success is transient", func(t *testing.T) {
		calls := 0
		w := newWorker(func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
			calls++
			return nodeProbeRoundResult{ok: calls == 2}
		})
		if w.ProbeConfirm(context.Background(), 7, "m") {
			t.Fatal("a second-ping success must be transient")
		}
		if calls != 2 {
			t.Fatalf("expected two rounds, ran %d", calls)
		}
	})

	t.Run("cancelled context fails closed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w := newWorker(func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
			return nodeProbeRoundResult{ok: false}
		})
		if !w.ProbeConfirm(ctx, 7, "m") {
			t.Fatal("cancelled confirm must fail closed (confirmed broken)")
		}
	})
}

func TestProbeConfirm_NilWorkerFailsClosed(t *testing.T) {
	var w *NodeProbeWorker
	if !w.ProbeConfirm(context.Background(), 7, "m") {
		t.Fatal("nil worker must fail closed")
	}
}

// The confirm window must stay inside the design's 2~5s pacing.
func TestProbeConfirm_PacingConstants(t *testing.T) {
	if nodeProbeConfirmFirstPingDelay < 2*time.Second || nodeProbeConfirmFirstPingDelay > 5*time.Second {
		t.Fatalf("first ping delay %v outside the 2~5s design window", nodeProbeConfirmFirstPingDelay)
	}
	if nodeProbeConfirmPingGap < 0 || nodeProbeConfirmFirstPingDelay+nodeProbeConfirmPingGap > 5*time.Second {
		t.Fatalf("second ping at %v lands outside the 2~5s design window", nodeProbeConfirmFirstPingDelay+nodeProbeConfirmPingGap)
	}
}

// R57 §三.2：ProbeConfirm 的直连 ping 纳入共享 per-cred ≤2 闸；闸被
// ProbeSync 占满时有界等待后 fail-open（不降级）且一个 ping 都不跑。
func TestProbeConfirm_SlotStarvationFailsOpen(t *testing.T) {
	w := NewNodeProbeWorker(nil, nil, nil, "", "", nil)
	calls := 0
	w.probeConfirmRound = func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
		calls++
		return nodeProbeRoundResult{ok: false}
	}
	sem := w.credSemaphore(9)
	sem <- struct{}{}
	sem <- struct{}{}
	defer func() { <-sem; <-sem }()

	start := time.Now()
	if w.ProbeConfirm(context.Background(), 9, "m") {
		t.Fatal("slot starvation must fail open (not confirmed broken)")
	}
	if calls != 0 {
		t.Fatalf("no ping may run under slot starvation, ran %d", calls)
	}
	if elapsed := time.Since(start); elapsed < nodeProbeConfirmSlotWait {
		t.Fatalf("confirm must hold the slot window before failing open, elapsed %v", elapsed)
	}
}

// R57 §三.2：ProbeConfirm 释放闸后，同凭据的 ProbeSync 直连可以立即拿到
// 槽——两条路径共享同一 credSemaphore 实例。
func TestProbeConfirm_ReleasesSharedCredSlot(t *testing.T) {
	w := NewNodeProbeWorker(nil, nil, nil, "", "", nil)
	w.probeConfirmRound = func(ctx context.Context, credID int, model string) nodeProbeRoundResult {
		return nodeProbeRoundResult{ok: true}
	}
	if w.ProbeConfirm(context.Background(), 11, "m") {
		t.Fatal("successful ping must be transient")
	}
	sem := w.credSemaphore(11)
	select {
	case sem <- struct{}{}:
		<-sem
	default:
		t.Fatal("confirm must release the shared per-cred slot")
	}
}
