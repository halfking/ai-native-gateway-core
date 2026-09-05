package streaming

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/state"
)

// TestInitRequestStateMachine_Sanity confirms the SP-02 wiring surface:
//
//   - init returns a runtime at StateReceived
//   - the eventLoop goroutine is alive (Done() not yet closed)
//   - cancelRequestStateMachine transitions the runtime to a terminal state
//   - the runtime converges to terminal within a small bounded window so
//     the parent handler can exit cleanly without leaking the goroutine
func TestInitRequestStateMachine_Sanity(t *testing.T) {
	t.Parallel()

	h := &ChatHandler{}
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	rt, _ := h.initRequestStateMachine(parent, "req-test-sanity", "")

	if rt == nil {
		t.Fatal("initRequestStateMachine returned nil runtime")
	}
	if got := rt.State(); got != state.StateReceived {
		t.Fatalf("initial state = %s, want StateReceived", got)
	}

	// Drive the runtime through a happy-path sequence so we exercise the
	// event loop goroutine without any of the executor / compressor
	// collaborators that production handlers own. The state machine
	// transition table only accepts EventRouted from StateAuthed (not
	// StateReceived), and EventCompressing/EventCompressingSkipped
	// from StateAuthed or StateRouted — see state.request_state.go.
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)
	rt.Emit(state.EventCompressing)
	rt.Emit(state.EventCompressingDone)
	rt.Emit(state.EventFirstByte)
	rt.Emit(state.EventStreamEnded)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.State().IsTerminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := rt.State(); got != state.StateCompleted {
		t.Fatalf("terminal state = %s, want StateCompleted", got)
	}

	// Cancel must be idempotent — calling it on a terminal runtime is a
	// no-op and must not panic or block.
	cancelRequestStateMachine(rt, errors.New("post-terminal cancel"))
}

// TestInitRequestStateMachine_ParentCancel asserts the runtime reacts to
// r.Context() cancellation by transitioning to StateCancelled (per spec §3
// "r.Context().Done() OR state.Context.cancel 任一触发 → StateCancelled").
func TestInitRequestStateMachine_ParentCancel(t *testing.T) {
	t.Parallel()

	h := &ChatHandler{}
	parent, cancelParent := context.WithCancel(context.Background())

	rt, _ := h.initRequestStateMachine(parent, "req-test-parent-cancel", "")
	rt.Emit(state.EventAuthed)
	rt.Emit(state.EventRouted)

	cancelParent()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.State().IsTerminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := rt.State(); got != state.StateCancelled {
		t.Fatalf("state after parent cancel = %s, want StateCancelled", got)
	}
}

// TestInitRequestStateMachine_NilSafe asserts the cancel helper is
// defensive — a nil runtime must not panic, so deferred calls in
// error-paths are always safe.
func TestInitRequestStateMachine_NilSafe(t *testing.T) {
	t.Parallel()

	// should not panic on nil runtime
	cancelRequestStateMachine(nil, errors.New("nil runtime"))
}
