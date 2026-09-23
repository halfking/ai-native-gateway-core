package errorsx

import "errors"

// ErrProtocolTerminalRendered marks a failed request whose protocol terminal
// (SSE error envelope / [DONE] / message_stop) the gateway already put on
// the wire for this connection — rendered by a stream bridge (§11.6
// eof_without_done envelope, stream-timeout envelope, anthropic
// passthrough/bridge interruption frames) or by the survival coordinator's
// Terminal seam (whose package-level sentinel wraps this one).
//
// Post-loop error handlers consult it via errors.Is (including through
// ExecuteError.LastErr, which has no Unwrap) so they keep the failure
// bookkeeping but never stack a SECOND terminal on the wire after the first
// one — one terminal per request.
//
// Defined here (not in domains/streaming) because the executor dispatch
// pipeline also needs to wrap it and cannot import domains/streaming
// (import cycle: streaming → executors).
var ErrProtocolTerminalRendered = errors.New("gateway protocol terminal already rendered on the wire")
