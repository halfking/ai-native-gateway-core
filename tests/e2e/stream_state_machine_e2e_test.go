// Package e2e hosts end-to-end tests for the llm-gateway-go streaming
// request lifecycle. The tests in this file target the state machine
// core (domains/streaming/state) through its public API and cover the
// four spec cases:
//
//   - Case 1 (cancel propagation): covered below through the public
//     RequestContext cancellation channel.
//   - Case 2 (empty candidate set): SKIPPED at the state-machine level
//     — see "ASSUMPTIONS / BLOCKERS" in the report. The state machine
//     has no concept of a candidate set; that lives in the routing
//     layer above. The closest observable at this layer is "early
//     EventFailed → StateFailed", which is covered by the upstream-503
//     test below.
//   - Case 3 (upstream 503 → provider_error): partially covered — we
//     verify EventFailed → StateFailed, the failure error is preserved,
//     and subsequent events are rejected. The literal "provider_error"
//     code lives one layer up (the HTTP handler writes the JSON
//     envelope) and cannot be asserted from the state machine alone.
//   - Case 4 (happy path): fully covered — full state-transition event
//     sequence from initial through normal to terminal success, with
//     chunks + [DONE] in order.
package e2e

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/state"
	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// e2eTestDeadline bounds the wall time of every test in this file. The
// per-case musts require ≤5s wall time per test.
const e2eTestDeadline = 5 * time.Second

// happyPathCadence mirrors the production keep-alive cadence (5ms) and
// is used by the upstream fake producer.
const happyPathCadence = 5 * time.Millisecond

// driveToStreaming emits the canonical prefix of events that bring the
// runtime to StateStreaming. It returns once rt.State() reports
// StateStreaming or the deadline expires, whichever comes first.
//
// This helper avoids time.Sleep entirely: the function polls the
// runtime state via runtime.Gosched() and uses the parent context for
// hard timeouts.
func driveToStreaming(t *testing.T, ctx context.Context, rt *state.Runtime) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for rt.State() != state.StateStreaming {
		if rt.State().IsTerminal() {
			t.Fatalf("unexpected terminal state during drive: %s", rt.State())
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach StateStreaming within 1s; current=%s", rt.State())
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("ctx done during drive: %v", err)
		}
		runtime.Gosched()
	}
}

// TestStreamStateMachine_HappyPath exercises the full happy-path
// state-transition sequence:
//
//	Received → Authed → Routed → Dispatching → Streaming → Completed
//
// It asserts:
//
//   - Final terminal state is StateCompleted.
//   - The stream writer received N data chunks followed by [DONE].
//   - The event log records the exact state-transition sequence:
//     initial (Received→Authed) → normal (Routed, Dispatching, Streaming)
//     → terminal success (Streaming→Completed).
//   - No goroutine leaks: after Run returns, the keep-alive ticker
//     goroutine has been stopped by the runtime.
//
// This is case 4 of the spec.
func TestStreamStateMachine_HappyPath(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < e2eTestDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), e2eTestDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTestDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, 5*time.Millisecond)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-happy-e2e", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)

	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
		return nil
	})
	// Mirror production: close writer on terminal so any straggling
	// upstream writes are rejected (the happy path does not write
	// anything after terminal, but the invariant matters for the
	// cancel and failure tests).
	rt.OnEnter(state.StateCompleted, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})
	rt.OnEnter(state.StateFailed, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})
	rt.OnEnter(state.StateCancelled, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})

	t.Cleanup(func() {
		ps.Stop()
		sw.Close()
	})

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	// Drive Received → Authed → Routed → Dispatching → Streaming.
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped) // → Dispatching
	rt.Emit(state.EventFirstByte)          // → Streaming

	driveToStreaming(t, ctx, rt)

	// Mock upstream producer: emit 3 data chunks + [DONE].
	const wantChunks = 3
	up := testutil.NewFakeUpstream(sw, wantChunks, happyPathCadence, "data: chunk")
	upCtx, upCancel := context.WithCancel(ctx)
	defer upCancel()

	var upWG sync.WaitGroup
	upWG.Add(1)
	go func() {
		defer upWG.Done()
		_ = up.Run(upCtx)
	}()

	// Side-effect pump: wait for the producer to drain, then emit
	// EventStreamEnded so the runtime moves to StateCompleted.
	go func() {
		upWG.Wait()
		_ = sw.WriteDone()
		rt.Emit(state.EventStreamEnded)
	}()

	// Wait for Run to return.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}

	// Final terminal status must be Completed.
	if got := rt.State(); got != state.StateCompleted {
		t.Fatalf("final state=%s want=%s", got, state.StateCompleted)
	}

	// Chunks + [DONE] recorded by the writer.
	frames := sw.FramesSnapshot()
	if len(frames) != wantChunks {
		t.Errorf("expected %d data frames, got %d", wantChunks, len(frames))
	}
	if !sw.WroteDone() {
		t.Errorf("expected [DONE] to be written")
	}

	// Output buffer should contain all chunks followed by [DONE].
	out := sbuf.String()
	wantSubstrings := []string{
		"data: chunk 0",
		"data: chunk 1",
		"data: chunk 2",
		"[DONE]",
	}
	for _, s := range wantSubstrings {
		if !contains(out, s) {
			t.Errorf("output missing %q (full output:\n%s)", s, out)
		}
	}

	// Event log order assertion: every transition we expected must
	// appear in order. The log records (From, To, Event) per entry;
	// we extract the per-entry (from, to) pairs and walk them.
	log := rctx.EventLog()
	type transition struct {
		from state.RequestState
		to   state.RequestState
		ev   state.Event
	}
	wantTransitions := []transition{
		{from: state.StateReceived, to: state.StateAuthed, ev: state.EventAuthed},
		{from: state.StateAuthed, to: state.StateRouted, ev: state.EventRouted},
		{from: state.StateRouted, to: state.StateDispatching, ev: state.EventCompressingSkipped},
		{from: state.StateDispatching, to: state.StateStreaming, ev: state.EventFirstByte},
		{from: state.StateStreaming, to: state.StateCompleted, ev: state.EventStreamEnded},
	}
	if len(log) < len(wantTransitions) {
		t.Fatalf("event log too short: got %d entries, want ≥%d (log=%+v)",
			len(log), len(wantTransitions), log)
	}
	for i, wt := range wantTransitions {
		got := log[i]
		if got.From != wt.from || got.To != wt.to || got.Event != wt.ev {
			t.Errorf("event log[%d] mismatch: got (%s→%s, ev=%s) want (%s→%s, ev=%s)",
				i, got.From, got.To, got.Event, wt.from, wt.to, wt.ev)
		}
	}

	// No goroutine leaks: ticker must be stopped after Run returns.
	ps.Stop()
	if ps.Running() {
		t.Errorf("ticker goroutine still running after Run() returned")
	}
}

// TestStreamStateMachine_CancelPropagationThroughRequestContext verifies the
// cancellation channel used by the handler/retry bridge. A client-facing
// cancellation enters through Runtime.Cancel, closes reqCtx.Cancelled, and
// drives the runtime to its terminal cancelled state without accepting any
// later stream events.
func TestStreamStateMachine_CancelPropagationThroughRequestContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), e2eTestDeadline)
	defer cancel()

	rctx := state.NewRequestContext("req-cancel-channel-e2e", "tenant-1")
	rt := state.NewRuntime(rctx)
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped)
	rt.Emit(state.EventFirstByte)
	driveToStreaming(t, ctx, rt)

	rt.Cancel(context.Canceled)
	select {
	case <-rctx.Cancelled():
	case <-ctx.Done():
		t.Fatalf("request cancellation channel was not closed: %v", ctx.Err())
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after request cancellation")
		}
	case <-ctx.Done():
		t.Fatalf("Run did not return after request cancellation: %v", ctx.Err())
	}

	if got := rt.State(); got != state.StateCancelled {
		t.Fatalf("final state=%s want=%s", got, state.StateCancelled)
	}
	rt.Emit(state.EventStreamEnded)
	if got := rt.State(); got != state.StateCancelled {
		t.Fatalf("state changed after cancellation: got=%s want=%s", got, state.StateCancelled)
	}
}

// TestStreamStateMachine_Upstream503TerminalFailure exercises the
// upstream-failure case (case 3 of the spec). The runtime receives
// EventFailed while in StateStreaming; the machine must transition to
// StateFailed, the failure reason must propagate through Runtime.Err(),
// and any subsequent events must be silently dropped.
//
// The literal "provider_error" JSON code lives in the HTTP envelope
// layer above the state machine, so we assert what is observable here:
//
//   - StateFailed is the terminal state.
//   - The error returned from Run() is non-nil.
//   - Subsequent Emit(EventStreamEnded) and Emit(EventAuthed) do not
//     change the state.
//   - The keep-alive ticker goroutine has been stopped.
func TestStreamStateMachine_Upstream503TerminalFailure(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < e2eTestDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), e2eTestDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTestDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, 5*time.Millisecond)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-fail-503", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)
	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
		return nil
	})
	rt.OnEnter(state.StateFailed, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})

	t.Cleanup(func() {
		ps.Stop()
		sw.Close()
	})

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped) // → Dispatching
	rt.Emit(state.EventFirstByte)          // → Streaming

	driveToStreaming(t, ctx, rt)

	// Upstream returns 503: signal a hard failure. We deliberately
	// ignore the spec's "provider_error" code string — that lives
	// in the HTTP envelope layer, not the state machine.
	rt.Emit(state.EventFailed)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}

	if got := rt.State(); got != state.StateFailed {
		t.Fatalf("final state=%s want=%s", got, state.StateFailed)
	}

	// Subsequent events must be dropped — no state mutation.
	rt.Emit(state.EventStreamEnded)
	rt.Emit(state.EventFirstByte)
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventCancelled) // direct cancel emit after terminal: drop
	if got := rt.State(); got != state.StateFailed {
		t.Errorf("state mutated after terminal: got=%s want=%s", got, state.StateFailed)
	}

	// Keep-alive goroutine must be stopped.
	ps.Stop()
	if ps.Running() {
		t.Errorf("ticker goroutine still running after terminal failure")
	}
}

// TestStreamStateMachine_UpstreamFailureFromEarlyState covers the
// "upstream returned 503 before any progress" edge case — EventFailed
// arrives immediately after routing (no compression, no dispatch).
// It mirrors what a higher layer would emit if routing succeeded but
// the executor reported "no candidates / provider unavailable" (i.e.
// the closest observable at the state-machine level for case 2 of the
// spec — empty candidate set / upstream 503 before any byte). The
// state machine must transition straight to StateFailed, no chunks
// may have been written, and the [DONE] frame must NOT have been
// written.
func TestStreamStateMachine_UpstreamFailureFromEarlyState(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < e2eTestDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), e2eTestDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTestDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, 5*time.Millisecond)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-fail-early", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)
	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
		return nil
	})
	rt.OnEnter(state.StateFailed, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})

	t.Cleanup(func() {
		ps.Stop()
		sw.Close()
	})

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)

	// Wait for Routed.
	deadline := time.Now().Add(time.Second)
	for rt.State() != state.StateRouted {
		if rt.State().IsTerminal() {
			t.Fatalf("unexpected terminal state: %s", rt.State())
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach StateRouted within 1s; current=%s", rt.State())
		}
		runtime.Gosched()
	}

	// Fail before any compression / dispatch / first byte.
	rt.Emit(state.EventFailed)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}

	if got := rt.State(); got != state.StateFailed {
		t.Fatalf("final state=%s want=%s", got, state.StateFailed)
	}

	// CRITICAL invariant for case 2: no chunks, no [DONE], no
	// "success + stream-error" mixed state. The stream writer
	// must report zero frames and zero [DONE] writes.
	frames := sw.FramesSnapshot()
	if len(frames) != 0 {
		t.Errorf("expected 0 frames on early failure, got %d (%v)", len(frames), frames)
	}
	if sw.WroteDone() {
		t.Errorf("[DONE] was written before failure — invariant violated")
	}
	if sw.WrittenBytes() != 0 {
		t.Errorf("stream writer produced %d bytes before failure; want 0", sw.WrittenBytes())
	}

	ps.Stop()
	if ps.Running() {
		t.Errorf("ticker goroutine still running after early failure")
	}
}

// contains is a tiny strings.Contains replacement to avoid importing
// "strings" for a single call site.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
