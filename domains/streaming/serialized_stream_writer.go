package streaming

import (
	"errors"
	"net/http"
	"sync"
)

// SR-W1 (doc 18 §9.3 write concurrency): once an attempt is committed,
// keepalive, status frames and the protocol bridge must share ONE serialized
// write channel. SerializedStreamWriter is that channel.
//
// Guarantees:
//   - every Write/Flush is serialized under one mutex;
//   - a write error (or panic, e.g. write to a hijacked/closed conn) latches
//     the connection detached — after that the writer never touches the
//     underlying ResponseWriter again (doc 18 §9.3 "写失败标记连接 detached");
//   - callers never observe a panic from the underlying writer.
type SerializedStreamWriter struct {
	writer  http.ResponseWriter
	flusher http.Flusher

	mu       sync.Mutex
	detached bool
}

// ErrStreamWriterDetached is returned by Write after the connection detached
// (write failure, write panic, or explicit Detach).
var ErrStreamWriterDetached = errors.New("serialized_stream_writer: connection detached")

// NewSerializedStreamWriter builds the single serialized write channel over
// the real client connection. f may be nil; it is then derived from w.
func NewSerializedStreamWriter(w http.ResponseWriter, f http.Flusher) *SerializedStreamWriter {
	if f == nil {
		if fl, ok := w.(http.Flusher); ok {
			f = fl
		}
	}
	return &SerializedStreamWriter{writer: w, flusher: f}
}

// Write implements io.Writer. After detach it returns ErrStreamWriterDetached
// without touching the underlying writer.
func (s *SerializedStreamWriter) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached {
		return 0, ErrStreamWriterDetached
	}
	defer func() {
		if r := recover(); r != nil {
			s.detached = true
			n, err = 0, ErrStreamWriterDetached
		}
	}()
	n, err = s.writer.Write(p)
	if err != nil {
		s.detached = true
		return n, ErrStreamWriterDetached
	}
	return n, nil
}

// Flush flushes the underlying flusher. After detach it is a no-op.
func (s *SerializedStreamWriter) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached {
		return
	}
	if s.flusher == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.detached = true
		}
	}()
	s.flusher.Flush()
}

// Detach latches the connection as gone (e.g. client disconnect observed by
// the keepalive loop). Idempotent.
func (s *SerializedStreamWriter) Detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = true
}

// Detached reports whether the connection is latched as gone.
func (s *SerializedStreamWriter) Detached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}
