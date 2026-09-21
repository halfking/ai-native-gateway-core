package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// SR-12 streaming durable: the gate's durable write-ahead checkpoint hook
// (doc 18 §11.3). The hook fires exactly when the commit state advances and
// the bytes of that state are about to reach the network; a failed
// checkpoint must fail the frame write so nothing semantic is sent.

func TestAttemptCommitGateCheckpointWriteAheadOrder(t *testing.T) {
	f := &trackingFlusher{}
	var calls []CommitState
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode:                   GateModeBuffered,
			MaxMetadataBufferBytes: 1024,
			BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
				calls = append(calls, s)
				return nil
			},
		})

	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"

	if err := g.WriteFrame(meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("buffered metadata must not checkpoint (write-ahead only), got %v", calls)
	}
	if err := g.WriteFrame(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if len(calls) != 1 || calls[0] != CommitStateContent {
		t.Fatalf("checkpoint calls = %v, want [content] before the wire write", calls)
	}
	if f.buf.Len() == 0 {
		t.Fatal("committed content must reach the wire after the checkpoint")
	}
}

// DB checkpoint failure must prohibit the network write (doc 18 §11.3):
// the frame write errors, the gate stays uncommitted and the attempt
// fail-closes via the existing discard_refused path.
func TestAttemptCommitGateCheckpointFailureBlocksNetworkWrite(t *testing.T) {
	f := &trackingFlusher{}
	boom := errors.New("checkpoint db down")
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode:                   GateModeBuffered,
			MaxMetadataBufferBytes: 1024,
			BeforeSemanticCommit:   func(context.Context, CommitState) error { return boom },
		})

	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	err := g.WriteFrame(content)
	if err == nil {
		t.Fatal("content frame must fail when the write-ahead checkpoint fails")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error must wrap the checkpoint cause, got %v", err)
	}
	if g.Committed() {
		t.Fatal("gate must not commit when the checkpoint fails")
	}
	if f.buf.Len() != 0 {
		t.Fatalf("nothing may reach the wire, got %q", f.buf.String())
	}
	// The attempt is now fail-closed: Discard refuses because the state
	// advanced past metadata, so the coordinator terminates the task
	// instead of transparently retrying (state outcome unknown in DB).
	if err := g.Discard(); !errors.Is(err, ErrAttemptAlreadyCommitted) {
		t.Fatalf("Discard after checkpoint failure = %v, want ErrAttemptAlreadyCommitted (fail-closed)", err)
	}
}

// Immediate mode writes every frame through, so every state advance must
// checkpoint before its bytes are sent.
func TestAttemptCommitGateCheckpointImmediateModeChecksMetadata(t *testing.T) {
	f := &trackingFlusher{}
	var calls []CommitState
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode: GateModeImmediate,
			BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
				calls = append(calls, s)
				return nil
			},
		})
	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	if err := g.WriteFrame(meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if len(calls) != 1 || calls[0] != CommitStateMetadata {
		t.Fatalf("immediate mode must checkpoint metadata before the write, got %v", calls)
	}
	if f.buf.Len() == 0 {
		t.Fatal("immediate mode still writes through after a successful checkpoint")
	}
}

func TestAttemptCommitGateCheckpointFailureBlocksLaterWrites(t *testing.T) {
	f := &trackingFlusher{}
	boom := errors.New("checkpoint db down")
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(context.Context, CommitState) error {
			return boom
		},
	})
	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	if err := gate.WriteFrame(content); !errors.Is(err, boom) {
		t.Fatalf("initial content write = %v, want checkpoint error", err)
	}
	if got := f.buf.Len(); got != 0 {
		t.Fatalf("checkpoint failure wrote %d bytes", got)
	}
	if got := gate.State(); got != CommitStateContent {
		t.Fatalf("state = %v, want content after failed checkpoint", got)
	}

	for name, write := range map[string]func() error{
		"write frame":    func() error { return gate.WriteFrame(content) },
		"commit":         gate.Commit,
		"finish":         func() error { return gate.FinishAttempt("data: [DONE]\n\n") },
		"flush holdback": gate.FlushHoldback,
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(); !errors.Is(err, boom) {
				t.Fatalf("operation error = %v, want original checkpoint error", err)
			}
		})
	}
	if got := f.buf.Len(); got != 0 {
		t.Fatalf("later operations wrote %d bytes after checkpoint failure", got)
	}
}

func TestAttemptCommitGateCheckpointCoversTerminalPartial(t *testing.T) {
	f := &trackingFlusher{}
	var calls []CommitState
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(_ context.Context, state CommitState) error {
			calls = append(calls, state)
			return nil
		},
	})
	partial := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if err := gate.FinishAttempt(partial); err != nil {
		t.Fatalf("terminal partial = %v", err)
	}
	if len(calls) != 1 || calls[0] != CommitStateTerminal {
		t.Fatalf("checkpoint calls = %v, want [terminal]", calls)
	}
	if gate.State() != CommitStateTerminal || !gate.Committed() {
		t.Fatalf("gate state=%v committed=%v, want terminal+committed", gate.State(), gate.Committed())
	}
	if got := f.buf.String(); got != partial {
		t.Fatalf("wire = %q, want %q", got, partial)
	}
}

func TestAttemptCommitGateCheckpointFailureBlocksTerminalPartial(t *testing.T) {
	f := &trackingFlusher{}
	boom := errors.New("terminal checkpoint down")
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode:                 GateModeBuffered,
		BeforeSemanticCommit: func(context.Context, CommitState) error { return boom },
	})
	partial := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if err := gate.FinishAttempt(partial); !errors.Is(err, boom) {
		t.Fatalf("terminal partial = %v, want checkpoint error", err)
	}
	if gate.Committed() {
		t.Fatal("terminal partial must not commit when checkpoint fails")
	}
	if f.buf.Len() != 0 {
		t.Fatalf("terminal partial reached wire: %q", f.buf.String())
	}
	if err := gate.FinishAttempt(partial); !errors.Is(err, boom) {
		t.Fatalf("repeated terminal partial = %v, want sticky checkpoint error", err)
	}
}

func TestAttemptCommitGateFlushHoldbackCheckpointFailure(t *testing.T) {
	f := &trackingFlusher{}
	boom := errors.New("checkpoint db down")
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode:                 GateModeBuffered,
		HoldbackWindow:       time.Second,
		HoldbackMaxChunks:    10,
		BeforeSemanticCommit: func(context.Context, CommitState) error { return boom },
	})
	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	if err := gate.WriteFrame(content); err != nil {
		t.Fatalf("write content to holdback: %v", err)
	}
	if got := gate.HoldbackHeldChunks(); got != 1 {
		t.Fatalf("held chunks = %d, want 1", got)
	}
	if got := f.buf.Len(); got != 0 {
		t.Fatalf("holdback window wrote %d bytes, want 0", got)
	}

	err := gate.FlushHoldback()
	if !errors.Is(err, boom) {
		t.Fatalf("FlushHoldback() = %v, want checkpoint error", err)
	}
	if !errors.Is(err, ErrAttemptCheckpointFailed) {
		t.Fatalf("FlushHoldback() error not wrapped with sentinel: %v", err)
	}
	if got := f.buf.Len(); got != 0 {
		t.Fatalf("checkpoint failure wrote %d bytes", got)
	}
	if got := gate.State(); got != CommitStateContent {
		t.Fatalf("state = %v, want content after holdback checkpoint failure", got)
	}
	if gate.Committed() {
		t.Fatal("gate must not commit after holdback checkpoint failure")
	}

	for _, op := range []struct {
		name string
		fn   func() error
	}{
		{"second FlushHoldback", gate.FlushHoldback},
		{"WriteFrame", func() error { return gate.WriteFrame(content) }},
		{"Commit", gate.Commit},
		{"FinishAttempt", func() error { return gate.FinishAttempt("") }},
	} {
		t.Run(op.name, func(t *testing.T) {
			if err := op.fn(); !errors.Is(err, boom) || !errors.Is(err, ErrAttemptCheckpointFailed) {
				t.Fatalf("%s = %v, want sticky checkpoint error", op.name, err)
			}
		})
	}
}

func TestAttemptCommitGateImmediateMetadataCheckpointFailure(t *testing.T) {
	f := &trackingFlusher{}
	boom := errors.New("metadata checkpoint failed")
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode:                 GateModeImmediate,
		BeforeSemanticCommit: func(context.Context, CommitState) error { return boom },
	})
	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	err := gate.WriteFrame(meta)
	if !errors.Is(err, boom) {
		t.Fatalf("immediate metadata write = %v, want checkpoint error", err)
	}
	if !errors.Is(err, ErrAttemptCheckpointFailed) {
		t.Fatalf("error not wrapped with sentinel: %v", err)
	}
	if got := f.buf.Len(); got != 0 {
		t.Fatalf("checkpoint failure wrote %d bytes", got)
	}
	if got := gate.State(); got != CommitStateMetadata {
		t.Fatalf("state = %v, want metadata", got)
	}
	if gate.Committed() {
		t.Fatal("gate must not commit after metadata checkpoint failure")
	}

	// Metadata-only checkpoint failure: state < content, so Discard is allowed.
	if err := gate.Discard(); err != nil {
		t.Fatalf("Discard after metadata checkpoint failure = %v, want success", err)
	}

	// After Discard, later writes return ErrAttemptDiscarded.
	if err := gate.WriteFrame(meta); !errors.Is(err, ErrAttemptDiscarded) {
		t.Fatalf("write after discard = %v, want ErrAttemptDiscarded", err)
	}
}

func TestAttemptCommitGateCommitExecutesCheckpoint(t *testing.T) {
	f := &trackingFlusher{}
	var calls []CommitState
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
			calls = append(calls, s)
			return nil
		},
	})
	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	if err := gate.WriteFrame(meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("buffered metadata triggered checkpoint: %v", calls)
	}

	if err := gate.Commit(); err != nil {
		t.Fatalf("Commit() = %v", err)
	}
	if len(calls) != 1 || calls[0] != CommitStateMetadata {
		t.Fatalf("Commit checkpoint calls = %v, want [metadata]", calls)
	}
	if !gate.Committed() {
		t.Fatal("gate not committed after Commit()")
	}
	if got := f.buf.String(); got != meta {
		t.Fatalf("wire = %q, want %q", got, meta)
	}
}

func TestAttemptCommitGateTerminalPartialThreeModes(t *testing.T) {
	tests := []struct {
		name     string
		mode     GateMode
		setup    func(*AttemptCommitGate) error
		protocol ClientProtocol
		partial  string
	}{
		{
			name:     "immediate OpenAI Chat [DONE]",
			mode:     GateModeImmediate,
			protocol: ProtocolOpenAIChat,
			partial:  "data: [DONE]\n\n",
		},
		{
			name:     "buffered committed Anthropic message_stop",
			mode:     GateModeBuffered,
			protocol: ProtocolAnthropic,
			partial:  "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			setup: func(g *AttemptCommitGate) error {
				content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"
				return g.WriteFrame(content)
			},
		},
		{
			name:     "buffered uncommitted OpenAI Responses response.completed",
			mode:     GateModeBuffered,
			protocol: ProtocolOpenAIResponses,
			partial:  "event: response.completed\ndata: {}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &trackingFlusher{}
			var calls []CommitState
			gate := NewAttemptCommitGate(context.Background(), tt.protocol, NewSerializedStreamWriter(f), GateOptions{
				Mode: tt.mode,
				BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
					calls = append(calls, s)
					return nil
				},
			})
			if tt.setup != nil {
				if err := tt.setup(gate); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}

			if err := gate.FinishAttempt(tt.partial); err != nil {
				t.Fatalf("FinishAttempt = %v", err)
			}

			if !containsCommitState(calls, CommitStateTerminal) {
				t.Fatalf("checkpoint calls = %v, want terminal", calls)
			}
			if gate.State() != CommitStateTerminal {
				t.Fatalf("state = %v, want terminal", gate.State())
			}
			if !gate.Committed() {
				t.Fatal("gate not committed after terminal partial")
			}
			if !strings.Contains(f.buf.String(), strings.TrimSpace(tt.partial)) {
				t.Fatalf("wire missing terminal partial: %q", f.buf.String())
			}
		})
	}
}

func containsCommitState(states []CommitState, target CommitState) bool {
	for _, s := range states {
		if s == target {
			return true
		}
	}
	return false
}
