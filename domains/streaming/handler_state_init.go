package streaming

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/state"
	"github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

// requestStateInit wires a state.Runtime + RequestContext into the per-request
// pipeline. The runtime runs on its own goroutine (driven by r.Context() and
// the cancel reason), so the request body itself is still a pure synchronous
// pipeline — emit() calls become non-blocking channels into the state machine.
//
// Lifecycle contract (matches spec §3):
//   - Init at handler entry; runtime starts in StateReceived.
//   - Caller emits events at the same points where existing INFO logs live
//     (auth verified → EventAuthed; candidates resolved → EventRouted; ...).
//   - On handler exit, defer runtime.Cancel(reason) so the event loop reaches
//     a terminal state and the runtime goroutine exits even if no caller has
//     emitted the terminating Event explicitly. The cancel hook on
//     r.Context().Done() also feeds into the same channel, so client
//     disconnect and handler-exit share one termination trigger.
//   - The runtime goroutine never blocks the request body; emit drops events
//     on a full buffer (per SP-01's design — drop-on-overflow is part of the
//     contract).
//
// This helper is intentionally tiny: it exists so chat / responses / messages
// handlers share one entry point. The downstream state machine calls
// (EnterAuthed / EnterRouted / etc.) live in the body of each handler.
func (h *ChatHandler) initRequestStateMachine(parent context.Context, requestID, tenantID string) (*state.Runtime, *state.RequestContext) {
	reqCtx := state.NewRequestContext(requestID, tenantID)
	rt := state.NewRuntime(reqCtx)
	streamretry.BindStateCancel(parent, reqCtx.Cancelled())
	// Run the event loop in the background. r.Context() cancellation drives
	// the StateCancelled path automatically (see runtime.eventLoop).
	go func() {
		_ = rt.Run(parent)
	}()
	return rt, reqCtx
}

// cancelRequestStateMachine is the deferred cleanup companion to
// initRequestStateMachine. It is idempotent and safe to call multiple times.
func cancelRequestStateMachine(rt *state.Runtime, reason error) {
	if rt == nil {
		return
	}
	rt.Cancel(reason)
}
