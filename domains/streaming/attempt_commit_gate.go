package streaming

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// attempt_commit_gate.go — SR-W1 (doc 18 §5.1 AttemptCommitGate, §9.3, §10.1)
//
// AttemptCommitGate gives one attempt a protocol-aware local buffer sink:
// the bridge writes frames into the gate instead of the real connection, and
// the gate records a monotonically advancing commit state. Before Commit()
// only keepalive/status comments reach the client. The first semantic frame
// (content/tool_call/terminal) triggers the normal semantic commit — the
// buffered frames are flushed in original order through the shared
// SerializedStreamWriter. While the state is none/metadata a recoverable
// failure may Discard() the whole buffer and transparently retry.
//
// Metadata buffering is bounded: when the limit is hit the gate surfaces
// ErrAttemptMetadataBufferExceeded so the caller can block or switch to a
// different attempt. It never flushes the buffer just to free memory.
//
// GateModeImmediate is the Phase 0B deployment shape: every frame is written
// through at the legacy timing so the wire bytes are identical to the old
// path while commit state is already tracked. GateModeBuffered is the
// survival-mode shape where the coordinator owns the commit decision window.

// CommitState tracks how far an attempt has progressed toward
// client-visible, non-discardable output. Ordered; never regresses.
type CommitState int

const (
	// CommitStateNone: nothing meaningful produced yet.
	CommitStateNone CommitState = iota
	// CommitStateMetadata: only attempt metadata (opening envelopes, block
	// starts, role frames) is buffered. Still fully discardable.
	CommitStateMetadata
	// CommitStateContent: semantic model text was committed.
	CommitStateContent
	// CommitStateToolCall: tool-call output was committed (blocks phase-2
	// continuation per doc 18 §10.3).
	CommitStateToolCall
	// CommitStateTerminal: the attempt's stream terminated.
	CommitStateTerminal
)

// String implements fmt.Stringer for logs and metrics labels.
func (s CommitState) String() string {
	switch s {
	case CommitStateNone:
		return "none"
	case CommitStateMetadata:
		return "metadata"
	case CommitStateContent:
		return "content"
	case CommitStateToolCall:
		return "tool_call"
	case CommitStateTerminal:
		return "terminal"
	default:
		return "unknown"
	}
}

// GateMode selects when buffered frames reach the real connection.
type GateMode int

const (
	// GateModeImmediate writes every frame through as it arrives (Phase 0B:
	// legacy timing, byte-identical wire).
	GateModeImmediate GateMode = iota
	// GateModeBuffered holds frames in the attempt-local buffer until the
	// first semantic frame triggers the commit (survival mode).
	GateModeBuffered
)

var (
	// ErrAttemptAlreadyCommitted is returned by Discard after the attempt
	// has committed client-visible output.
	ErrAttemptAlreadyCommitted = errors.New("attempt already committed")
	// ErrAttemptMetadataBufferExceeded means the attempt-local metadata
	// buffer hit its byte limit. Recover by discarding or switching to an
	// attempt that produces no metadata — never by forcing Commit().
	ErrAttemptMetadataBufferExceeded = errors.New("attempt_metadata_buffer_exceeded")
	// ErrAttemptDiscarded is returned by WriteFrame after Discard: the
	// attempt is dead and a new attempt must use a new gate.
	ErrAttemptDiscarded = errors.New("attempt_discarded")
	// ErrAttemptCheckpointFailed is returned when the durable write-ahead
	// checkpoint hook fails. The gate latches this error and refuses all
	// subsequent writes to prevent sending uncheckpointed semantic bytes.
	ErrAttemptCheckpointFailed = errors.New("attempt_checkpoint_failed")
)

// DefaultMaxMetadataBufferBytes bounds the attempt-local metadata buffer.
// Pre-first-byte protocol openings are small; 64 KiB matches the empty-gate
// budget in stream.go.
const DefaultMaxMetadataBufferBytes = 64 * 1024

// DefaultMaxMetadataBufferAge bounds how long attempt metadata may sit
// buffered (doc 18 §5.1 byte/TIME cap).
const DefaultMaxMetadataBufferAge = 30 * time.Second

// GateOptions configures an AttemptCommitGate.
type GateOptions struct {
	Mode GateMode
	// RequestID correlates every commit/discard log line from this gate with
	// the owning request (2026-08-19 observability pass — reconstruct any
	// failure from `request_id` alone). Empty when the caller has no
	// correlation ID (e.g. detached unit tests).
	RequestID              string
	MaxMetadataBufferBytes int
	// BeforeSemanticCommit is the durable write-ahead hook (doc 18 §11.3):
	// it fires exactly when the gate's commit state advances AND bytes of
	// that state are about to reach the network (immediate mode, an already
	// committed attempt, or the buffered semantic commit — including
	// post-commit advances such as content→tool_call, the §10.3 replay
	// blocker). Returning an error fails the frame write and latches the gate
	// against any later client-visible writes — nothing of that state may be
	// sent (禁写网络). Buffering metadata alone never fires it: the first
	// semantic commit's checkpoint covers the metadata rank too.
	BeforeSemanticCommit func(CommitState) error
	// FirstSemanticByte fires once when the first content/tool-call frame is
	// accepted. Transport comments, ping, metadata, and terminal-only frames do
	// not invoke it.
	FirstSemanticByte func()
	// MaxMetadataBufferAge bounds how long attempt metadata may stay
	// buffered; 0 = DefaultMaxMetadataBufferAge. Overflow surfaces the
	// same ErrAttemptMetadataBufferExceeded as the byte cap.
	MaxMetadataBufferAge time.Duration
	// HoldbackWindow (FR-12 L1, 会话优化 v4 T13): when > 0, the FIRST
	// HoldbackMaxChunks semantic frames (or HoldbackWindow after the first
	// semantic frame, whichever first) are held in the attempt-local buffer
	// WITHOUT advancing commit state — Discard stays legal, so an
	// interruption inside the window discards the buffer and replays
	// invisibly (客户端零感知). 0 (default) keeps the existing behavior:
	// the first semantic frame commits immediately.
	HoldbackWindow time.Duration
	// HoldbackMaxChunks is the chunk half of the L1 window; <= 0 →
	// DefaultHoldbackMaxChunks (only meaningful when HoldbackWindow > 0).
	HoldbackMaxChunks int
	// Now is the clock seam for the holdback window (tests). nil → time.Now.
	Now func() time.Time
}

// AttemptCommitGate is the per-attempt protocol-aware buffer sink.
type AttemptCommitGate struct {
	mu                   sync.Mutex
	writeMu              sync.Mutex
	protocol             ClientProtocol
	writer               *SerializedStreamWriter
	mode                 GateMode
	requestID            string
	maxMetadata          int
	maxMetadataAge       time.Duration
	beforeSemanticCommit func(CommitState) error
	firstSemanticByte    func()
	firstSemanticSeen    bool
	nowFn                func() time.Time

	// FR-12 L1 holdback window state (inactive when holdbackWindow == 0).
	holdbackWindow    time.Duration
	holdbackMaxChunks int
	holdbackOpened    bool
	holdbackOpenAt    time.Time
	holdbackHeld      int

	state     CommitState
	committed bool
	discarded bool
	// checkpointBlocked latches a durable write-ahead failure and keeps the
	// attempt from emitting any later client-visible bytes.
	checkpointBlocked bool
	checkpointErr     error
	buffer            []byte
	bufferLen         int
	// firstMetaAt timestamps the first buffered attempt metadata; the
	// buffer is bounded by bytes AND age (doc 18 §5.1). Checked lazily on
	// the next buffered write — no timer goroutine.
	firstMetaAt time.Time
}

// NewAttemptCommitGate creates a gate for one attempt. All client-facing
// writes for this attempt must go through the gate.
func NewAttemptCommitGate(protocol ClientProtocol, writer *SerializedStreamWriter, opts GateOptions) *AttemptCommitGate {
	if opts.MaxMetadataBufferBytes <= 0 {
		opts.MaxMetadataBufferBytes = DefaultMaxMetadataBufferBytes
	}
	if opts.MaxMetadataBufferAge <= 0 {
		opts.MaxMetadataBufferAge = DefaultMaxMetadataBufferAge
	}
	if opts.HoldbackWindow > 0 && opts.HoldbackMaxChunks <= 0 {
		opts.HoldbackMaxChunks = DefaultHoldbackMaxChunks
	}
	if writer == nil {
		panic("attempt commit gate requires a serialized stream writer")
	}
	return &AttemptCommitGate{
		protocol:             protocol,
		writer:               writer,
		mode:                 opts.Mode,
		requestID:            opts.RequestID,
		maxMetadata:          opts.MaxMetadataBufferBytes,
		maxMetadataAge:       opts.MaxMetadataBufferAge,
		beforeSemanticCommit: opts.BeforeSemanticCommit,
		firstSemanticByte:    opts.FirstSemanticByte,
		nowFn:                opts.Now,
		holdbackWindow:       opts.HoldbackWindow,
		holdbackMaxChunks:    opts.HoldbackMaxChunks,
	}
}

// protocolMetricLabel renders the client protocol as a metric label value.
func protocolMetricLabel(p ClientProtocol) string {
	switch p {
	case ProtocolOpenAIChat:
		return "openai_chat"
	case ProtocolOpenAIResponses:
		return "openai_responses"
	case ProtocolAnthropic:
		return "anthropic"
	default:
		return "unknown"
	}
}

// MayWriteTerminal reports whether a bridge may keep its LEGACY error-path
// terminal-frame rendering (error SSE / synthesized [DONE] / final events).
//
//   - no gate attached (gate disabled): true — legacy behavior, verbatim;
//   - GateModeImmediate (Phase 0B): true — every frame passes through at
//     legacy timing, so error frames reach the wire exactly as before;
//   - GateModeBuffered: true only after the attempt committed (the client
//     saw semantic bytes and must receive a well-formed ending). While the
//     gate still holds an uncommitted attempt the bridge must return a
//     structured outcome ONLY — the survival coordinator owns the final
//     protocol rendering (doc 18 §9.3).
func (g *AttemptCommitGate) MayWriteTerminal() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed || g.mode == GateModeImmediate
}

// SetFirstSemanticByteCallback binds an attempt-scoped callback. It is safe to
// call when a coordinator already created the gate before dispatch selected the
// concrete attempt.
func (g *AttemptCommitGate) SetFirstSemanticByteCallback(callback func()) {
	if g == nil || callback == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.firstSemanticSeen {
		g.firstSemanticByte = callback
	}
}

// State returns the current commit state.
func (g *AttemptCommitGate) State() CommitState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// Committed reports whether this attempt has already flushed
// client-visible output.
func (g *AttemptCommitGate) Committed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed
}

// attemptHasClientSemanticOutput reports whether an upstream failure may be
// rendered as a terminal client frame. HTTP headers, keepalive comments, and
// pre-stream metadata do not make an attempt terminal: the gateway must be
// able to discard that attempt and fail over without exposing the upstream
// error. Immediate gates do not set committed because they never buffer, so
// their semantic commit state is the authoritative signal.
func attemptHasClientSemanticOutput(g *AttemptCommitGate, chunkCount int) bool {
	if g != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.state >= CommitStateContent && (g.committed || g.mode == GateModeImmediate)
	}
	return chunkCount > 0
}

// WriteFrame classifies and records one client-facing SSE frame. In
// buffered mode the frame is held in the attempt-local buffer until commit;
// keepalive frames pass through immediately and never advance state.
func (g *AttemptCommitGate) WriteFrame(frame string) error {
	class := ClassifyClientFrame(g.protocol, frame)

	// Serialize each gate's state decision with its corresponding wire write,
	// while leaving mu available to state observers during potentially slow IO.
	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	g.mu.Lock()
	if err := g.checkpointBlockedErrorLocked(); err != nil {
		g.mu.Unlock()
		return err
	}
	if g.discarded {
		g.mu.Unlock()
		return ErrAttemptDiscarded
	}
	if class == FrameClassKeepalive {
		// Transport-level keepalive is attempt-independent: straight to the
		// shared serialized channel, never state-changing. But it must
		// never jump AROUND already-buffered attempt frames — reordering
		// protocol frames (e.g. an anthropic ping ahead of message_start)
		// breaks client parsers — so queue in order while frames are
		// pending.
		if g.mode == GateModeBuffered && !g.committed && g.bufferLen > 0 {
			// Order-preserving queue behind pending attempt frames — bounded
			// by the same byte/age caps as any other buffered frame.
			err := g.appendBufferedLocked(frame)
			g.mu.Unlock()
			return err
		}
		g.mu.Unlock()
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		return g.writer.FlushError()
	}

	// FR-12 L1 holdback (HoldbackWindow > 0): while the window is open,
	// semantic frames buffer WITHOUT advancing commit state — Discard stays
	// legal and an in-window interruption replays invisibly. The first
	// semantic frame after the window closes falls through to the normal
	// commit path, flushing the held frames in order.
	if g.mode == GateModeBuffered && !g.committed && isSemanticClass(class) {
		held, err := g.holdbackTryHoldLocked(frame)
		if held || err != nil {
			g.mu.Unlock()
			return err
		}
	}

	// Write-ahead checkpoint (doc 18 §11.3): before bytes of a newly
	// reached state reach the network, the durable commit_state must be
	// persisted. A failed checkpoint fails the write (禁写网络) and the
	// advanced local state keeps Discard refused — the attempt fail-closes
	// instead of transparently retrying an unknown DB outcome.
	if err := g.checkpointStateAdvanceUnderWriteLock(class); err != nil {
		g.mu.Unlock()
		return err
	}

	if g.mode == GateModeImmediate || g.committed {
		g.mu.Unlock()
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		if err := g.writer.FlushError(); err != nil {
			return err
		}
		g.markFirstSemanticByte(class)
		return nil
	}

	// Buffered mode, not yet committed.
	if isSemanticClass(class) {
		// First semantic frame triggers the normal semantic commit: flush
		// buffered frames in original order, then this frame.
		slog.Info("attempt_commit_gate: committing on first semantic frame",
			"request_id", g.requestID,
			"frame_class", int(class),
			"buffer_len", g.bufferLen,
			"time_since_first_meta_ms", time.Since(g.firstMetaAt).Milliseconds(),
		)
		buffer := g.buffer
		g.buffer = nil
		g.bufferLen = 0
		if len(buffer) == 0 {
			g.committed = true
		}
		g.mu.Unlock()
		if len(buffer) > 0 {
			if _, err := g.writer.Write(buffer); err != nil {
				g.mu.Lock()
				g.buffer = buffer
				g.bufferLen = len(buffer)
				g.mu.Unlock()
				return err
			}
			g.mu.Lock()
			g.committed = true
			g.mu.Unlock()
		}
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		if err := g.writer.FlushError(); err != nil {
			return err
		}
		g.markFirstSemanticByte(class)
		return nil
	}

	err := g.appendBufferedLocked(frame)
	g.mu.Unlock()
	return err
}

func (g *AttemptCommitGate) markFirstSemanticByte(class FrameClass) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markFirstSemanticByteLocked(class)
}

func (g *AttemptCommitGate) blockCheckpointLocked(err error) {
	if err == nil || g.checkpointBlocked {
		return
	}
	g.checkpointBlocked = true
	g.checkpointErr = fmt.Errorf("%w: %w (state=%s)", ErrAttemptCheckpointFailed, err, g.state)
}

func (g *AttemptCommitGate) checkpointBlockedErrorLocked() error {
	if !g.checkpointBlocked {
		return nil
	}
	return g.checkpointErr
}

// checkpointStateAdvanceUnderWriteLock advances state and invokes the
// write-ahead checkpoint hook if needed, releasing g.mu during hook execution
// while maintaining writeMu exclusion. Returns the checkpoint error (latched)
// or nil. Caller must hold writeMu and g.mu on entry; g.mu will be held on
// return (even on error).
func (g *AttemptCommitGate) checkpointStateAdvanceUnderWriteLock(class FrameClass) error {
	advanced := g.advanceStateLocked(class)
	if !advanced || g.beforeSemanticCommit == nil ||
		(g.mode != GateModeImmediate && !g.committed && !isSemanticClass(class)) {
		return nil
	}
	hook := g.beforeSemanticCommit
	state := g.state
	g.mu.Unlock()
	checkpointErr := hook(state)
	g.mu.Lock()
	if checkpointErr != nil {
		g.blockCheckpointLocked(checkpointErr)
		return g.checkpointErr
	}
	return nil
}

// appendBufferedLocked appends one frame to the attempt-local buffer under
// the unified byte/age caps. Every frame class that lands in the buffer
// (attempt metadata, error frames, order-queued keepalives) goes through
// here; overflow surfaces ErrAttemptMetadataBufferExceeded and never forces
// a commit.
func (g *AttemptCommitGate) appendBufferedLocked(frame string) error {
	if g.firstMetaAt.IsZero() {
		g.firstMetaAt = time.Now()
	} else if time.Since(g.firstMetaAt) > g.maxMetadataAge {
		// Age cap exceeded; never force a commit to reclaim memory.
		metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(protocolMetricLabel(g.protocol)).Inc()
		return ErrAttemptMetadataBufferExceeded
	}
	g.buffer = append(g.buffer, frame...)
	g.bufferLen += len(frame)
	// The cap guards the attempt-local buffer itself — whatever frame class
	// lands in it (metadata, error, keepalive), not just the metadata state.
	if g.bufferLen > g.maxMetadata {
		// Surface the overflow; never force a commit to reclaim memory.
		metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(protocolMetricLabel(g.protocol)).Inc()
		return ErrAttemptMetadataBufferExceeded
	}
	return nil
}

// holdbackTryHoldLocked implements the L1 window decision for one semantic
// frame in buffered uncommitted mode (FR-12 R12.2 L1). The first semantic
// frame opens the window; frames hold while
//
//	held < HoldbackMaxChunks  AND  now - openAt < HoldbackWindow
//
// (chunk #20 is still held, #21 flushes; elapsed == window closes —
// deterministic boundaries, UT-SR-03/04). Returns held=true when the frame
// was buffered (no state advance — Discard remains legal). Frames arriving
// after the window closed return held=false so the caller falls through to
// the normal commit path. The held bytes share the unified byte/age caps of
// the attempt-local buffer.
func (g *AttemptCommitGate) holdbackTryHoldLocked(frame string) (held bool, err error) {
	if g.holdbackWindow <= 0 {
		return false, nil
	}
	now := time.Now()
	if g.nowFn != nil {
		now = g.nowFn()
	}
	if !g.holdbackOpened {
		g.holdbackOpened = true
		g.holdbackOpenAt = now
	}
	if g.holdbackHeld >= g.holdbackMaxChunks || now.Sub(g.holdbackOpenAt) >= g.holdbackWindow {
		return false, nil
	}
	if err := g.appendBufferedLocked(frame); err != nil {
		return false, err
	}
	g.holdbackHeld++
	return true, nil
}

// HoldbackHeldChunks reports how many semantic chunks the L1 window holds.
func (g *AttemptCommitGate) HoldbackHeldChunks() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holdbackHeld
}

// HoldbackWindowOpen reports whether the L1 window is configured, opened and
// still within its limits (chunk/time). The recovery loop polls it to decide
// DiscardAndReplay vs committed-prefix handling.
func (g *AttemptCommitGate) HoldbackWindowOpen() bool {
	if g == nil || g.holdbackWindow <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.discarded || g.committed || !g.holdbackOpened {
		return false
	}
	now := time.Now()
	if g.nowFn != nil {
		now = g.nowFn()
	}
	return g.holdbackHeld < g.holdbackMaxChunks && now.Sub(g.holdbackOpenAt) < g.holdbackWindow
}

// FlushHoldback force-closes the L1 window: held semantic frames commit to
// the client now (write-ahead hook fires for the newly reached state). Use
// it when the stream ended while the window was still open — no later frame
// would trigger the lazy close. No-op for discarded/committed gates.
func (g *AttemptCommitGate) FlushHoldback() error {
	if g == nil {
		return nil
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.flushHoldbackLocked()
}

func (g *AttemptCommitGate) flushHoldbackLocked() error {
	if err := g.checkpointBlockedErrorLocked(); err != nil {
		return err
	}
	if g.discarded || g.committed || g.holdbackWindow <= 0 || !g.holdbackOpened || g.bufferLen == 0 {
		return nil
	}
	if err := g.checkpointStateAdvanceUnderWriteLock(FrameClassContent); err != nil {
		return err
	}
	if err := g.commitLocked(); err != nil {
		return err
	}
	return g.writer.FlushError()
}

// advanceStateLocked moves the commit state forward according to the frame
// class, reporting whether the state advanced. Unknown frames fail closed
// as content.
func (g *AttemptCommitGate) advanceStateLocked(class FrameClass) bool {
	var next CommitState
	switch class {
	case FrameClassKeepalive:
		return false
	case FrameClassConnectionMetadata, FrameClassAttemptMetadata:
		next = CommitStateMetadata
	case FrameClassContent, FrameClassUnknown:
		next = CommitStateContent
	case FrameClassToolCall:
		next = CommitStateToolCall
	case FrameClassTerminal:
		next = CommitStateTerminal
	case FrameClassError:
		// Error frames do not advance semantic state; the outcome path owns
		// terminal rendering.
		return false
	default:
		next = CommitStateContent
	}
	if next > g.state {
		g.state = next
		return true
	}
	return false
}

// isSemanticClass reports whether a frame class forces the semantic commit.
func isSemanticClass(class FrameClass) bool {
	switch class {
	case FrameClassContent, FrameClassToolCall, FrameClassTerminal, FrameClassUnknown:
		return true
	default:
		return false
	}
}

func (g *AttemptCommitGate) markFirstSemanticByteLocked(class FrameClass) {
	if g.firstSemanticSeen || g.firstSemanticByte == nil {
		return
	}
	switch class {
	case FrameClassContent, FrameClassToolCall, FrameClassUnknown:
		g.firstSemanticSeen = true
		g.firstSemanticByte()
	}
}

// commitLocked flushes the attempt-local buffer to the shared writer in
// original order and marks the attempt committed.
func (g *AttemptCommitGate) commitLocked() error {
	if len(g.buffer) > 0 {
		if _, err := g.writer.Write(g.buffer); err != nil {
			return err
		}
		g.buffer = nil
		g.bufferLen = 0
	}
	g.committed = true
	return nil
}

// Commit flushes any buffered frames to the real connection and marks the
// attempt committed. Before flushing, it executes the write-ahead checkpoint
// for the current state if it hasn't been checkpointed yet. After commit,
// Discard is refused.
func (g *AttemptCommitGate) Commit() error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkpointBlockedErrorLocked(); err != nil {
		return err
	}
	if g.discarded {
		return ErrAttemptAlreadyCommitted
	}
	// Checkpoint the current state before committing buffered frames to wire.
	// If state is none/metadata and hook is configured, this ensures metadata
	// is checkpointed before network write.
	if g.beforeSemanticCommit != nil && g.state > CommitStateNone {
		hook := g.beforeSemanticCommit
		state := g.state
		g.mu.Unlock()
		checkpointErr := hook(state)
		g.mu.Lock()
		if checkpointErr != nil {
			g.blockCheckpointLocked(checkpointErr)
			return g.checkpointErr
		}
	}
	if err := g.commitLocked(); err != nil {
		return err
	}
	return g.writer.FlushError()
}

// FinishAttempt writes the attempt's trailing partial frame (bytes after the
// last blank-line terminator — GateWriter.Finish passes them here) and flushes.
// Called once at attempt end, never by bridges.
//
// The partial bytes cannot be classified as a complete frame, so they follow
// the attempt's commit state instead: immediate mode or a committed attempt
// writes them through verbatim (line-protocol bytes must never be dropped or
// duplicated); an uncommitted buffered attempt keeps them in the attempt-local
// buffer under the unified caps — flushed on Commit, dropped on Discard; a
// discarded attempt refuses them with ErrAttemptDiscarded so a dead attempt
// can never emit trailing bytes.
func (g *AttemptCommitGate) FinishAttempt(partial string) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkpointBlockedErrorLocked(); err != nil {
		return err
	}
	if g.discarded {
		return ErrAttemptDiscarded
	}
	if partial == "" {
		if g.mode == GateModeImmediate || g.committed {
			return g.writer.FlushError()
		}
		// The stream ended while the L1 holdback window still held semantic
		// frames: no later frame will trigger the lazy close, so flush now —
		// a successful attempt must never swallow held content. Metadata-only
		// buffers keep the legacy finish semantics (retained, dropped with
		// the gate).
		return g.flushHoldbackLocked()
	}
	class := ClassifyClientFrame(g.protocol, partial)
	terminal := isPartialTerminalFrame(g.protocol, partial)
	if terminal {
		if err := g.checkpointStateAdvanceUnderWriteLock(class); err != nil {
			return err
		}
	}
	if g.mode == GateModeImmediate || g.committed {
		if _, err := g.writer.Write([]byte(partial)); err != nil {
			return err
		}
		if err := g.writer.FlushError(); err != nil {
			return err
		}
		if g.mode == GateModeImmediate {
			g.committed = true
		}
		g.markFirstSemanticByteLocked(class)
		return nil
	}
	if !terminal {
		return g.appendBufferedLocked(partial)
	}
	if err := g.commitLocked(); err != nil {
		return err
	}
	if _, err := g.writer.Write([]byte(partial)); err != nil {
		return err
	}
	g.markFirstSemanticByteLocked(class)
	return g.writer.FlushError()
}

func isPartialTerminalFrame(protocol ClientProtocol, partial string) bool {
	trimmed := strings.TrimSpace(partial)
	switch protocol {
	case ProtocolOpenAIChat:
		return trimmed == "data: [DONE]"
	case ProtocolOpenAIResponses:
		return strings.Contains(trimmed, "event: response.completed") || strings.Contains(trimmed, `"type":"response.completed"`)
	case ProtocolAnthropic:
		return strings.Contains(trimmed, "event: message_stop") || strings.Contains(trimmed, `"type":"message_stop"`)
	default:
		return false
	}
}

// Discard drops the attempt-local buffer without ever touching the client
// connection. Only legal while the state is none/metadata (nothing was
// committed); after the semantic commit it returns
// ErrAttemptAlreadyCommitted.
func (g *AttemptCommitGate) Discard() error {
	g.mu.Lock()
	bufferBytes := g.bufferLen
	holdbackHeld := g.holdbackHeld
	gateState := g.state
	if g.committed || g.state >= CommitStateContent {
		g.mu.Unlock()
		return ErrAttemptAlreadyCommitted
	}
	g.buffer = nil
	g.bufferLen = 0
	g.firstMetaAt = time.Time{}
	// The attempt produced nothing client-visible: state resets so a fresh
	// attempt/gate can be reasoned about uniformly.
	g.state = CommitStateNone
	g.discarded = true
	// Clear checkpoint latch for metadata-only failures: after Discard,
	// the gate is dead and later writes should see ErrAttemptDiscarded,
	// not the stale checkpoint error.
	g.checkpointBlocked = false
	g.checkpointErr = nil
	g.mu.Unlock()
	// 2026-08-19 observability: the discard path was previously silent, so
	// post-mortem could not tell how many buffered bytes were thrown away on
	// a transparent retry. The slog is at debug to keep production logs
	// quiet on the common (no-op, zero-byte) case.
	slog.Debug("attempt_buffer_discarded",
		"request_id", g.requestID,
		"protocol", g.protocol.String(),
		"state", gateState.String(),
		"buffer_bytes", bufferBytes,
		"holdback_held", holdbackHeld,
	)
	return nil
}

// Snapshot returns the gate's current buffer size, holdback chunk count and
// commit state without mutating anything. Used by the survival coordinator
// to log a structured decision line BEFORE Discard zeroes the fields so
// the buffer_bytes reported to slog/audit is the bytes that were actually
// discarded (not zero).
func (g *AttemptCommitGate) Snapshot() (bufferBytes, holdbackHeld int, state CommitState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bufferLen, g.holdbackHeld, g.state
}
