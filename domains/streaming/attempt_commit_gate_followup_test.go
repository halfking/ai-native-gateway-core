package streaming

import (
	"context"
	"testing"
)

func TestAttemptCommitGateCommitCheckpointRunsOncePerState(t *testing.T) {
	f := &trackingFlusher{}
	calls := make([]CommitState, 0, 2)
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(_ context.Context, state CommitState) error {
			calls = append(calls, state)
			return nil
		},
	})
	meta := "event: message_start\ndata: {}\n\n"
	if err := g.WriteFrame(meta); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != CommitStateMetadata {
		t.Fatalf("checkpoint calls=%v, want [metadata]", calls)
	}
}

func TestAttemptCommitGateCheckpointAdvancesOnce(t *testing.T) {
	f := &trackingFlusher{}
	calls := make([]CommitState, 0, 2)
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f), GateOptions{
		Mode: GateModeBuffered,
		BeforeSemanticCommit: func(_ context.Context, state CommitState) error {
			calls = append(calls, state)
			return nil
		},
	})
	if err := g.WriteFrame("event: message_start\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFrame("event: content_block_delta\ndata: {\"delta\":{\"text\":\"x\"}}\n\n"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != CommitStateMetadata || calls[1] != CommitStateContent {
		t.Fatalf("checkpoint calls=%v, want [metadata content]", calls)
	}
}

func TestSerializedStreamWriterCaptureExcludesFailedWrite(t *testing.T) {
	f := &trackingFlusher{fail: true}
	s := NewSerializedStreamWriter(f)
	s.EnableCapture(1024)
	_, err := s.Write([]byte("data: failed\n\n"))
	if err == nil {
		t.Fatal("expected write error")
	}
	captured, captureErr := s.Captured()
	if captureErr != nil {
		t.Fatalf("Captured: %v", captureErr)
	}
	if len(captured) != 0 {
		t.Fatalf("failed write captured %q, want empty", captured)
	}
}

func TestSerializedStreamWriterCaptureExcludesDetachedWrite(t *testing.T) {
	f := &trackingFlusher{}
	s := NewSerializedStreamWriter(f)
	s.EnableCapture(1024)
	if _, err := s.Write([]byte("data: ok\n\n")); err != nil {
		t.Fatal(err)
	}
	if !s.Detached() {
		// Detach through a failing flush so the subsequent write is a no-op.
		f.panicFlush = true
		if err := s.FlushError(); err == nil {
			t.Fatal("expected flush panic")
		}
	}
	_, _ = s.Write([]byte("data: ignored\n\n"))
	captured, err := s.Captured()
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != "data: ok\n\n" {
		t.Fatalf("capture=%q, want only successful write", captured)
	}
}
