package streaming

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

func TestAttemptCommitGateCheckpointRunsOncePerStateRegression(t *testing.T) {
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
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != CommitStateMetadata {
		t.Fatalf("checkpoint calls=%v, want [metadata]", calls)
	}
}

func TestAttemptCommitGateCheckpointAdvancesOncePerStateRegression(t *testing.T) {
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

func TestSerializedStreamWriterCaptureExcludesFailedAndDetachedWritesRegression(t *testing.T) {
	f := &trackingFlusher{fail: true}
	s := NewSerializedStreamWriter(f)
	s.EnableCapture(1024)
	if _, err := s.Write([]byte("data: failed\n\n")); err == nil {
		t.Fatal("expected write error")
	}
	f.fail = false
	if _, err := s.Write([]byte("data: detached\n\n")); err != nil {
		t.Fatal(err)
	}
	captured, err := s.Captured()
	if err != nil {
		t.Fatalf("Captured: %v", err)
	}
	if len(captured) != 0 {
		t.Fatalf("failed or detached write captured %q", captured)
	}
}

type trailingPartialExecutorRegression struct{ partial string }

func (e *trailingPartialExecutorRegression) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if _, err := params.W.Write([]byte(e.partial)); err != nil {
		return nil, err
	}
	return &executors.ExecuteResult{}, nil
}

func TestSurvivalCoordinatorFlushesSuccessfulTrailingPartialRegression(t *testing.T) {
	const partial = `data: {"choices":[{"delta":{"content":"tail"}}]}`
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = &trailingPartialExecutorRegression{partial: partial}

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{IsStream: true})
	if !res.Succeed {
		t.Fatalf("expected success, decision=%v reason=%s", res.Decision.Action, res.Decision.Reason)
	}
	if got := h.flusher.buf.String(); !strings.Contains(got, partial) {
		t.Fatalf("successful trailing partial missing from wire: %q", got)
	}
}
