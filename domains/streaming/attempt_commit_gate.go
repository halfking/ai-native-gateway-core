package streaming

import (
	"errors"
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
	Mode                   GateMode
	MaxMetadataBufferBytes int
	// MaxMetadataBufferAge bounds how long attempt metadata may stay
	// buffered; 0 = DefaultMaxMetadataBufferAge. Overflow surfaces the
	// same ErrAttemptMetadataBufferExceeded as the byte cap.
	MaxMetadataBufferAge time.Duration
}

// AttemptCommitGate is the per-attempt protocol-aware buffer sink.
type AttemptCommitGate struct {
	mu             sync.Mutex
	protocol       ClientProtocol
	writer         *SerializedStreamWriter
	mode           GateMode
	maxMetadata    int
	maxMetadataAge time.Duration

	state     CommitState
	committed bool
	discarded bool
	buffer    []byte
	bufferLen int
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
	if writer == nil {
		panic("attempt commit gate requires a serialized stream writer")
	}
	return &AttemptCommitGate{
		protocol:       protocol,
		writer:         writer,
		mode:           opts.Mode,
		maxMetadata:    opts.MaxMetadataBufferBytes,
		maxMetadataAge: opts.MaxMetadataBufferAge,
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

// WriteFrame classifies and records one client-facing SSE frame. In
// buffered mode the frame is held in the attempt-local buffer until commit;
// keepalive frames pass through immediately and never advance state.
func (g *AttemptCommitGate) WriteFrame(frame string) error {
	class := ClassifyClientFrame(g.protocol, frame)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.discarded {
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
			return g.appendBufferedLocked(frame)
		}
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		g.writer.Flush()
		return nil
	}

	g.advanceStateLocked(class)

	if g.mode == GateModeImmediate || g.committed {
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		g.writer.Flush()
		return nil
	}

	// Buffered mode, not yet committed.
	if isSemanticClass(class) {
		// First semantic frame triggers the normal semantic commit: flush
		// buffered frames in original order, then this frame.
		if err := g.commitLocked(); err != nil {
			return err
		}
		if _, err := g.writer.Write([]byte(frame)); err != nil {
			return err
		}
		g.writer.Flush()
		return nil
	}

	return g.appendBufferedLocked(frame)
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

// advanceStateLocked moves the commit state forward according to the frame
// class. Unknown frames fail closed as content.
func (g *AttemptCommitGate) advanceStateLocked(class FrameClass) {
	var next CommitState
	switch class {
	case FrameClassKeepalive:
		return
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
		return
	default:
		next = CommitStateContent
	}
	if next > g.state {
		g.state = next
	}
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
// attempt committed. After commit, Discard is refused.
func (g *AttemptCommitGate) Commit() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.discarded {
		return ErrAttemptAlreadyCommitted
	}
	if err := g.commitLocked(); err != nil {
		return err
	}
	g.writer.Flush()
	return nil
}

// Discard drops the attempt-local buffer without ever touching the client
// connection. Only legal while the state is none/metadata (nothing was
// committed); after the semantic commit it returns
// ErrAttemptAlreadyCommitted.
func (g *AttemptCommitGate) Discard() error {
	g.mu.Lock()
	defer g.mu.Unlock()
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
	return nil
}
