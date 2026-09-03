package streaming

import (
	"context"
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
	// ErrL2AlignmentMiss is returned by an L2 replay gate (FR-12 R12.2 L2,
	// GateOptions.ReplayAlignment) when the fresh replay stream failed to
	// reproduce the committed prefix below the suppression threshold. The
	// attempt is void — nothing client-visible was emitted — and the
	// coordinator degrades to the existing resume_blocked envelope path.
	ErrL2AlignmentMiss = errors.New("l2_alignment_miss")
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
	// P1-2 fix (2026-08-28): Added context.Context parameter to enable timeout
	// control, cancellation, and trace propagation in checkpoint operations.
	BeforeSemanticCommit func(context.Context, CommitState) error
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

	// PrefixObserve (FR-12 L2 wiring, design resume-blocked-long-stream-
	// recovery §3.3 point 1): when non-nil, every semantic frame that
	// successfully reaches the wire is folded into the committed-prefix
	// cache (CommittedPrefixCache.Observe semantics: requestID + normalized
	// semantic bytes — SSE data payloads only, never transport
	// keepalives/comments). nil (L2 disabled — the default) keeps the gate
	// byte-identical to the legacy behavior: the observer is consulted only
	// AFTER a successful write+flush, so discarded attempts never feed the
	// cache.
	PrefixObserve func(requestID string, semantic []byte)
	// ReplayAlignment (FR-12 L2 wiring, §3.3 point 3): when non-nil this gate
	// is an aligned-continuation REPLAY gate. The replay stream's first
	// CommittedPrefix.TotalBytes normalized semantic bytes buffer while the
	// PrefixAligner compares them against the committed prefix; until
	// alignment proves no duplication NOTHING but transport keepalives
	// reaches the client. Never set on the default (L2 disabled) path.
	ReplayAlignment *ReplayAlignmentOptions
}

// ReplayAlignmentOptions arms an AttemptCommitGate as an L2 aligned-
// continuation replay gate (design §3.3 point 3).
type ReplayAlignmentOptions struct {
	// Committed is the request's committed-prefix snapshot (a deep copy —
	// the gate never aliases the cache).
	Committed CommittedPrefix
	// Aligner grades the replay stream against Committed; nil → the default
	// 9000bp aligner.
	Aligner *PrefixAligner
}

// replayBufferedFrame is one raw replay frame with the normalized-semantic-
// byte range it contributed ([normStart, normEnd); zero-width for metadata /
// keepalive-class frames, which carry no alignment evidence).
type replayBufferedFrame struct {
	raw       string
	normStart int
	normEnd   int
}

// replayAlignmentState is the per-replay-gate L2 machine (all fields guarded
// by the gate's mu; wire writes additionally serialized by writeMu).
type replayAlignmentState struct {
	aligner   *PrefixAligner
	committed CommittedPrefix
	needBytes int // normalized bytes needed before the verdict (== TotalBytes)
	maxRaw    int // raw buffering bound (frame overhead over needBytes)
	frames    []replayBufferedFrame
	norm      []byte
	rawLen    int
	decided   bool
	aligned   bool
	missed    bool
	result    PrefixAlignment
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
	beforeSemanticCommit func(context.Context, CommitState) error
	firstSemanticByte    func()
	firstSemanticSeen    bool
	nowFn                func() time.Time
	ctx                  context.Context // P1-2: context for checkpoint operations

	// FR-12 L1 holdback window state (inactive when holdbackWindow == 0).
	holdbackWindow    time.Duration
	holdbackMaxChunks int
	holdbackOpened    bool
	holdbackOpenAt    time.Time
	holdbackHeld      int

	// FR-12 L2 wiring: prefixObserve folds wire-proven semantic frames into
	// the committed-prefix cache (nil — L2 disabled — is a pure no-op);
	// align carries the replay-alignment machine (nil for normal attempts).
	prefixObserve func(requestID string, semantic []byte)
	align         *replayAlignmentState

	state     CommitState
	committed bool
	discarded bool
	// checkpointedState records the highest state whose durable checkpoint
	// completed successfully. It prevents duplicate non-idempotent hooks.
	checkpointedState CommitState
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
// P1-2 fix (2026-08-28): Added ctx parameter to propagate context to checkpoint operations.
func NewAttemptCommitGate(ctx context.Context, protocol ClientProtocol, writer *SerializedStreamWriter, opts GateOptions) *AttemptCommitGate {
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
	if ctx == nil {
		ctx = context.Background()
	}
	g := &AttemptCommitGate{
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
		prefixObserve:        opts.PrefixObserve,
		ctx:                  ctx,
	}
	if opts.ReplayAlignment != nil {
		aligner := opts.ReplayAlignment.Aligner
		if aligner == nil {
			aligner = NewPrefixAligner(0)
		}
		need := opts.ReplayAlignment.Committed.TotalBytes
		g.align = &replayAlignmentState{
			aligner:   aligner,
			committed: opts.ReplayAlignment.Committed,
			needBytes: need,
			// Raw frames carry SSE framing overhead over the folded data
			// payloads; bound the buffer generously so the verdict can always
			// form before memory becomes the constraint.
			maxRaw: need*2 + DefaultMaxMetadataBufferBytes,
		}
	}
	return g
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

	// FR-12 L2 aligned-continuation replay (ReplayAlignment armed): frames
	// buffer suppressed while PrefixAligner decides whether the replay
	// reproduces the committed prefix; only the keepalives handled above may
	// reach the client meanwhile. Never armed on the default (L2 disabled)
	// path, so the check costs one nil branch there. It precedes the L1
	// holdback on purpose: a replay gate must never hold frames in the
	// attempt-local holdback buffer (its suppression window is the aligner).
	if g.align != nil {
		err := g.writeFrameReplayAlignedLocked(frame, class)
		g.mu.Unlock()
		return err
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
		// L2 wiring point 1: the frame is wire-proven — fold its semantic
		// payload into the committed-prefix cache (no-op when L2 is off).
		g.observeFrame(class, frame)
		return nil
	}

	// Buffered mode, not yet committed.
	if isSemanticClass(class) {
		// First semantic frame triggers the normal semantic commit: flush
		// buffered frames in original order, then this frame.
		// 2026-08-27 P0 fix: Mark committed BEFORE releasing the lock to
		// prevent Discard() from succeeding after bytes reach the network.
		slog.Info("attempt_commit_gate: committing on first semantic frame",
			"request_id", g.requestID,
			"frame_class", int(class),
			"buffer_len", g.bufferLen,
			"time_since_first_meta_ms", time.Since(g.firstMetaAt).Milliseconds(),
		)
		buffer := g.buffer
		g.buffer = nil
		g.bufferLen = 0
		g.committed = true // Mark committed before network I/O
		g.mu.Unlock()
		if len(buffer) > 0 {
			if _, err := g.writer.Write(buffer); err != nil {
				// Write failed but already marked committed; cannot rollback
				// (bytes may have partially reached the client). Return error
				// to stop further writes.
				return err
			}
		}
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		if err := g.writer.FlushError(); err != nil {
			return err
		}
		g.markFirstSemanticByte(class)
		// L2 wiring point 1: fold exactly the bytes that reached the wire —
		// the flushed attempt buffer first (wire order; it may hold L1
		// holdback chunks), then the triggering semantic frame.
		g.observeWrittenBytes(buffer)
		g.observeFrame(class, frame)
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
	if err == nil {
		return
	}
	// P1-5 fix (2026-08-28): Log subsequent checkpoint errors for diagnostics
	// even though the gate latches only the first error.
	if g.checkpointBlocked {
		slog.Warn("attempt_commit_gate: subsequent checkpoint error ignored (gate already blocked)",
			"new_error", err,
			"latched_error", g.checkpointErr,
			"state", g.state)
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
// P1-2 fix (2026-08-28): Pass stored context to checkpoint hook for timeout control.
func (g *AttemptCommitGate) checkpointStateAdvanceUnderWriteLock(class FrameClass) error {
	advanced := g.advanceStateLocked(class)
	if !advanced || g.beforeSemanticCommit == nil ||
		(g.mode != GateModeImmediate && !g.committed && !isSemanticClass(class)) {
		return nil
	}
	hook := g.beforeSemanticCommit
	state := g.state
	ctx := g.ctx
	g.mu.Unlock()
	checkpointErr := hook(ctx, state)
	g.mu.Lock()
	if checkpointErr != nil {
		g.blockCheckpointLocked(checkpointErr)
		return g.checkpointErr
	}
	if state > g.checkpointedState {
		g.checkpointedState = state
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
	// 2026-08-28 audit fix: check capacity BEFORE appending to prevent
	// bufferLen from transiently exceeding maxMetadata. The old code
	// appended first, then checked, allowing overflow until the next call.
	if g.bufferLen+len(frame) > g.maxMetadata {
		metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(protocolMetricLabel(g.protocol)).Inc()
		return ErrAttemptMetadataBufferExceeded
	}
	g.buffer = append(g.buffer, frame...)
	g.bufferLen += len(frame)
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
	oldState := g.state
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
		// 2026-09-01: the state machine was previously only observable at its
		// terminal verdict. The none→metadata→content transition is what decides
		// whether an interrupted attempt can still be discarded, so log every
		// advance to make the resume_blocked escalation reconstructible.
		slog.Debug("gate_state_advanced",
			"request_id", g.requestID,
			"from", oldState.String(),
			"to", next.String(),
			"frame_class", int(class),
			"buffer_bytes", g.bufferLen,
			"committed", g.committed,
		)
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
		// 2026-09-01 P0 observability: log the commit decision with holdback
		// context so operators can confirm whether the L1 window was active
		// (holdback_window_ms > 0) and how many chunks it held before commit.
		// This is critical for diagnosing committed_output / resume_blocked
		// failures on unstable models like glm-5.2 / minimax-m3.
		streamLogFromContext(g.ctx, nil).Info("attempt_commit_gate_committing",
			"buffer_bytes", len(g.buffer),
			"holdback_window_ms", g.holdbackWindow.Milliseconds(),
			"holdback_max_chunks", g.holdbackMaxChunks,
			"holdback_held_chunks", g.holdbackHeld,
			"holdback_opened", g.holdbackOpened,
			"commit_state", g.state.String(),
		)
		// 2026-08-27 P0 fix: Clear buffer BEFORE writing to prevent duplicate
		// writes if the caller retries after a write error. Once committed=true,
		// the gate refuses further operations, so a partial write cannot be
		// completed by retrying.
		bufCopy := g.buffer
		g.buffer = nil
		g.bufferLen = 0
		g.committed = true // Mark committed to prevent retries
		if _, err := g.writer.Write(bufCopy); err != nil {
			return err
		}
		// L2 wiring point 1 (single choke point for every buffered flush —
		// semantic commit, holdback release, terminal partial commit): the
		// bytes are wire-proven, fold their semantic payloads in wire order.
		// No-op when L2 is disabled.
		g.observeWrittenBytesLocked(bufCopy)
	} else {
		g.committed = true
	}
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
	// P1-2 fix (2026-08-28): Pass stored context to checkpoint hook.
	if g.beforeSemanticCommit != nil && g.state > g.checkpointedState {
		hook := g.beforeSemanticCommit
		state := g.state
		ctx := g.ctx
		g.mu.Unlock()
		checkpointErr := hook(ctx, state)
		g.mu.Lock()
		// The checkpoint hook runs without g.mu, so Discard may win while it is
		// blocked. Discard is terminal for this attempt and must win before any
		// hook result can lead to flushing the old buffer.
		if g.discarded {
			return ErrAttemptDiscarded
		}
		if checkpointErr != nil {
			g.blockCheckpointLocked(checkpointErr)
			return g.checkpointErr
		}
		g.checkpointedState = state
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
	// L2 replay gate: the trailing partial folds into the alignment buffers
	// like any frame, and an undecided alignment is settled NOW (the stream
	// will produce no further evidence).
	if g.align != nil {
		return g.finishReplayAttemptLocked(partial)
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
		// L2 wiring point 1: wire-proven trailing bytes fold too (under-
		// observation is the dangerous direction — an unobserved tail could
		// be re-forwarded by a later aligned replay).
		g.observeFrameLocked(class, partial)
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
	g.observeFrameLocked(class, partial)
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
//
// Lock scope (audit-24h-20260828-r4 P3 doc): Discard acquires g.mu only,
// NOT writeMu. This is intentional and SAFE under the gate's contract:
//
//   - writeMu serializes state-changing WRITES with the corresponding
//     wire write (gates share a single client connection; each wire write
//     is a serialized event on the shared channel).
//   - Discard performs no wire write — it only resets in-memory state
//     (buffer, state machine, discarded flag). The shared client
//     connection is untouched, so writeMu exclusion is unnecessary.
//   - The g.discarded flag set here is read under g.mu inside WriteFrame
//     (line 325), which holds writeMu first, so a concurrent WriteFrame
//     will either: (a) complete before Discard acquires g.mu, in which
//     case the pre-Discard write has already been flushed and Discard
//     is a no-op for that frame; or (b) see g.discarded == true after
//     Discard releases g.mu and return ErrAttemptDiscarded without any
//     wire write. There is no interleaving that emits frames after a
//     Discard.
//
// The mu-only acquisition is documented here so future contributors do
// not 'fix' the asymmetry by adding writeMu — that would deadlock any
// goroutine currently in the write side (e.g. checkpointStateAdvance
// under WriteLock at line 461) and is not required for correctness.
func (g *AttemptCommitGate) Discard() error {
	// mu-only acquisition: see lock-scope comment above. writeMu is omitted
	// because (a) Discard performs no wire write, only resets in-memory state,
	// and (b) Commit/WriteFrame/FinishAttempt/FlushHoldback all hold writeMu
	// around their hook or wire-IO windows — adding writeMu here would deadlock
	// any concurrent caller that is mid-hook (regression caught by
	// TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook).
	g.mu.Lock()
	defer g.mu.Unlock()
	bufferBytes := g.bufferLen
	holdbackHeld := g.holdbackHeld
	gateState := g.state
	if g.committed || g.state >= CommitStateContent {
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

// ── FR-12 L2 wiring: committed-prefix observation + aligned replay ─────────
//
// Design: docs/design/resume-blocked-long-stream-recovery.md §3.3 points 1/3.
// Red line (doc 18 §10.3): 绝不透明重试已提交内容 — a replay gate forwards a
// single byte only after PrefixAligner proved the fresh stream reproduces the
// client-visible committed prefix, and only from a whole-frame boundary at or
// after the suffix offset (never mid-frame, never a straddling frame).

// l2SemanticObservation extracts the normalized semantic bytes of one
// client-bound frame (design §3.3 point 1: "SSE data 载荷或等价规范化"):
// the data-payload lines of semantic-class frames (content / tool-call /
// terminal / unknown). Transport keepalives, SSE comments and attempt
// metadata (per-attempt openings — message_start, role frames, block starts —
// which are regenerated and never byte-stable across replays) fold nothing.
// Pure and deterministic so the replay gate's alignment folding reproduces
// the identical normalization byte for byte.
func l2SemanticObservation(class FrameClass, frame string) []byte {
	if !isSemanticClass(class) {
		return nil
	}
	var obs []byte
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		obs = append(obs, strings.TrimSpace(strings.TrimPrefix(line, "data:"))...)
		obs = append(obs, '\n')
	}
	return obs
}

// observeFrame folds one wire-proven frame into the committed-prefix cache.
// Nil observer (L2 disabled — the default) is a no-op: the disabled hot path
// pays exactly one branch check per frame.
func (g *AttemptCommitGate) observeFrame(class FrameClass, frame string) {
	if g.prefixObserve == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observeFrameLocked(class, frame)
}

// observeFrameLocked is observeFrame with g.mu held.
func (g *AttemptCommitGate) observeFrameLocked(class FrameClass, frame string) {
	if g.prefixObserve == nil {
		return
	}
	if obs := l2SemanticObservation(class, frame); len(obs) > 0 {
		g.prefixObserve(g.requestID, obs)
	}
}

// observeWrittenBytes re-splits a flushed byte blob (the attempt buffer:
// metadata, L1-holdback chunks, order-queued keepalives, trailing partials)
// and folds every semantic frame in wire order. The trailing unterminated
// remainder folds whole: every semantic byte that reached the wire must be
// observed, otherwise a later aligned replay could re-forward the unobserved
// tail (duplication — the dangerous direction).
func (g *AttemptCommitGate) observeWrittenBytes(buf []byte) {
	if g.prefixObserve == nil || len(buf) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observeWrittenBytesLocked(buf)
}

// observeWrittenBytesLocked is observeWrittenBytes with g.mu held.
func (g *AttemptCommitGate) observeWrittenBytesLocked(buf []byte) {
	if g.prefixObserve == nil {
		return
	}
	for len(buf) > 0 {
		n := frameBoundary(buf)
		var frame []byte
		if n < 0 {
			frame = buf
			buf = nil
		} else {
			frame = buf[:n]
			buf = buf[n:]
		}
		cls := ClassifyClientFrame(g.protocol, string(frame))
		g.observeFrameLocked(cls, string(frame))
	}
}

// errL2ReplayBufferOverflow guards the replay raw buffer (hash-only mode must
// fold the full committed byte count before the verdict).
var errL2ReplayBufferOverflow = errors.New("l2_replay_buffer_overflow")

// writeFrameReplayAlignedLocked handles one frame for an L2 replay gate.
// Caller holds writeMu and g.mu; returns with g.mu held.
func (g *AttemptCommitGate) writeFrameReplayAlignedLocked(frame string, class FrameClass) error {
	st := g.align
	if st.decided {
		if !st.aligned {
			// Miss already latched: the attempt is void; refuse further
			// frames so the bridge stops feeding this gate.
			return ErrL2AlignmentMiss
		}
		return g.forwardReplayPassthroughLocked(frame, class)
	}
	if err := st.bufferLocked(class, frame); err != nil {
		st.decided = true
		st.missed = true
		logL2Alignment(g.ctx, g.requestID, st.committed, PrefixAlignment{}, "replay_buffer_overflow")
		recordAlignmentMiss()
		return ErrL2AlignmentMiss
	}
	if len(st.norm) >= st.needBytes {
		return g.decideReplayAlignmentLocked()
	}
	// Suppressed silently: the bridge keeps streaming; only keepalives
	// (handled in WriteFrame before this branch) keep the client connection
	// warm during the alignment window.
	return nil
}

// bufferLocked appends one frame (raw bytes + its normalized-semantic
// contribution) to the replay buffers.
func (st *replayAlignmentState) bufferLocked(class FrameClass, frame string) error {
	if st.rawLen+len(frame) > st.maxRaw {
		return errL2ReplayBufferOverflow
	}
	obs := l2SemanticObservation(class, frame)
	st.frames = append(st.frames, replayBufferedFrame{
		raw:       frame,
		normStart: len(st.norm),
		normEnd:   len(st.norm) + len(obs),
	})
	st.norm = append(st.norm, obs...)
	st.rawLen += len(frame)
	return nil
}

// decideReplayAlignmentLocked grades the buffered replay stream against the
// committed prefix. Aligned → the write-ahead checkpoint fires for the
// content rank (the decision is the commit moment) and the suffix is
// forwarded from a frame boundary. Miss → the attempt is void.
func (g *AttemptCommitGate) decideReplayAlignmentLocked() error {
	st := g.align
	res := st.aligner.Align(st.committed, st.norm)
	st.decided = true
	st.result = res
	logL2Alignment(g.ctx, g.requestID, st.committed, res, "")
	if !res.Aligned {
		st.missed = true
		recordAlignmentMiss()
		return ErrL2AlignmentMiss
	}
	st.aligned = true
	// Write-ahead checkpoint (doc 18 §11.3): the first suffix byte is a
	// semantic commit — fire the hook for the content rank BEFORE any
	// suppressed-prefix-lifted byte may reach the network (mirrors "the
	// first semantic commit's checkpoint covers the metadata rank").
	if err := g.checkpointStateAdvanceUnderWriteLock(FrameClassContent); err != nil {
		return err
	}
	return g.forwardReplaySuffixLocked(res)
}

// forwardReplaySuffixLocked releases the suppression: every buffered frame
// whose normalized contribution lies ENTIRELY at/after the suffix offset is
// forwarded; frames fully before it stay suppressed (client already has
// them); a frame STRADDLING the offset is suppressed whole — forwarding it
// would re-send client-visible bytes (绝不变换出重复内容), dropping it loses
// at most one frame of fresh bytes. Zero-contribution frames (replay opening
// metadata) stay suppressed: the client already received the original
// attempt's openings.
func (g *AttemptCommitGate) forwardReplaySuffixLocked(res PrefixAlignment) error {
	st := g.align
	first := len(st.frames)
	for i := range st.frames {
		if f := &st.frames[i]; f.normEnd > f.normStart && f.normStart >= res.SuffixOffset {
			first = i
			break
		}
	}
	suffix := st.frames[first:]
	st.frames = nil
	st.norm = nil
	st.rawLen = 0
	if len(suffix) == 0 {
		return nil
	}
	// Mark committed before the first byte reaches the wire so Discard can
	// never win after the suppression lifts (2026-08-27 P0 fix idiom).
	g.committed = true
	for i := range suffix {
		cls := ClassifyClientFrame(g.protocol, suffix[i].raw)
		if err := g.checkpointStateAdvanceUnderWriteLock(cls); err != nil {
			return err
		}
		if _, err := g.writer.Write([]byte(suffix[i].raw)); err != nil {
			return err
		}
		g.observeFrameLocked(cls, suffix[i].raw)
		g.markFirstSemanticByteLocked(cls)
	}
	return g.writer.FlushError()
}

// forwardReplayPassthroughLocked writes one post-decision frame straight
// through (alignment already proved no duplication for everything before it).
func (g *AttemptCommitGate) forwardReplayPassthroughLocked(frame string, class FrameClass) error {
	if err := g.checkpointStateAdvanceUnderWriteLock(class); err != nil {
		return err
	}
	g.committed = true
	if _, err := g.writer.Write([]byte(frame)); err != nil {
		return err
	}
	if err := g.writer.FlushError(); err != nil {
		return err
	}
	g.observeFrameLocked(class, frame)
	g.markFirstSemanticByteLocked(class)
	return nil
}

// finishReplayAttemptLocked is FinishAttempt's L2 replay branch. A trailing
// partial frame folds into the alignment buffers like any frame; a still
// undecided alignment (stream ended before the window filled) is decided NOW
// — the attempt can never end with the client waiting behind an unresolved
// verdict.
func (g *AttemptCommitGate) finishReplayAttemptLocked(partial string) error {
	st := g.align
	if st.missed {
		return ErrL2AlignmentMiss
	}
	if !st.decided {
		if partial != "" {
			class := ClassifyClientFrame(g.protocol, partial)
			if err := st.bufferLocked(class, partial); err != nil {
				st.decided = true
				st.missed = true
				logL2Alignment(g.ctx, g.requestID, st.committed, PrefixAlignment{}, "replay_buffer_overflow")
				recordAlignmentMiss()
				return ErrL2AlignmentMiss
			}
		}
		if len(st.norm) == 0 {
			// The replay produced no observable semantic bytes at all
			// (instant upstream death): void the attempt — nothing was or
			// will be client-visible from it.
			st.decided = true
			st.missed = true
			logL2Alignment(g.ctx, g.requestID, st.committed, PrefixAlignment{}, "replay_no_semantic_bytes")
			recordAlignmentMiss()
			return ErrL2AlignmentMiss
		}
		if err := g.decideReplayAlignmentLocked(); err != nil {
			return err
		}
		return g.writer.FlushError()
	}
	if !st.aligned {
		return ErrL2AlignmentMiss
	}
	if partial == "" {
		return g.writer.FlushError()
	}
	class := ClassifyClientFrame(g.protocol, partial)
	return g.forwardReplayPassthroughLocked(partial, class)
}

// AlignmentOutcome reports the L2 replay alignment verdict (design §3.3
// point 4 observability; the survival coordinator folds it into the task
// decision). decided=false means the stream ended before a verdict formed —
// callers treat that as a miss. Nil-safe for non-replay gates.
func (g *AttemptCommitGate) AlignmentOutcome() (decided bool, result PrefixAlignment) {
	if g == nil || g.align == nil {
		return false, PrefixAlignment{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.align.decided, g.align.result
}

// logL2Alignment emits the L2 decision event (design §3.3 point 4 / §五
// metrics): survival_l2_alignment{score_bp, common_bytes, hash_only,
// aligned}. reason carries the degenerate verdicts that have no score
// (buffer overflow, empty replay).
func logL2Alignment(ctx context.Context, requestID string, committed CommittedPrefix, res PrefixAlignment, reason string) {
	attrs := []any{
		"request_id", requestID,
		"score_bp", int(res.ScoreBP),
		"common_bytes", res.CommonBytes,
		"hash_only", res.HashOnly,
		"aligned", res.Aligned,
		"committed_bytes", committed.TotalBytes,
		"suffix_offset", res.SuffixOffset,
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if res.Aligned {
		streamLogFromContext(ctx, nil).Info("survival_l2_alignment", attrs...)
		return
	}
	streamLogFromContext(ctx, nil).Warn("survival_l2_alignment", attrs...)
}
