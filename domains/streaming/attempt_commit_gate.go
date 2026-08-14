package streaming

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// SR-W1 protocol commit gate (doc 18 §5.1 AttemptCommitGate, §9.3, §10).
//
// The gate sits between a protocol bridge and the client connection as an
// http.ResponseWriter wrapper. Bridges keep writing frames exactly as they do
// today; the gate reassembles those bytes into complete SSE frames, classifies
// each frame, and decides per frame:
//
//   - comment / stable-connection frames go straight to the wire (transport
//     keepalive must keep flowing while an attempt is being replaced);
//   - attempt-bound frames accumulate in the attempt-local buffer until
//     Commit() — before that they are invisible to the client and can be
//     dropped wholesale via Discard() for a transparent retry;
//   - the first content/tool_call/terminal frame triggers the semantic commit
//     (AttemptCommitFirstSemantic policy), flushing the buffer in order;
//   - after commit everything passes straight through, still serialized.
//
// Phase 0B compatibility: AttemptCommitImmediate commits on the very first
// frame, i.e. at the exact pre-gate timing, so the wire bytes are identical
// to the ungated stream.

// CommitState is the attempt-local commit state of the gate (doc 18 §5.1).
type CommitState string

const (
	CommitStateNone     CommitState = "none"
	CommitStateMetadata CommitState = "metadata"
	CommitStateContent  CommitState = "content"
	CommitStateToolCall CommitState = "tool_call"
	CommitStateTerminal CommitState = "terminal"
)

var commitStateRank = map[CommitState]int{
	CommitStateNone:     0,
	CommitStateMetadata: 1,
	CommitStateContent:  2,
	CommitStateToolCall: 3,
	CommitStateTerminal: 4,
}

// AttemptCommitPolicy selects when the gate commits.
type AttemptCommitPolicy string

const (
	// AttemptCommitImmediate is the Phase 0B baseline: commit on the first
	// frame written, preserving the legacy timing (and wire bytes).
	AttemptCommitImmediate AttemptCommitPolicy = "immediate"

	// AttemptCommitFirstSemantic is the deferred semantics: only the first
	// content/tool_call/terminal frame commits; metadata stays buffered.
	AttemptCommitFirstSemantic AttemptCommitPolicy = "first_semantic"
)

const (
	// DefaultMaxAttemptMetadataBytes caps the attempt metadata buffer. Doc 18
	// §5.1: exceeding it returns attempt_metadata_buffer_exceeded; the gate
	// must never force a Commit() just to release memory.
	DefaultMaxAttemptMetadataBytes = 64 * 1024

	// DefaultMaxAttemptMetadataAge bounds how long attempt metadata may sit
	// buffered (doc 18 §5.1 byte/TIME cap). Checked lazily on the next
	// buffered write — no timer goroutine.
	DefaultMaxAttemptMetadataAge = 30 * time.Second
)

var (
	// ErrAttemptMetadataBufferExceeded is surfaced by Write (and latched in
	// Err()) when buffered attempt metadata exceeds the configured cap.
	ErrAttemptMetadataBufferExceeded = errors.New("attempt_metadata_buffer_exceeded")

	// ErrAttemptNotDroppable is returned by Discard after a semantic frame
	// (content/tool_call/terminal) has been committed for this attempt.
	ErrAttemptNotDroppable = errors.New("attempt_not_droppable")

	// ErrGateDetached is returned by Commit/Write after the underlying
	// connection failed.
	ErrGateDetached = errors.New("attempt_gate_detached")

	// ErrGateOverflowCommitted is returned by Commit while the metadata
	// overflow latch is set (force-commit is forbidden).
	ErrGateOverflowCommitted = errors.New("attempt_metadata_overflow_commit_forbidden")
)

// AttemptGateConfig parameterizes a gate instance.
type AttemptGateConfig struct {
	// Protocol is the CLIENT-facing protocol the bridge emits.
	Protocol FrameProtocol
	// Policy selects immediate (Phase 0B) or deferred commit semantics.
	Policy AttemptCommitPolicy
	// MaxAttemptMetadataBytes caps the attempt metadata buffer; 0 = default.
	MaxAttemptMetadataBytes int
	// MaxAttemptMetadataAge caps how long attempt metadata may stay
	// buffered; 0 = default. Overflow surfaces the same
	// attempt_metadata_buffer_exceeded error as the byte cap.
	MaxAttemptMetadataAge time.Duration
}

// AttemptCommitGate is the per-attempt protocol buffer described in doc 18
// §5.1. It is NOT safe to share one gate across attempts: create one per
// attempt (per bridge invocation).
type AttemptCommitGate struct {
	cfg  AttemptGateConfig
	sw   *SerializedStreamWriter
	real http.ResponseWriter

	mu           sync.Mutex
	committed    bool
	state        CommitState
	contentSeen  bool
	toolSeen     bool
	terminalSeen bool

	// assembler accumulates raw bytes until a complete frame (blank line)
	// is available; buffer holds classified-but-uncommitted attempt frames.
	// flushedPrefix counts leading assembler bytes already written by an
	// eager commit-time flush of a still-incomplete frame — when that frame
	// completes, only the unflushed remainder is emitted, preserving frame
	// boundaries and byte order.
	assembler     bytes.Buffer
	flushedPrefix int
	buffer        bytes.Buffer
	metadataLen   int
	firstMetaAt   time.Time
	overflow      bool
}

// NewAttemptCommitGate builds a per-attempt gate over the real client
// connection. One gate per bridge invocation (per attempt) — never shared.
func NewAttemptCommitGate(real http.ResponseWriter, cfg AttemptGateConfig) *AttemptCommitGate {
	if cfg.Protocol == "" {
		cfg.Protocol = FrameProtocolOpenAIChat
	}
	if cfg.Policy == "" {
		cfg.Policy = AttemptCommitFirstSemantic
	}
	if cfg.MaxAttemptMetadataBytes <= 0 {
		cfg.MaxAttemptMetadataBytes = DefaultMaxAttemptMetadataBytes
	}
	if cfg.MaxAttemptMetadataAge <= 0 {
		cfg.MaxAttemptMetadataAge = DefaultMaxAttemptMetadataAge
	}
	flusher, _ := real.(http.Flusher)
	return &AttemptCommitGate{
		cfg:   cfg,
		real:  real,
		sw:    NewSerializedStreamWriter(real, flusher),
		state: CommitStateNone,
	}
}

// ResponseWriter returns the bridge-facing writer. Header() and
// WriteHeader() pass through to the real connection — headers are
// connection-level, not attempt frames.
func (g *AttemptCommitGate) ResponseWriter() http.ResponseWriter { return &gateWriter{g: g} }

type gateWriter struct {
	g *AttemptCommitGate
}

func (gw *gateWriter) Header() http.Header  { return gw.g.real.Header() }
func (gw *gateWriter) WriteHeader(code int) { gw.g.real.WriteHeader(code) }

func (gw *gateWriter) Write(p []byte) (int, error) { return gw.g.writeRaw(p) }
func (gw *gateWriter) Flush()                      { gw.g.sw.Flush() }

// writeRaw feeds raw bridge output through frame reassembly and per-frame
// gate logic. Bytes are never altered — frames are re-emitted verbatim,
// including their original terminators. The returned n always equals len(p):
// the gate consumes the whole write (into buffer or wire); a non-nil error is
// a latch signal (overflow/detach), not a byte count.
func (g *AttemptCommitGate) writeRaw(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.sw.Detached() {
		return 0, ErrGateDetached
	}

	g.assembler.Write(p)
	for {
		frame, rest, ok := nextSSEFrame(g.assembler.Bytes())
		if !ok {
			break
		}
		// Emit only the part of the frame not yet flushed by an eager
		// commit; classify on the FULL frame so prefixes cannot corrupt
		// classification.
		out := frame
		if g.flushedPrefix > 0 {
			if g.flushedPrefix < len(frame) {
				out = frame[g.flushedPrefix:]
			} else {
				out = nil
			}
			g.flushedPrefix = 0
		}
		g.assembler.Reset()
		g.assembler.Write(rest)
		if err := g.acceptFrameLocked(out, frame); err != nil {
			return len(p), err
		}
	}
	if g.sw.Detached() {
		return len(p), ErrGateDetached
	}
	return len(p), nil
}

// nextSSEFrame splits the first complete frame (content + terminator) off the
// head of buf. Handles both "\n\n" and "\r\n\r\n" terminators.
func nextSSEFrame(buf []byte) (frame []byte, rest []byte, ok bool) {
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] == '\n' {
			next := buf[i+1]
			if next == '\n' {
				return buf[:i+2], buf[i+2:], true
			}
			if next == '\r' && i+2 < len(buf) && buf[i+2] == '\n' {
				return buf[:i+3], buf[i+3:], true
			}
		}
	}
	return nil, nil, false
}

// acceptFrameLocked applies the gate policy to one complete frame. `out` is
// the not-yet-flushed portion of the frame; `full` the complete frame bytes
// used for classification.
func (g *AttemptCommitGate) acceptFrameLocked(out, full []byte) error {
	class := ClassifySSEFrame(g.cfg.Protocol, string(full))
	g.applyStateLocked(class)

	switch {
	case class == FrameClassComment || class == FrameClassStableConnMetadata:
		// Connection-level traffic flows even before commit — but never
		// AROUND already-buffered attempt frames: reordering protocol
		// frames (e.g. an anthropic ping jumping ahead of message_start)
		// breaks client parsers. Queue in order; pass through directly
		// only when no attempt frames are pending.
		if !g.committed && g.cfg.Policy != AttemptCommitImmediate && g.buffer.Len() > 0 {
			g.buffer.Write(out)
			return nil
		}
		if len(out) > 0 {
			if _, err := g.sw.Write(out); err != nil {
				return ErrGateDetached
			}
		}
		return nil

	case g.committed || g.cfg.Policy == AttemptCommitImmediate:
		if len(out) > 0 {
			if _, err := g.sw.Write(out); err != nil {
				return ErrGateDetached
			}
		}
		if !g.committed {
			// Immediate policy: first frame commits at legacy timing.
			g.committed = true
		}
		return nil

	case class == FrameClassAttemptMetadata:
		if g.overflow {
			return ErrAttemptMetadataBufferExceeded
		}
		if g.firstMetaAt.IsZero() {
			g.firstMetaAt = time.Now()
		} else if time.Since(g.firstMetaAt) > g.cfg.MaxAttemptMetadataAge {
			g.overflow = true
			metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(string(g.cfg.Protocol)).Inc()
			return fmt.Errorf("%w (age=%s cap=%s)",
				ErrAttemptMetadataBufferExceeded, time.Since(g.firstMetaAt), g.cfg.MaxAttemptMetadataAge)
		}
		g.buffer.Write(out)
		g.metadataLen += len(out)
		if g.metadataLen > g.cfg.MaxAttemptMetadataBytes {
			g.overflow = true
			metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(string(g.cfg.Protocol)).Inc()
			return fmt.Errorf("%w (buffered=%d cap=%d)",
				ErrAttemptMetadataBufferExceeded, g.metadataLen, g.cfg.MaxAttemptMetadataBytes)
		}
		return nil

	default:
		// content / tool_call / terminal under deferred policy: the first
		// semantic frame commits the whole attempt buffer in order.
		g.buffer.Write(out)
		return g.commitLocked()
	}
}

func (g *AttemptCommitGate) applyStateLocked(class FrameClass) {
	switch class {
	case FrameClassAttemptMetadata, FrameClassStableConnMetadata:
		g.promoteLocked(CommitStateMetadata)
	case FrameClassContent:
		g.contentSeen = true
		g.promoteLocked(CommitStateContent)
	case FrameClassToolCall:
		g.toolSeen = true
		g.promoteLocked(CommitStateToolCall)
	case FrameClassTerminal:
		g.terminalSeen = true
		g.promoteLocked(CommitStateTerminal)
	}
}

func (g *AttemptCommitGate) promoteLocked(s CommitState) {
	if commitStateRank[s] > commitStateRank[g.state] {
		g.state = s
	}
}

// commitLocked flushes the attempt buffer (plus any assembler bytes not yet
// on the wire) to the real connection, in order, and switches the gate to
// direct passthrough. Eagerly flushed partial-frame bytes are tracked in
// flushedPrefix so the remainder of that frame is emitted when it completes.
func (g *AttemptCommitGate) commitLocked() error {
	if g.committed {
		return nil
	}
	if g.sw.Detached() {
		return ErrGateDetached
	}
	out := make([]byte, 0, g.buffer.Len()+g.assembler.Len())
	out = append(out, g.buffer.Bytes()...)
	if g.assembler.Len() > g.flushedPrefix {
		out = append(out, g.assembler.Bytes()[g.flushedPrefix:]...)
		g.flushedPrefix = g.assembler.Len()
	}
	if len(out) > 0 {
		if _, err := g.sw.Write(out); err != nil {
			return ErrGateDetached
		}
	}
	g.buffer.Reset()
	g.metadataLen = 0
	g.firstMetaAt = time.Time{}
	g.committed = true
	return nil
}

// Commit flushes the attempt buffer to the client and switches the gate to
// direct passthrough. It is refused while the metadata-overflow latch is set
// (doc 18 §5.1: memory is never released by force-committing) and after the
// connection detached.
func (g *AttemptCommitGate) Commit() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.overflow && !g.committed {
		return ErrGateOverflowCommitted
	}
	return g.commitLocked()
}

// Discard drops the attempt-local buffer for a transparent retry. It is only
// legal before any semantic frame was committed; afterwards it fails with
// ErrAttemptNotDroppable. Comments/stable metadata already on the wire stay
// there — they are connection-level.
func (g *AttemptCommitGate) Discard() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed || g.state == CommitStateContent ||
		g.state == CommitStateToolCall || g.state == CommitStateTerminal {
		return ErrAttemptNotDroppable
	}
	g.buffer.Reset()
	g.assembler.Reset()
	g.flushedPrefix = 0
	g.metadataLen = 0
	g.firstMetaAt = time.Time{}
	g.overflow = false
	return nil
}

// State returns the current commit state (monotonic across the attempt).
func (g *AttemptCommitGate) State() CommitState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// Committed reports whether the attempt has been committed to the client.
func (g *AttemptCommitGate) Committed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed
}

// ContentSeen reports whether any content frame was classified, even for a
// terminal-only stream (empty response detection, doc 18 §10.1: ChunkCount
// cannot stand in for this).
func (g *AttemptCommitGate) ContentSeen() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.contentSeen
}

// ToolCallSeen reports whether any tool-call frame was classified.
func (g *AttemptCommitGate) ToolCallSeen() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.toolSeen
}

// TerminalSeen reports whether any terminal frame was classified.
func (g *AttemptCommitGate) TerminalSeen() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.terminalSeen
}

// Err returns the latched gate error (metadata overflow), if any.
func (g *AttemptCommitGate) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.overflow {
		return ErrAttemptMetadataBufferExceeded
	}
	return nil
}

// Detached reports whether the underlying connection is gone.
func (g *AttemptCommitGate) Detached() bool { return g.sw.Detached() }

// MayWriteTerminal reports whether a bridge may keep its LEGACY error-path
// terminal-frame rendering (error SSE / synthesized [DONE] / final events).
//
//   - no gate attached (gate disabled): true — legacy behavior, verbatim;
//   - immediate policy (Phase 0B): true — first write commits at legacy
//     timing, so error frames reach the wire exactly as before;
//   - deferred policy: true only after the attempt committed (the client saw
//     semantic bytes and must receive a well-formed ending). While the gate
//     still holds an uncommitted attempt the bridge must return a structured
//     outcome ONLY — the survival coordinator owns the final protocol
//     rendering (doc 18 §9.3).
func (g *AttemptCommitGate) MayWriteTerminal() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed || g.cfg.Policy == AttemptCommitImmediate
}

// ─── bridge wiring (Phase 0B) ──────────────────────────────────────────────
//
// wrapAttemptWriter is the single integration point between the protocol
// bridges and the commit gate. With the gate disabled (default) it returns
// the writer unchanged, so the legacy byte path is preserved verbatim; with
// the gate enabled the bridge's frames flow through the gate wrapper.
//
// LLM_GATEWAY_ATTEMPT_COMMIT_GATE=false is the production default until the
// survival coordinator (SR-W2) owns Commit()/Discard() decisions.

var (
	attemptGateEnabledOverride atomic.Value // bool
	attemptGatePolicyOverride  atomic.Value // AttemptCommitPolicy
)

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func attemptGateEnabled() bool {
	if v, ok := attemptGateEnabledOverride.Load().(bool); ok {
		return v
	}
	return envBool("LLM_GATEWAY_ATTEMPT_COMMIT_GATE", false)
}

func attemptGateCommitPolicy() AttemptCommitPolicy {
	if v, ok := attemptGatePolicyOverride.Load().(AttemptCommitPolicy); ok {
		return v
	}
	switch envString("LLM_GATEWAY_ATTEMPT_COMMIT_GATE_POLICY", "semantic") {
	case "immediate":
		return AttemptCommitImmediate
	default:
		return AttemptCommitFirstSemantic
	}
}

// setAttemptGateForTest overrides the enabled flag and commit policy for the
// duration of a test. The returned restore function must be deferred.
func setAttemptGateForTest(enabled bool, policy AttemptCommitPolicy) (restore func()) {
	oldEnabled, _ := attemptGateEnabledOverride.Load().(bool)
	oldPolicy, _ := attemptGatePolicyOverride.Load().(AttemptCommitPolicy)
	attemptGateEnabledOverride.Store(enabled)
	attemptGatePolicyOverride.Store(policy)
	return func() {
		attemptGateEnabledOverride.Store(oldEnabled)
		attemptGatePolicyOverride.Store(oldPolicy)
	}
}

// wrapAttemptWriter wraps the client writer of a streaming bridge in a commit
// gate for the given CLIENT protocol. Disabled gates are a pure identity
// function — zero overhead, zero behavioral delta; the returned gate is nil
// and every gate.MayWriteTerminal() call site degrades to legacy behavior.
func wrapAttemptWriter(w http.ResponseWriter, protocol FrameProtocol) (http.ResponseWriter, *AttemptCommitGate) {
	if !attemptGateEnabled() {
		return w, nil
	}
	gate := NewAttemptCommitGate(w, AttemptGateConfig{
		Protocol: protocol,
		Policy:   attemptGateCommitPolicy(),
	})
	return gate.ResponseWriter(), gate
}
