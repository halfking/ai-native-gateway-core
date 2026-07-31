package streaming

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestRequestLogContextTerminalGate_SingleWinner asserts that among
// concurrent callers claiming terminal, exactly one SetTerminal call
// returns won=true and that subsequent SetTerminal calls all return
// false. Verifies the high-level outcome contract for the
// success/failure/disconnect triple, matching spec §10 Step 2.
func TestRequestLogContextTerminalGate_SingleWinner(t *testing.T) {
	ctx := &RequestLogContext{}
	kinds := []string{"success", "failure", "disconnect"}
	var winners [3]atomic.Int64
	var winnerKinds [3]atomic.Value
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func(i int, kind string) {
			defer wg.Done()
			won := ctx.SetTerminal(kind, nil)
			if won {
				winners[i].Add(1)
				winnerKinds[i].Store(kind)
			}
		}(i, kind)
	}
	wg.Wait()
	totalWins := winners[0].Load() + winners[1].Load() + winners[2].Load()
	if totalWins != 1 {
		t.Fatalf("expected exactly one winner, got %d", totalWins)
	}
	if !ctx.IsTerminal() {
		t.Fatalf("IsTerminal() = false after a winner claimed the gate")
	}
	var winnerKind string
	for i := range winners {
		if winners[i].Load() == 1 {
			winnerKind, _ = winnerKinds[i].Load().(string)
			break
		}
	}
	if winnerKind == "" {
		t.Fatalf("could not determine winning kind")
	}
	if got := ctx.TerminalKind(); got != winnerKind {
		t.Fatalf("TerminalKind() = %q, want %q", got, winnerKind)
	}
}

// TestRequestLogContextTerminalGate_SequentialCalls asserts that once
// a winner has claimed the terminal, every subsequent SetTerminal call
// (regardless of caller goroutine) returns false. This protects against
// late success/failure/disconnect events racing the safety net.
func TestRequestLogContextTerminalGate_SequentialCalls(t *testing.T) {
	ctx := &RequestLogContext{}
	entry := &telemetry.RequestLogEntry{}
	if !ctx.SetTerminal("success", entry) {
		t.Fatalf("first SetTerminal must win")
	}
	for _, kind := range []string{"failure", "disconnect", "success"} {
		if ctx.SetTerminal(kind, nil) {
			t.Fatalf("subsequent SetTerminal(%q) should not win", kind)
		}
	}
	if got := ctx.TerminalKind(); got != "success" {
		t.Fatalf("TerminalKind() = %q, want %q (first winner)", got, "success")
	}
	if got := ctx.TerminalEntry(); got != entry {
		t.Fatalf("TerminalEntry() should return the payload captured by the winner")
	}
}

// TestRequestLogContextTerminalGate_PayloadCaptured asserts the entry
// passed by the winning caller is the one returned by TerminalEntry,
// and that late losers cannot overwrite it.
func TestRequestLogContextTerminalGate_PayloadCaptured(t *testing.T) {
	ctx := &RequestLogContext{}
	winEntry := &telemetry.RequestLogEntry{}
	loserEntry := &telemetry.RequestLogEntry{}
	if !ctx.SetTerminal("failure", winEntry) {
		t.Fatalf("first SetTerminal must win")
	}
	if ctx.SetTerminal("disconnect", loserEntry) {
		t.Fatalf("loser must not win")
	}
	if got := ctx.TerminalEntry(); got != winEntry {
		t.Fatalf("TerminalEntry() = %v, want the winner's entry", got)
	}
}

// TestRequestLogContextTerminalGate_NilSafe guards against nil-pointer
// panics for SetTerminal, IsTerminal and TerminalKind so the safety
// net can call them defensively before the log context is wired up.
func TestRequestLogContextTerminalGate_NilSafe(t *testing.T) {
	var ctx *RequestLogContext
	if ctx.SetTerminal("success", nil) {
		t.Fatalf("nil receiver SetTerminal must return false")
	}
	if ctx.IsTerminal() {
		t.Fatalf("nil receiver IsTerminal must return false")
	}
	if got := ctx.TerminalKind(); got != "" {
		t.Fatalf("nil receiver TerminalKind must return \"\", got %q", got)
	}
	if got := ctx.TerminalEntry(); got != nil {
		t.Fatalf("nil receiver TerminalEntry must return nil, got %v", got)
	}
}

// TestRequestLogContextTerminalGate_EmptyKindRejects ensures callers
// cannot accidentally claim the terminal by passing an empty kind.
// CAS must not flip in that case and TerminalKind must stay empty.
func TestRequestLogContextTerminalGate_EmptyKindRejects(t *testing.T) {
	ctx := &RequestLogContext{}
	if ctx.SetTerminal("", nil) {
		t.Fatalf("empty kind must not claim the terminal")
	}
	if ctx.IsTerminal() {
		t.Fatalf("IsTerminal() = true after an empty-kind call")
	}
	if got := ctx.TerminalKind(); got != "" {
		t.Fatalf("TerminalKind() = %q, want \"\"", got)
	}
}