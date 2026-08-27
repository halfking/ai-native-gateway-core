package streaming

import (
	"errors"
	"testing"
)

// SR-12 streaming durable: the gate's durable write-ahead checkpoint hook
// (doc 18 §11.3). The hook fires exactly when the commit state advances and
// the bytes of that state are about to reach the network; a failed
// checkpoint must fail the frame write so nothing semantic is sent.

func TestAttemptCommitGateCheckpointWriteAheadOrder(t *testing.T) {
	f := &trackingFlusher{}
	var calls []CommitState
	g := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode:                   GateModeBuffered,
			MaxMetadataBufferBytes: 1024,
			BeforeSemanticCommit: func(s CommitState) error {
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
	g := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode:                   GateModeBuffered,
			MaxMetadataBufferBytes: 1024,
			BeforeSemanticCommit:   func(CommitState) error { return boom },
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
	g := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{
			Mode: GateModeImmediate,
			BeforeSemanticCommit: func(s CommitState) error {
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
	gate := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(CommitState) error {
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
	gate := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(state CommitState) error {
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
	gate := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode:                 GateModeBuffered,
		BeforeSemanticCommit: func(CommitState) error { return boom },
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
