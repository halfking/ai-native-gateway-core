package streaming

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
)

// SR-W1 SerializedStreamWriter (doc 18 §9.3): once an attempt commits,
// keepalive frames, status frames and the protocol bridge share one
// serialized write channel. A write failure detaches the connection — the
// writer must never touch the underlying ResponseWriter again.

type trackingFlusher struct {
	buf     bytes.Buffer
	flushes int
	fail    bool
}

func (f *trackingFlusher) Write(p []byte) (int, error) {
	if f.fail {
		return 0, errors.New("write failed")
	}
	return f.buf.Write(p)
}

func (f *trackingFlusher) Flush() { f.flushes++ }

func TestSerializedStreamWriterWriteAndFlush(t *testing.T) {
	f := &trackingFlusher{}
	w := NewSerializedStreamWriter(f)
	if _, err := w.Write([]byte("data: x\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Flush()
	if got := f.buf.String(); got != "data: x\n\n" {
		t.Fatalf("payload = %q", got)
	}
	if f.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", f.flushes)
	}
}

func TestSerializedStreamWriterDetachedAfterWriteFailure(t *testing.T) {
	f := &trackingFlusher{fail: true}
	w := NewSerializedStreamWriter(f)
	if _, err := w.Write([]byte("data: x\n\n")); err == nil {
		t.Fatal("expected write error")
	}
	if !w.Detached() {
		t.Fatal("writer should be detached after write failure")
	}
	// After detaching, writes become silent no-ops instead of hammering a
	// dead connection.
	if n, err := w.Write([]byte("data: y\n\n")); err != nil || n != len("data: y\n\n") {
		t.Fatalf("post-detach write = (%d, %v), want full-length nil", n, err)
	}
	if f.buf.Len() != 0 {
		t.Fatalf("detached writer must not touch underlying writer, buf = %q", f.buf.String())
	}
}

// TestSerializedStreamWriterConcurrentWritesNeverInterleave verifies the
// race-free serialization guarantee: two goroutines writing complete frames
// must never produce an interleaved frame on the wire.
func TestSerializedStreamWriterConcurrentWritesNeverInterleave(t *testing.T) {
	f := &trackingFlusher{}
	w := NewSerializedStreamWriter(f)
	frame := "event: content_block_delta\ndata: {\"delta\":{\"text\":\"hello world\"}}\n\n"

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Write([]byte(frame)); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}()
	}
	wg.Wait()

	out := f.buf.String()
	if n := strings.Count(out, frame); n != 64 {
		t.Fatalf("wire contains %d intact frames, want 64 — frames interleaved or lost:\n%q", n, out)
	}
}
