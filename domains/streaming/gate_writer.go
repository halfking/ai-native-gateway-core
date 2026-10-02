package streaming

import (
	"io"
	"net/http"
	"strings"
)

// gate_writer.go — SR-W1 Phase 0B (doc 18 §9.3)
//
// GateWriter adapts the bridges' byte-oriented io.Writer interface to the
// frame-oriented AttemptCommitGate: it assembles incoming bytes into
// complete SSE frames (terminated by a blank line) and feeds each frame
// through the gate. The gate owns the actual write to the client connection
// via its SerializedStreamWriter, so with the gate in GateModeImmediate the
// bytes that reach the wire are identical to the legacy path — Phase 0B only
// adds commit-state tracking.
//
// This lets the survival coordinator (W2) switch a request to
// GateModeBuffered without touching any bridge code: the same writer, the
// same call sites, a different gate mode.

// GateWriter assembles SSE frames and forwards them through an
// AttemptCommitGate. It also implements http.ResponseWriter so it can wrap
// the bridges' client writer directly; header/status calls delegate to the
// original ResponseWriter when one was provided at construction.
type GateWriter struct {
	gate               *AttemptCommitGate
	pending            []byte
	delegate           http.ResponseWriter
	status             int
	header             http.Header
	semanticVisibility func()
}

// NewGateWriter wraps the client connection for one attempt. The gate must
// have been constructed over a SerializedStreamWriter that wraps the real
// ResponseWriter; bridges write to the GateWriter instead of the
// ResponseWriter directly.
func NewGateWriter(gate *AttemptCommitGate) *GateWriter {
	return &GateWriter{gate: gate}
}

// NewGateWriterWithResponse wraps gate plus the original ResponseWriter so
// Header/WriteHeader calls reach the real connection.
func NewGateWriterWithResponse(gate *AttemptCommitGate, orig http.ResponseWriter) *GateWriter {
	return &GateWriter{gate: gate, delegate: orig}
}

// Header delegates to the wrapped ResponseWriter, or returns a throwaway
// header map when the gate stands alone (tests, capture sinks).
func (gw *GateWriter) Header() http.Header {
	if gw.delegate != nil {
		return gw.delegate.Header()
	}
	if gw.header == nil {
		gw.header = make(http.Header)
	}
	return gw.header
}

// WriteHeader delegates to the wrapped ResponseWriter when present.
func (gw *GateWriter) WriteHeader(code int) {
	gw.status = code
	if gw.delegate != nil {
		gw.delegate.WriteHeader(code)
	}
}

// Write buffers p, extracts every complete SSE frame and forwards it to the
// gate in arrival order. Frames split across Write calls are reassembled.
func (gw *GateWriter) Write(p []byte) (int, error) {
	gw.pending = append(gw.pending, p...)
	consumed := 0
	for {
		n := frameBoundary(gw.pending)
		if n < 0 {
			break
		}
		frame := string(gw.pending[:n]) // includes the blank line
		gw.pending = gw.pending[n:]
		consumed += n
		if err := gw.gate.WriteFrame(frame); err != nil {
			// The gate (immediate mode) has already passed earlier bytes
			// through; report the failure so the bridge stops.
			return consumed, err
		}
	}
	return len(p), nil
}

// Flush flushes the underlying connection only. It deliberately does NOT
// pass the pending partial frame through: bridges call Flush after every
// line, and eagerly dumping pending bytes would bypass the gate's
// classification. Pending bytes stay buffered until the frame completes (or
// Finish at attempt end).
func (gw *GateWriter) Flush() {
	_ = gw.FlushError()
}

// FlushError preserves connection errors from the shared serialized writer.
func (gw *GateWriter) FlushError() error {
	return gw.gate.writer.FlushError()
}

// Finish writes any trailing partial frame through the gate's attempt-end
// path (gate.FinishAttempt: verbatim when committed/immediate, held in the
// attempt buffer when the attempt is still discardable, refused when
// discarded) and flushes. Called once at attempt end by the survival
// coordinator, not by bridges. The pending bytes never bypass the gate.
func (gw *GateWriter) Finish() error {
	partial := string(gw.pending)
	gw.pending = nil
	return gw.gate.FinishAttempt(partial)
}

// DrainPending terminates and forwards any residual complete SSE line held in
// the frame-assembly buffer when the upstream stream has ended.
//
// SSE dispatches an event at a blank line OR at end-of-stream, but this
// writer only assembles on the blank line. Upstreams that close the
// connection right after the final `data: [DONE]\n` without the trailing
// blank line (vapeur relay, raw-log proven 2026-09-30) strand the terminal
// frame here forever: the bridge records a clean outcome while the client
// never sees the terminator and hangs. Bridges call this once at attempt end
// so the last well-formed line reaches the gate (and the wire) like any
// other frame. Constraints:
//
//   - only an ALREADY-COMMITTED attempt (or immediate mode) is drained —
//     an uncommitted buffered attempt stays fully discardable and its
//     residue belongs to the survival coordinator's Finish()/FinishAttempt
//     hold semantics, never to a bridge-side commit;
//   - malformed residue (an upstream crash mid-line) and blank/comment-only
//     lines keep the legacy drop behavior — only [DONE] or valid-JSON
//     payloads are forwarded.
func (gw *GateWriter) DrainPending() error {
	if gw == nil || len(gw.pending) == 0 {
		return nil
	}
	gw.gate.mu.Lock()
	drainable := gw.gate.committed || gw.gate.mode == GateModeImmediate
	gw.gate.mu.Unlock()
	if !drainable {
		return nil
	}
	pending := string(gw.pending)
	gw.pending = nil
	if extractPayload(pending) == "" || !validateSSEDataFrame(pending) {
		return nil
	}
	if !strings.HasSuffix(pending, "\n") {
		pending += "\n"
	}
	pending += "\n"
	return gw.gate.WriteFrame(pending)
}

// UnderlyingAttemptGate exposes the gate this writer fronts so downstream
// wrapAttemptWriter calls (bridges) reuse the coordinator-owned gate instead
// of stacking a second one (SR-07 wiring).
func (gw *GateWriter) UnderlyingAttemptGate() *AttemptCommitGate {
	return gw.gate
}

// SetFirstSemanticByteCallback binds dispatch's attempt-scoped first-byte hook
// to a coordinator-created gate without stacking a second classifier.
func (gw *GateWriter) SetFirstSemanticByteCallback(callback func()) {
	if gw != nil && gw.gate != nil {
		gw.gate.SetFirstSemanticByteCallback(callback)
	}
}

// MarkClientSemanticVisible records a client-visible terminal/error frame that
// intentionally does not advance AttemptCommitGate semantic state. Dispatch
// uses this only for post-write failover protection.
func (gw *GateWriter) MarkClientSemanticVisible() {
	if gw != nil && gw.semanticVisibility != nil {
		gw.semanticVisibility()
	}
}

func (gw *GateWriter) SetClientSemanticVisibility(callback func()) {
	if gw != nil {
		gw.semanticVisibility = callback
	}
}

// frameBoundary returns the byte length of the first complete frame in buf
// (content plus its blank-line terminator), or -1 when the buffer does not
// yet contain a complete frame. Both "\n\n" and "\r\n\r\n" terminators
// are recognized: anthropic passthrough forwards upstream bytes verbatim and
// CRLF-dialect vendors must not stall frame assembly.
func frameBoundary(buf []byte) int {
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] != '\n' {
			continue
		}
		if buf[i+1] == '\n' {
			return i + 2
		}
		if buf[i+1] == '\r' && i+2 < len(buf) && buf[i+2] == '\n' {
			return i + 3
		}
	}
	return -1
}

// compile-time interface checks
var _ io.Writer = (*GateWriter)(nil)
