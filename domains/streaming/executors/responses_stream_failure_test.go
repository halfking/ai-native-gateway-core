package executors

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type responsesBridgeProbeBody struct {
	reader  io.Reader
	readErr error
	closed  chan struct{}
	once    sync.Once
}

func (b *responsesBridgeProbeBody) Read(p []byte) (int, error) {
	if b.reader == nil {
		<-b.closed
		return 0, io.ErrClosedPipe
	}
	n, err := b.reader.Read(p)
	if err == io.EOF && b.readErr != nil {
		return n, b.readErr
	}
	return n, err
}
func (b *responsesBridgeProbeBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestResponsesStreamBridgeReadFailuresDoNotComplete(t *testing.T) {
	sentinel := errors.New("read interrupted")
	for _, readErr := range []error{nil, sentinel} {
		source := &responsesBridgeProbeBody{reader: strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), readErr: readErr, closed: make(chan struct{})}
		bridge := responsesToChatStream(context.Background(), source, "m")
		out, err := io.ReadAll(bridge)
		_ = bridge.Close()
		if err == nil || strings.Contains(string(out), "[DONE]") || strings.Contains(string(out), `"finish_reason":"stop"`) {
			t.Fatalf("read failure became completed: err=%v body=%s", err, out)
		}
		if readErr != nil && !errors.Is(err, readErr) {
			t.Fatalf("read error identity lost: %v", err)
		}
		select {
		case <-source.closed:
		case <-time.After(time.Second):
			t.Fatal("upstream body not closed")
		}
	}
}
func TestResponsesStreamBridgeCancelAndCloseReleaseBlockedReader(t *testing.T) {
	for _, cancelReader := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		source := &responsesBridgeProbeBody{closed: make(chan struct{})}
		bridge := responsesToChatStream(ctx, source, "m")
		done := make(chan error, 1)
		go func() { _, err := io.ReadAll(bridge); done <- err }()
		if cancelReader {
			cancel()
		} else {
			_ = bridge.Close()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("closed stream returned success")
			}
			if cancelReader && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked bridge leaked after cancellation/Close")
		}
		select {
		case <-source.closed:
		case <-time.After(time.Second):
			t.Fatal("upstream reader remained open")
		}
		cancel()
		_ = bridge.Close()
	}
}
func TestResponsesStreamBridgeStopsAtFirstTerminal(t *testing.T) {
	raw := canonicalTextStream + "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n"
	bridge := responsesToChatStream(context.Background(), io.NopCloser(strings.NewReader(raw)), "m")
	out, err := io.ReadAll(bridge)
	_ = bridge.Close()
	if err != nil || strings.Count(string(out), "data: [DONE]") != 1 || strings.Contains(string(out), `"error"`) {
		t.Fatalf("duplicate terminal: %v %s", err, out)
	}
}
