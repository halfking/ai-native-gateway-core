package middleware

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

type tracingCapabilityWriter struct {
	*httptest.ResponseRecorder
	flushes  int
	flushErr error
	hijacked bool
	peer     net.Conn
	pushed   string
	readFrom bool
}

func (w *tracingCapabilityWriter) Flush() { w.flushes++ }
func (w *tracingCapabilityWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}
func (w *tracingCapabilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	server, peer := net.Pipe()
	w.peer = peer
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}
func (w *tracingCapabilityWriter) Push(target string, _ *http.PushOptions) error {
	w.pushed = target
	return nil
}
func (w *tracingCapabilityWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom = true
	return io.Copy(w.ResponseRecorder, r)
}

func TestStatusRecordingWriterPreservesOptionalHTTPInterfaces(t *testing.T) {
	delegate := &tracingCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := &statusRecordingWriter{ResponseWriter: delegate}

	if err := writer.FlushError(); err != nil {
		t.Fatalf("FlushError() error = %v", err)
	}
	if delegate.flushes != 1 || writer.status != http.StatusOK {
		t.Fatalf("FlushError() flushes=%d status=%d, want 1 and 200", delegate.flushes, writer.status)
	}

	conn, _, err := writer.Hijack()
	if err != nil {
		t.Fatalf("Hijack() error = %v", err)
	}
	if !delegate.hijacked {
		t.Fatal("Hijack() was not forwarded")
	}
	_ = conn.Close()
	_ = delegate.peer.Close()

	if err := writer.Push("/asset.js", nil); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if delegate.pushed != "/asset.js" {
		t.Fatalf("Push() target = %q", delegate.pushed)
	}

	written, err := writer.ReadFrom(bytes.NewBufferString("stream body"))
	if err != nil {
		t.Fatalf("ReadFrom() error = %v", err)
	}
	if written != int64(len("stream body")) || !delegate.readFrom {
		t.Fatalf("ReadFrom() written=%d forwarded=%v", written, delegate.readFrom)
	}
	if got := delegate.Body.String(); got != "stream body" {
		t.Fatalf("body = %q", got)
	}
}

func TestTracingMiddlewareDefaultsToHTTP200(t *testing.T) {
	wrapped := NewTracingMiddleware().Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
