// Package state implements the explicit state machine that drives the
// lifecycle of a streaming request handled by the LLM gateway.
//
// The package is intentionally split into three layers:
//   - request_state.go    (this file): pure data + pure transition function
//   - request_context.go:              per-request container + collaborators
//   - runtime.go:                      goroutine-safe driver with hooks
//
// Keeping Next() a pure function lets the transition matrix be exhaustively
// tested with table-driven unit tests, while the runtime owns all I/O,
// locking, and hook execution.
package state

import "fmt"

// RequestState enumerates every legal position a streaming request may
// occupy. Completed, Failed and Cancelled are terminal — once reached the
// state machine rejects any further event (see Next for details).
type RequestState int

const (
	// StateReceived is the entry point — the request has been parsed
	// and the auth/key material has not yet been validated.
	StateReceived RequestState = iota
	// StateAuthed is reached once the key has been accepted.
	StateAuthed
	// StateRouted is reached once routing has selected the final
	// candidate(s) that will service the request.
	StateRouted
	// StateCompressing is reached while body compression is in flight.
	StateCompressing
	// StateDispatching is reached when the executor has accepted the
	// request and is sending it upstream (no first byte yet).
	StateDispatching
	// StateStreaming is reached once the upstream provider returned
	// the first byte; subsequent bytes stream through until
	// EventStreamEnded or a terminal failure.
	StateStreaming
	// StateCompleted is the success terminal state.
	StateCompleted
	// StateFailed is the failure terminal state — reached when any
	// non-terminal state observes EventFailed.
	StateFailed
	// StateCancelled is the cancellation terminal state — reached
	// when any non-terminal state observes EventCancelled. It
	// pre-empts StateFailed when both events arrive concurrently.
	StateCancelled
)

// String renders a human readable label for logs and assertions.
func (s RequestState) String() string {
	switch s {
	case StateReceived:
		return "Received"
	case StateAuthed:
		return "Authed"
	case StateRouted:
		return "Routed"
	case StateCompressing:
		return "Compressing"
	case StateDispatching:
		return "Dispatching"
	case StateStreaming:
		return "Streaming"
	case StateCompleted:
		return "Completed"
	case StateFailed:
		return "Failed"
	case StateCancelled:
		return "Cancelled"
	default:
		return fmt.Sprintf("RequestState(%d)", int(s))
	}
}

// IsTerminal reports whether s admits no further transitions.
func (s RequestState) IsTerminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// Event enumerates every external signal that may advance the state
// machine. Events are emitted by upstream hooks (auth, routing,
// compression, executor, client cancellation, etc.).
type Event int

const (
	// EventAuthed signals successful key validation.
	EventAuthed Event = iota
	// EventRouted signals routing has selected a final candidate set.
	EventRouted
	// EventCompressing signals that body compression has started.
	EventCompressing
	// EventCompressingDone signals that body compression finished
	// successfully and the executor may proceed.
	EventCompressingDone
	// EventCompressingSkipped signals that compression was not needed
	// (e.g. body below threshold) and execution can start directly.
	EventCompressingSkipped
	// EventDispatching signals that the executor has accepted the
	// request and is sending it upstream. It is the dispatcher's
	// own "I am now live" signal and is interchangeable with
	// EventCompressingSkipped from the matrix point of view.
	EventDispatching
	// EventFirstByte signals that the upstream provider sent the
	// first byte of the response, opening the streaming window.
	EventFirstByte
	// EventStreamEnded signals a clean end of stream from upstream.
	EventStreamEnded
	// EventFailed signals a hard failure at any non-terminal state.
	EventFailed
	// EventCancelled signals client cancellation. It is honoured at
	// any non-terminal state and overrides EventFailed for ordering
	// purposes (see Runtime priority rules).
	EventCancelled
)

// String renders a human readable label for logs and assertions.
func (e Event) String() string {
	switch e {
	case EventAuthed:
		return "Authed"
	case EventRouted:
		return "Routed"
	case EventCompressing:
		return "Compressing"
	case EventCompressingDone:
		return "CompressingDone"
	case EventCompressingSkipped:
		return "CompressingSkipped"
	case EventDispatching:
		return "Dispatching"
	case EventFirstByte:
		return "FirstByte"
	case EventStreamEnded:
		return "StreamEnded"
	case EventFailed:
		return "Failed"
	case EventCancelled:
		return "Cancelled"
	default:
		return fmt.Sprintf("Event(%d)", int(e))
	}
}

// Transition is the deterministic outcome of feeding an event into the
// state machine. Emits is reserved for future hook fan-out; the current
// transition table emits nothing — exit/enter hooks are wired via the
// Runtime instead.
type Transition struct {
	Next  RequestState
	Emits []string
}

// transitionTable encodes the legal (state, event) -> Next mapping for
// every non-terminal source state. Universal transitions (EventFailed,
// EventCancelled) are handled directly inside Next and are not listed
// here so the table stays readable.
var transitionTable = func() map[RequestState]map[Event]RequestState {
	t := make(map[RequestState]map[Event]RequestState, 9)

	t[StateReceived] = map[Event]RequestState{
		EventAuthed: StateAuthed,
	}
	t[StateAuthed] = map[Event]RequestState{
		EventRouted:             StateRouted,
		EventCompressing:        StateCompressing,
		EventCompressingSkipped: StateDispatching,
		EventDispatching:        StateDispatching,
	}
	t[StateRouted] = map[Event]RequestState{
		EventCompressing:        StateCompressing,
		EventCompressingSkipped: StateDispatching,
		EventDispatching:        StateDispatching,
	}
	t[StateCompressing] = map[Event]RequestState{
		EventCompressingDone: StateDispatching,
	}
	t[StateDispatching] = map[Event]RequestState{
		EventFirstByte: StateStreaming,
	}
	t[StateStreaming] = map[Event]RequestState{
		EventStreamEnded: StateCompleted,
	}
	return t
}()

// Next is a pure function that resolves the next RequestState given the
// current state and the event being applied. ok=false signals an illegal
// transition; the runtime will fall back to StateFailed (per spec §2:
// "非法转移 ok=false,runtime 进入 StateFailed").
//
// EventFailed and EventCancelled are universal non-terminal transitions
// so they are wired directly here for consistency — callers do not need
// to special-case them.
func Next(current RequestState, event Event) (Transition, bool) {
	if current.IsTerminal() {
		// Terminal states reject every event, including EventFailed
		// and EventCancelled. The runtime treats a terminal hit as
		// a no-op rather than re-entering the same terminal state.
		return Transition{}, false
	}
	switch event {
	case EventFailed:
		return Transition{Next: StateFailed}, true
	case EventCancelled:
		return Transition{Next: StateCancelled}, true
	}
	row, ok := transitionTable[current]
	if !ok {
		return Transition{}, false
	}
	next, ok := row[event]
	if !ok {
		return Transition{}, false
	}
	return Transition{Next: next}, true
}
