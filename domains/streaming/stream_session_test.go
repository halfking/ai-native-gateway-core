package streaming

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamSessionHeartbeatsWhileReaderIsBlocked(t *testing.T) {
	rec := newSyncRecorder()
	session := NewStreamSession(rec, 5*time.Millisecond, sseKeepaliveComment)
	ctx, cancel := context.WithCancel(context.Background())
	session.Start(ctx)
	t.Cleanup(func() { cancel(); session.Stop() })

	pipeReader, pipeWriter := io.Pipe()
	readDone := make(chan streamReadResult, 1)
	go func() {
		readDone <- readNextStreamLine(ctx, bufio.NewReader(pipeReader), pipeReader, nil, nil, streamRuntimeConfig{
			streamChunkTimeout: time.Second,
		})
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for strings.Count(rec.String(), sseKeepaliveComment) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := strings.Count(rec.String(), sseKeepaliveComment); got < 3 {
		t.Fatalf("heartbeat count = %d, want at least 3 while upstream read is blocked", got)
	}
	select {
	case result := <-readDone:
		t.Fatalf("upstream read returned before unblock: %+v", result)
	default:
	}
	_, _ = pipeWriter.Write([]byte("data: semantic\n"))
	if result := <-readDone; result.err != nil || result.line != "data: semantic\n" {
		t.Fatalf("read result = %+v", result)
	}
	_ = pipeWriter.Close()
}

func TestStreamSessionStopEndsHeartbeatLoop(t *testing.T) {
	rec := newSyncRecorder()
	session := NewStreamSession(rec, 5*time.Millisecond, sseKeepaliveComment)
	session.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	session.Stop()
	before := rec.String()
	time.Sleep(20 * time.Millisecond)
	if got := rec.String(); got != before {
		t.Fatalf("wire changed after Stop: before=%q after=%q", before, got)
	}
	session.Stop()
}

func TestStreamSessionSerializesHeartbeatAndSemanticFrames(t *testing.T) {
	rec := newSyncRecorder()
	session := NewStreamSession(rec, time.Hour, sseKeepaliveComment)
	frame := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = session.Heartbeat() }()
		go func() { defer wg.Done(); _, _ = session.Writer().Write([]byte(frame)) }()
	}
	wg.Wait()

	out := rec.String()
	if got := strings.Count(out, sseKeepaliveComment); got != 64 {
		t.Fatalf("heartbeat count = %d, want 64", got)
	}
	if got := strings.Count(out, frame); got != 64 {
		t.Fatalf("semantic frame count = %d, want 64", got)
	}
}

func TestStreamSessionHeartbeatDoesNotCommitBeforeFirstSemanticFrame(t *testing.T) {
	rec := newSyncRecorder()
	session := NewStreamSession(rec, time.Hour, sseKeepaliveComment)
	gate := NewAttemptCommitGate(ProtocolOpenAIChat, session.writer.SerializedWriter(), GateOptions{Mode: GateModeBuffered})

	if err := session.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if gate.Committed() || gate.State() != CommitStateNone {
		t.Fatalf("heartbeat changed semantic state: committed=%v state=%v", gate.Committed(), gate.State())
	}
	semantic := "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"
	if err := gate.WriteFrame(semantic); err != nil {
		t.Fatalf("semantic frame: %v", err)
	}
	if !gate.Committed() || gate.State() != CommitStateContent {
		t.Fatalf("first semantic frame did not commit: committed=%v state=%v", gate.Committed(), gate.State())
	}
	if out := rec.String(); !strings.HasPrefix(out, sseKeepaliveComment) || !strings.HasSuffix(out, semantic) {
		t.Fatalf("wire order = %q", out)
	}
}

func TestStreamSessionCommentIsCompatibleWithSSEProtocols(t *testing.T) {
	for _, protocol := range []ClientProtocol{ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropic} {
		if got := ClassifyClientFrame(protocol, sseKeepaliveComment); got != FrameClassKeepalive {
			t.Fatalf("protocol %v classified heartbeat as %v", protocol, got)
		}
	}
}

func TestStreamSessionHeartbeatDoesNotEnterSemanticCapture(t *testing.T) {
	rec := newSyncRecorder()
	session := NewStreamSession(rec, time.Hour, sseKeepaliveComment)
	sw := session.writer.SerializedWriter()
	sw.EnableCapture(1024)

	if err := session.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	semantic := []byte("data: semantic\n\n")
	if _, err := session.Writer().Write(semantic); err != nil {
		t.Fatalf("semantic write: %v", err)
	}
	captured, err := sw.Captured()
	if err != nil {
		t.Fatalf("Captured: %v", err)
	}
	if string(captured) != string(semantic) {
		t.Fatalf("captured = %q, want semantic frame only", captured)
	}
}
