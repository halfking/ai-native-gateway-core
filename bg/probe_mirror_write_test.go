package bg

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// probe_mirror_write_test.go — P1-D (audit 2026-10-28 §4): the node_probe_state
// mirror write failure path must be observable (metrics + graded ERROR logs +
// one bounded in-cycle retry), not a silent Warn. The wording ladder is pinned
// directly (测函数不测接线 lesson from ursm_persist_alert.go), and the bounded
// retry behavior is pinned through execMirrorWrite with a counting exec stub.

func TestNodeProbeMirrorWriteEscalationLadder(t *testing.T) {
	// First failures: single-blip wording.
	for streak := int64(1); streak < nodeProbeMirrorWriteSustainedStreak; streak++ {
		lvl, msg := nodeProbeMirrorWriteEscalation(streak, "failure")
		if lvl != slog.LevelError {
			t.Fatalf("streak %d: level = %v, want Error (WARN 不是告警 — see ursm_persist_alert.go)", streak, lvl)
		}
		if want := "mirror failure write failed after immediate retry"; !strings.Contains(msg, want) {
			t.Fatalf("streak %d: msg %q missing %q", streak, msg, want)
		}
	}
	// Sustained outage must read differently so alert rules can grade it.
	lvl, msg := nodeProbeMirrorWriteEscalation(nodeProbeMirrorWriteSustainedStreak, "failure")
	if lvl != slog.LevelError || !strings.Contains(msg, "FAILED CONTINUOUSLY") {
		t.Fatalf("sustained streak: level=%v msg=%q", lvl, msg)
	}
	if msg == mustMsg(t, 1) {
		t.Fatal("sustained wording must differ from single-blip wording")
	}
}

func mustMsg(t *testing.T, streak int64) string {
	t.Helper()
	_, msg := nodeProbeMirrorWriteEscalation(streak, "failure")
	return msg
}

// TestExecMirrorWriteBoundedRetry pins the retry contract: exactly one
// immediate retry, then escalation. Success on retry resets the streak.
func TestExecMirrorWriteBoundedRetry(t *testing.T) {
	nodeProbeMirrorWriteRetryDelay = shrunkMirrorRetryDelay
	defer nodeProbeMirrorWriteStreak.Store(0)
	nodeProbeMirrorWriteStreak.Store(0)
	w := &NodeProbeWorker{}

	calls := 0
	flaky := errors.New("pooled connection blip")
	w.execMirrorWrite(context.Background(), "failure", "1|m", func() error {
		calls++
		if calls == 1 {
			return flaky
		}
		return nil // second attempt succeeds
	})
	if calls != 2 {
		t.Fatalf("exec calls = %d, want 2 (one failure + one bounded retry)", calls)
	}
	if s := nodeProbeMirrorWriteStreak.Load(); s != 0 {
		t.Fatalf("streak after recovery = %d, want 0", s)
	}
}

// TestExecMirrorWriteExhaustsAndCounts pins the escalation path: both
// attempts fail ⇒ the failure counter advances and the streak grows.
func TestExecMirrorWriteExhaustsAndCounts(t *testing.T) {
	nodeProbeMirrorWriteRetryDelay = shrunkMirrorRetryDelay
	defer nodeProbeMirrorWriteStreak.Store(0)
	nodeProbeMirrorWriteStreak.Store(0)
	w := &NodeProbeWorker{}

	stub := errors.New("db down")
	for i := 1; i <= 4; i++ {
		calls := 0
		w.execMirrorWrite(context.Background(), "success", "1|m", func() error {
			calls++
			return stub
		})
		if calls != 2 {
			t.Fatalf("round %d: exec calls = %d, want exactly 2 (bounded)", i, calls)
		}
		if s := nodeProbeMirrorWriteStreak.Load(); s != int64(i) {
			t.Fatalf("round %d: streak = %d, want %d", i, s, i)
		}
	}
	// The 4th round is past the sustained threshold; the wording must be
	// the sustained one by then (verified against the ladder fn directly).
	lvl, msg := nodeProbeMirrorWriteEscalation(4, "success")
	if lvl != slog.LevelError || !contains(msg, "FAILED CONTINUOUSLY") {
		t.Fatalf("round 4 wording not sustained: %v %q", lvl, msg)
	}
}

// TestExecMirrorWriteCtxDoneAbandons pins the context-death branch: no retry
// when the run context is already dead (worker shutdown), still counted.
func TestExecMirrorWriteCtxDoneAbandons(t *testing.T) {
	defer nodeProbeMirrorWriteStreak.Store(0)
	nodeProbeMirrorWriteStreak.Store(0)
	w := &NodeProbeWorker{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	w.execMirrorWrite(ctx, "failure", "1|m", func() error {
		calls++
		return errors.New("db down")
	})
	if calls != 1 {
		t.Fatalf("exec calls = %d, want 1 (no retry on dead ctx)", calls)
	}
	if s := nodeProbeMirrorWriteStreak.Load(); s != 1 {
		t.Fatalf("streak = %d, want 1 (abandoned retry still counts)", s)
	}
}

// TestMain-free suite-speed shim: the in-cycle retry delay is real time, so
// every retry-path test shrinks it up front. Restoring is unnecessary — the
// delay only matters on the failure path of production mirror writes, and
// test binaries do not write production mirrors.
const shrunkMirrorRetryDelay = time.Millisecond
