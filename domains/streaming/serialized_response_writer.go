package streaming

import (
	"net/http"
)

// serialized_response_writer.go — SR-W2 (doc 20 A-P2-6)
//
// serializedResponseWriter routes one connection's body writes and flushes
// through a shared SerializedStreamWriter while delegating header/status
// calls to the original ResponseWriter. Producers that used to hold the raw
// ResponseWriter — the pre-stream keepalive loop, node-switch notices, and
// the protocol bridges once the stream starts — all write through this
// adapter so no two of them can ever interleave a frame on the client
// connection.
//
// The pre-stream keepalive path installs the adapter first (it owns the
// earliest writes on the connection) and the handler reassigns its local
// ResponseWriter to the adapter, so every later write on that connection
// inherits the serialized channel. Wrapping the adapter again (interceptors,
// attempt gates) composes: they sit above the serialized channel and stay
// frame-atomic relative to every other producer.

// serializedResponseWriter is an http.ResponseWriter whose body writes are
// serialized through one SerializedStreamWriter.
type serializedResponseWriter struct {
	delegate http.ResponseWriter
	writer   *SerializedStreamWriter
}

// NewSerializedResponseWriter wraps w so all body writes and flushes are
// serialized. Headers and status still reach w directly; w must not be used
// for body writes by any other producer once wrapped.
func NewSerializedResponseWriter(w http.ResponseWriter) *serializedResponseWriter {
	return &serializedResponseWriter{delegate: w, writer: NewSerializedStreamWriter(w)}
}

// Header delegates to the wrapped ResponseWriter.
func (s *serializedResponseWriter) Header() http.Header { return s.delegate.Header() }

// WriteHeader delegates to the wrapped ResponseWriter.
func (s *serializedResponseWriter) WriteHeader(code int) { s.delegate.WriteHeader(code) }

// Write delegates to the shared serialized channel.
func (s *serializedResponseWriter) Write(p []byte) (int, error) { return s.writer.Write(p) }

// Flush flushes the shared serialized channel.
func (s *serializedResponseWriter) Flush() { s.writer.Flush() }

// FlushError preserves connection errors for callers that can act on them.
func (s *serializedResponseWriter) FlushError() error { return s.writer.FlushError() }

// SerializedWriter exposes the shared serialized channel so other producers
// (keepalive senders, attempt gates) can write without going through the
// http.ResponseWriter interface.
func (s *serializedResponseWriter) SerializedWriter() *SerializedStreamWriter {
	return s.writer
}
