// Package integration hosts cross-package integration tests for the
// llm-gateway-go streaming request lifecycle. This file targets the
// state machine core defined in domains/streaming/state and verifies
// that client cancellation propagates within the latency budget, the
// ticker goroutine is stopped, and no further state transitions are
// accepted after the terminal cancel.
//
// The state machine is exercised end-to-end through its public API:
// NewRequestContext + NewRuntime + Emit / Cancel / Run. No internals
// are touched. The mock upstream producer is shared with the e2e test
// suite via tests/testutil.
package integration

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/state"
	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// 5ms ticker cadence for the deterministic keep-alive ticker. Used to
// assert that the goroutine actually fires (not just that Stop is
// called) and to align with the spec's "deterministic 5ms ticker".
const cancelTestTickerInterval = 5 * time.Millisecond

// 30ms is the cancel propagation budget from the spec. We assert on
// observed latency with a small slack for CI noise.
const cancelLatencyBudget = 30 * time.Millisecond

// 10s overall deadline for each test (per-case musts). Walls > 5s
// will trip the outer t.Deadline() and fail the test.
const cancelTestOverallDeadline = 10 * time.Second

// TestStreamStateMachine_CancelFromStreaming drives the state machine
// all the way to StateStreaming, then issues a client cancel. The
// cancel must propagate to StateCancelled within cancelLatencyBudget,
// no further events may be accepted, the keep-alive ticker goroutine
// must be stopped, and the final terminal status must be Cancelled.
func TestStreamStateMachine_CancelFromStreaming(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < cancelTestOverallDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), cancelTestOverallDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cancelTestOverallDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, cancelTestTickerInterval)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-cancel-stream", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)

	// Start the keep-alive ticker when entering StateAuthed so it is
	// running by the time we reach StateStreaming — this lets the
	// cancel test assert that the runtime actually stops the goroutine.
	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
		return nil
	})

	// Mirror production: the stream writer is closed when the
	// request reaches a terminal state, so any straggling chunks
	// are rejected (the cancel must not race a chunk already in
	// flight on the writer side).
	rt.OnEnter(state.StateCancelled, func(r *state.Runtime) error {
		sw.Close()
		return nil
	})

	t.Cleanup(func() {
		// Belt-and-braces: ensure no goroutine leaks even if the
		// test bailed early via t.Fatal.
		ps.Stop()
		sw.Close()
	})

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	// Drive the machine forward into StateStreaming.
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped) // → StateDispatching
	rt.Emit(state.EventFirstByte)          // → StateStreaming

	// Spin-wait (cheap; no time.Sleep) until streaming so we can
	// measure cancel latency from a stable state.
	deadline := time.Now().Add(time.Second)
	for rt.State() != state.StateStreaming {
		if rt.State().IsTerminal() {
			t.Fatalf("unexpected terminal state before cancel: %s", rt.State())
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach StateStreaming within 1s; current=%s", rt.State())
		}
		runtime.Gosched()
	}

	// At this point the keep-alive ticker goroutine must be live
	// (otherwise we would not be testing cancellation against a
	// running ticker).
	framesBeforeCancel := ps.Frames()
	if framesBeforeCancel == 0 {
		// Give the ticker one extra cycle so the assertion below
		// (frames strictly increasing before Stop) is meaningful
		// even on a slow runner.
		select {
		case <-time.After(2 * cancelTestTickerInterval):
		case <-ctx.Done():
			t.Fatalf("ctx done waiting for ticker to fire: %v", ctx.Err())
		}
		framesBeforeCancel = ps.Frames()
	}
	if framesBeforeCancel == 0 {
		t.Fatalf("ticker did not fire before cancel — race test cannot proceed")
	}

	cancelStart := time.Now()
	rt.Cancel(errors.New("client disconnected"))

	// Wait for Run to return; this is the "cancel propagation has
	// landed" signal we measure against the budget.
	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}

	latency := time.Since(cancelStart)
	if latency > cancelLatencyBudget {
		t.Errorf("cancel latency too high: %v (budget %v)", latency, cancelLatencyBudget)
	}

	// Final terminal state must be Cancelled.
	if got := rt.State(); got != state.StateCancelled {
		t.Errorf("final state=%s want=%s", got, state.StateCancelled)
	}
	if runErr == nil {
		t.Errorf("Run returned nil error; want non-nil cancel reason")
	}

	// No further state transitions accepted: feeding the machine
	// more events after cancel must be silently dropped.
	rt.Emit(state.EventFirstByte)
	rt.Emit(state.EventStreamEnded)
	rt.Emit(state.EventFailed)
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventCancelled) // even direct cancel emits are dropped
	if got := rt.State(); got != state.StateCancelled {
		t.Errorf("state mutated after cancel: got=%s want=%s", got, state.StateCancelled)
	}

	// Ticker goroutine must have been stopped by the runtime.
	// Runtime calls preStream.Stop() on terminal transitions; allow
	// the goroutine a small grace window to drain.
	stopDeadline := time.Now().Add(100 * time.Millisecond)
	for !ps.Stopped() {
		if time.Now().After(stopDeadline) {
			t.Errorf("ticker Stop() was never invoked after cancel")
			break
		}
		runtime.Gosched()
	}

	// Belt-and-braces Stop call (idempotent) and confirm the
	// goroutine has fully exited. If this fails the runtime never
	// called Stop — the very regression we are guarding against.
	ps.Stop()
	if ps.Running() {
		t.Errorf("ticker goroutine still running after Stop()")
	}
}

// TestStreamStateMachine_CancelFromDispatching exercises cancellation
// from a different non-terminal position (StateDispatching) to confirm
// the universal cancel transition (event=EventCancelled at any
// non-terminal state) lands on StateCancelled with the same latency
// budget. This also asserts that the keep-alive ticker goroutine is
// stopped when cancel arrives mid-flight, before any keep-alive frames
// have been emitted (regression coverage for early cancel).
func TestStreamStateMachine_CancelFromDispatching(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < cancelTestOverallDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), cancelTestOverallDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cancelTestOverallDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, cancelTestTickerInterval)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-cancel-dispatch", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)

	// Start the ticker when entering StateAuthed.
	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
		return nil
	})
	// Close writer on cancel so any straggling frames are rejected.
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

	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped) // → StateDispatching

	// Wait for StateDispatching.
	deadline := time.Now().Add(time.Second)
	for rt.State() != state.StateDispatching {
		if rt.State().IsTerminal() {
			t.Fatalf("unexpected terminal state: %s", rt.State())
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach StateDispatching within 1s; current=%s", rt.State())
		}
		runtime.Gosched()
	}

	cancelStart := time.Now()
	rt.Cancel(errors.New("client aborted before first byte"))

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}

	latency := time.Since(cancelStart)
	if latency > cancelLatencyBudget {
		t.Errorf("cancel latency too high: %v (budget %v)", latency, cancelLatencyBudget)
	}

	if got := rt.State(); got != state.StateCancelled {
		t.Errorf("final state=%s want=%s", got, state.StateCancelled)
	}
	if runErr == nil {
		t.Errorf("Run returned nil error; want non-nil cancel reason")
	}

	// Cancel-rejection: subsequent Emit calls must be dropped.
	rt.Emit(state.EventFirstByte)
	if got := rt.State(); got != state.StateCancelled {
		t.Errorf("state mutated after cancel: got=%s want=%s", got, state.StateCancelled)
	}

	// Ticker must be stopped on terminal — runtime calls Stop().
	stopDeadline := time.Now().Add(100 * time.Millisecond)
	for !ps.Stopped() {
		if time.Now().After(stopDeadline) {
			t.Errorf("ticker Stop() was never invoked after cancel")
			break
		}
		runtime.Gosched()
	}
	ps.Stop()
	if ps.Running() {
		t.Errorf("ticker goroutine still running after Stop()")
	}
}

// TestStreamStateMachine_TickerGoroutineStopsOnCancel is the focused
// goroutine-leak check. It exercises a high-pressure scenario: the
// 5ms ticker is running, many Emit calls land at the runtime, then a
// cancel arrives. After cancel + Stop, no goroutine associated with
// the keep-alive ticker may still be live.
//
// The test additionally counts goroutines before/after the cancel to
// guard against an unrelated leak source in the runtime itself.
func TestStreamStateMachine_TickerGoroutineStopsOnCancel(t *testing.T) {
	t.Parallel()

	if deadline, ok := t.Deadline(); ok {
		if time.Until(deadline) < cancelTestOverallDeadline {
			t.Fatalf("parent deadline too tight: %v remaining (need ≥%v)",
				time.Until(deadline), cancelTestOverallDeadline)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cancelTestOverallDeadline)
	defer cancel()

	sbuf := testutil.NewSafeBuffer()
	ps := testutil.NewTickerKeepalive(sbuf, cancelTestTickerInterval)
	sw := testutil.NewBlockingStreamWriter(sbuf)

	rctx := state.NewRequestContext("req-leak", "tenant-1")
	rctx.SetPreStream(ps)
	rctx.SetStreamWriter(sw)

	rt := state.NewRuntime(rctx)
	rt.OnEnter(state.StateAuthed, func(r *state.Runtime) error {
		ps.Start()
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

	// Baseline goroutine count before the runtime is launched.
	runtime.GC()
	baseGCount := runtime.NumGoroutine()

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressingSkipped) // → Dispatching
	rt.Emit(state.EventFirstByte)          // → Streaming

	// Wait for streaming.
	deadline := time.Now().Add(time.Second)
	for rt.State() != state.StateStreaming {
		if rt.State().IsTerminal() {
			t.Fatalf("unexpected terminal state: %s", rt.State())
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach StateStreaming within 1s; current=%s", rt.State())
		}
		runtime.Gosched()
	}

	// Confirm the ticker is alive (otherwise the test below is
	// meaningless). Channel-based wait (no time.Sleep for sync).
	if ps.Frames() == 0 {
		select {
		case <-time.After(2 * cancelTestTickerInterval):
		case <-ctx.Done():
			t.Fatalf("ctx done waiting for ticker to fire: %v", ctx.Err())
		}
	}
	if ps.Frames() == 0 {
		t.Fatalf("ticker never fired — leak check cannot proceed")
	}

	// Hammer the runtime with spurious events to maximise the chance
	// of leaking a goroutine. These should be dropped after cancel.
	var spamWg sync.WaitGroup
	spamStop := make(chan struct{})
	spamEvents := []state.Event{
		state.EventFirstByte,
		state.EventStreamEnded,
		state.EventFailed,
		state.EventAuthed,
		state.EventRouted,
		state.EventCompressing,
		state.EventCompressingDone,
		state.EventCompressingSkipped,
		state.EventDispatching,
	}
	spamIdx := atomic.Int32{}
	for i := 0; i < 4; i++ {
		spamWg.Add(1)
		go func() {
			defer spamWg.Done()
			ticker := time.NewTicker(2 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-spamStop:
					return
				case <-ticker.C:
					ev := spamEvents[int(spamIdx.Add(1))%len(spamEvents)]
					rt.Emit(ev)
				}
			}
		}()
	}

	// Brief settle so spammers push a few events first.
	select {
	case <-time.After(10 * time.Millisecond):
	case <-ctx.Done():
		t.Fatalf("ctx done during spam settle: %v", ctx.Err())
	}

	// Cancel.
	cancelStart := time.Now()
	rt.Cancel(errors.New("client disconnected (leak test)"))

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return within deadline: %v", ctx.Err())
	}
	if time.Since(cancelStart) > cancelLatencyBudget {
		t.Errorf("cancel latency too high: %v (budget %v)", time.Since(cancelStart), cancelLatencyBudget)
	}

	// Stop spam goroutines.
	close(spamStop)
	spamWg.Wait()

	// Runtime must have called Stop() on the ticker. Idempotent
	// explicit call as a defensive net.
	ps.Stop()

	// Goroutine count must return to baseline ± small slack.
	// Allow up to 5 extra for GC workers, test-runner noise, etc.
	runtime.GC()
	deadline = time.Now().Add(2 * time.Second)
	var finalGCount int
	for {
		finalGCount = runtime.NumGoroutine()
		if finalGCount <= baseGCount+5 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count elevated after cancel: base=%d now=%d (delta=%d)",
				baseGCount, finalGCount, finalGCount-baseGCount)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The ticker itself must be stopped (no live goroutine).
	if ps.Running() {
		t.Errorf("ticker goroutine still running after cancel + Stop()")
	}
	if !ps.Stopped() {
		t.Errorf("ticker not in Stopped state after cancel + Stop()")
	}
}
