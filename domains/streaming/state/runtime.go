package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Hook is the signature for enter/exit hooks attached to a state. Hooks
// run under the runtime's write lock; they must be cheap and must not
// call Emit / Cancel back into the runtime.
type Hook func(r *Runtime) error

// Runtime is the goroutine-safe driver around Next() + RequestContext.
// All state transitions go through apply(), which is single-threaded
// by design so the state machine is linearisable from the outside.
type Runtime struct {
	ctx *RequestContext

	mu       sync.Mutex
	current  RequestState
	err      error
	finalErr error

	// Event channels. events is buffered for normal traffic; cancel
	// is signal-only and always takes priority in the event loop.
	events chan Event
	cancel chan struct{}
	done   chan struct{}

	// WaitGroup tracking the event loop goroutine so Run() can wait
	// for it to fully tear down before returning.
	wg sync.WaitGroup

	enterHooks map[RequestState]Hook
	exitHooks  map[RequestState]Hook
}

// NewRuntime returns a Runtime in StateReceived with no hooks attached.
// The event loop is not started until Run is called.
func NewRuntime(ctx *RequestContext) *Runtime {
	return &Runtime{
		ctx:        ctx,
		current:    StateReceived,
		events:     make(chan Event, 64),
		cancel:     make(chan struct{}, 1),
		done:       make(chan struct{}),
		enterHooks: make(map[RequestState]Hook, 9),
		exitHooks:  make(map[RequestState]Hook, 9),
	}
}

// OnEnter registers h to run when state s is entered. Last writer wins.
// Safe to call before Run() or from a hook.
func (r *Runtime) OnEnter(s RequestState, h Hook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enterHooks[s] = h
}

// OnExit registers h to run when state s is exited. Last writer wins.
func (r *Runtime) OnExit(s RequestState, h Hook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exitHooks[s] = h
}

// State returns the current state. Safe to call from any goroutine.
func (r *Runtime) State() RequestState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Err returns the terminal error (nil if StateCompleted).
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalErr
}

// Done returns a channel closed when the state machine reaches a
// terminal state. Useful for callers that want to fan-in without
// blocking on Run().
func (r *Runtime) Done() <-chan struct{} { return r.done }

// Emit offers event to the event loop. If the runtime is already
// terminal the event is silently dropped. If the events buffer is full
// the event is also dropped — the contract is "the *most recent*
// non-terminal event is honoured", not "every event is honoured".
//
// Emit never blocks.
func (r *Runtime) Emit(event Event) {
	r.mu.Lock()
	if r.current.IsTerminal() {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	select {
	case r.events <- event:
	default:
		// Buffer full. Drop. The runtime will still observe any
		// subsequent Cancellation that arrives via the cancel
		// channel, which always wins over buffered events.
	}
}

// Cancel marks the request as cancelled and signals the event loop to
// drive the state machine into StateCancelled. Cancel is idempotent —
// only the first reason is recorded.
func (r *Runtime) Cancel(reason error) {
	if reason == nil {
		reason = errors.New("cancelled")
	}
	r.ctx.performCancel(reason)
	select {
	case r.cancel <- struct{}{}:
	default:
		// Already pending.
	}
}

// Run starts the event loop and blocks until the state machine reaches
// a terminal state or parent is cancelled. Returns the terminal error
// (nil for StateCompleted, the cancel reason for StateCancelled, or the
// underlying error for StateFailed).
func (r *Runtime) Run(parent context.Context) error {
	r.wg.Add(1)
	go r.eventLoop(parent)
	<-r.done
	r.wg.Wait()
	return r.Err()
}

// eventLoop is the single goroutine that consumes events, applies the
// transition matrix, runs hooks, and shuts the runtime down on
// terminal state. It is the only writer of r.current.
func (r *Runtime) eventLoop(parent context.Context) {
	defer r.wg.Done()

	for {
		select {
		case <-parent.Done():
			r.apply(EventCancelled, fmt.Errorf("parent cancelled: %w", parent.Err()))
			r.shutdown()
			return
		case <-r.cancel:
			r.apply(EventCancelled, r.ctx.CancelErr())
			r.shutdown()
			return
		case event := <-r.events:
			r.apply(event, nil)
			if r.State().IsTerminal() {
				r.shutdown()
				return
			}
		}
	}
}

// apply is the only function that mutates r.current. It runs hooks,
// records the event log, and stops the keep-alive ticker on terminal.
// Errors from hooks are captured into finalErr but never override a
// previously recorded terminal error.
func (r *Runtime) apply(event Event, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.current.IsTerminal() {
		return
	}

	tr, ok := Next(r.current, event)
	if !ok {
		// Illegal transition: per spec, fall back to StateFailed.
		tr = Transition{Next: StateFailed}
		if err == nil {
			err = fmt.Errorf("illegal transition: state=%s event=%s",
				r.current, event)
		}
	}

	prev := r.current

	// Exit hook for prev state — may update err.
	if h := r.exitHooks[prev]; h != nil {
		if herr := h(r); herr != nil && err == nil {
			err = herr
		}
	}

	r.current = tr.Next
	if err != nil && r.finalErr == nil {
		r.finalErr = err
	}

	// Record into the per-request event log.
	r.ctx.appendEventLog(EventLogEntry{
		At:    time.Now(),
		From:  prev,
		To:    tr.Next,
		Event: event,
		Note:  errStr(err),
	})

	// Enter hook for the new state.
	if h := r.enterHooks[tr.Next]; h != nil {
		if herr := h(r); herr != nil && r.finalErr == nil {
			r.finalErr = herr
		}
	}

	// Terminal: ensure the keep-alive ticker cannot leak past the
	// stream writer. Stop() must be idempotent.
	if tr.Next.IsTerminal() {
		if ps := r.ctx.preStream; ps != nil {
			ps.Stop()
		}
	}
}

// shutdown closes the done channel exactly once.
func (r *Runtime) shutdown() {
	r.mu.Lock()
	select {
	case <-r.done:
		// Already closed.
	default:
		close(r.done)
	}
	r.mu.Unlock()
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
