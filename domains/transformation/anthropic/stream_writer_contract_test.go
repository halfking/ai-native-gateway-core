package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// shortWriteRW is an http.ResponseWriter whose Write accepts fewer bytes than
// requested, simulating a short write against a client connection.
type shortWriteRW struct {
	*httptest.ResponseRecorder
}

func (s *shortWriteRW) Write(p []byte) (int, error) {
	return len(p) / 2, nil
}

// panicFlushRW panics on Flush, simulating a flush-after-close panic on a
// disconnected client connection.
type panicFlushRW struct {
	*httptest.ResponseRecorder
}

func (p *panicFlushRW) Flush() {
	panic("flush after close")
}

func TestStreamWriter_ContextCancelAbortsWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sw := NewStreamWriter(ctx, httptest.NewRecorder())

	n, err := sw.Write([]byte("hello"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write after cancel: err=%v, want context.Canceled", err)
	}
	if n != 0 {
		t.Fatalf("Write after cancel: n=%d, want 0", n)
	}
	if !sw.Cancelled() {
		t.Fatal("Cancelled() should be true after ctx cancel")
	}
	if sw.Err() == nil {
		t.Fatal("Err() should report the cancellation")
	}
}

func TestStreamWriter_ShortWriteRecorded(t *testing.T) {
	sw := NewStreamWriter(context.Background(), &shortWriteRW{httptest.NewRecorder()})

	_, err := sw.Write([]byte("0123456789"))
	if err == nil {
		t.Fatal("short write should return an error")
	}
	if sw.Err() == nil || !strings.Contains(sw.Err().Error(), "short write") {
		t.Fatalf("Err()=%v, want short write error", sw.Err())
	}
	if sw.Cancelled() {
		t.Fatal("short write must not be classified as cancellation")
	}
}

func TestStreamWriter_FlushPanicRecovered(t *testing.T) {
	sw := NewStreamWriter(context.Background(), &panicFlushRW{httptest.NewRecorder()})

	sw.Flush() // must not propagate the panic

	if sw.Err() == nil || !strings.Contains(sw.Err().Error(), "flush panic") {
		t.Fatalf("Err()=%v, want flush panic recovered", sw.Err())
	}
}

func TestStreamWriter_NormalWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(context.Background(), rec)

	n, err := sw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Fatalf("n=%d, want 5", n)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body=%q, want hello", rec.Body.String())
	}
	if sw.Err() != nil || sw.Cancelled() {
		t.Fatalf("Err()=%v Cancelled()=%v, want clean", sw.Err(), sw.Cancelled())
	}
}

func TestStreamWriter_FlushNoopWhenNotFlusher(t *testing.T) {
	// A ResponseWriter without Flusher must not panic on Flush.
	sw := NewStreamWriter(context.Background(), nonFlusherRW{})
	sw.Flush()
	if sw.Err() != nil {
		t.Fatalf("Err()=%v, want nil for non-flusher Flush", sw.Err())
	}
}

func TestDeriveStreamContext_RespectsExistingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	got, gotCancel := DeriveStreamContext(ctx)
	defer gotCancel()
	if got != ctx {
		t.Fatal("DeriveStreamContext should return ctx unchanged when it already has a deadline")
	}
}

func TestDeriveStreamContext_AddsTimeoutWhenMissing(t *testing.T) {
	ctx, cancel := DeriveStreamContext(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("DeriveStreamContext should add a deadline when none exists")
	}
}

// nonFlusherRW is a minimal http.ResponseWriter that does NOT implement
// http.Flusher, used to verify StreamWriter.Flush stays safe.
type nonFlusherRW struct {
	headers http.Header
	body    []byte
}

func (w nonFlusherRW) Header() http.Header {
	if w.headers == nil {
		w.headers = http.Header{}
	}
	return w.headers
}
func (w nonFlusherRW) Write(p []byte) (int, error) { w.body = append(w.body, p...); return len(p), nil }
func (w nonFlusherRW) WriteHeader(int)             {}
