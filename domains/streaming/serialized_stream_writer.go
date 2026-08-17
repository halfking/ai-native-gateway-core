package streaming

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// serialized_stream_writer.go — SR-W1 (doc 18 §9.3)
//
// SerializedStreamWriter is the single serialized write channel shared by the
// keepalive renderer, status frames and the protocol bridge once an attempt
// has been committed. All writes funnel through one mutex so no two producers
// can ever interleave a frame on the client connection.
//
// A write failure detaches the writer: the connection is considered gone and
// every later write (including keepalives) becomes a silent no-op instead of
// touching a dead ResponseWriter.

type flusher interface{ Flush() }
type errorFlusher interface{ FlushError() error }

// SerializedStreamWriter serializes all client-facing stream writes for one
// HTTP connection.
type SerializedStreamWriter struct {
	mu              sync.Mutex
	w               io.Writer
	f               flusher
	fe              errorFlusher
	detached        bool
	detachErr       error
	capture         []byte
	captureLimit    int
	captureOverflow bool
}

// NewSerializedStreamWriter wraps the client connection. w must not be used
// directly by any other producer once wrapped.
func NewSerializedStreamWriter(w io.Writer) *SerializedStreamWriter {
	s := &SerializedStreamWriter{w: w}
	if f, ok := w.(flusher); ok {
		s.f = f
	}
	if fe, ok := w.(errorFlusher); ok {
		s.fe = fe
	}
	return s
}

// EnableCapture retains at most limit client-facing bytes for durable result persistence.
func (s *SerializedStreamWriter) EnableCapture(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > 0 {
		s.captureLimit = limit
	}
}

// Captured returns a copy of retained bytes or an error if the configured cap was exceeded.
func (s *SerializedStreamWriter) Captured() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captureOverflow {
		return nil, errors.New("serialized stream capture exceeded limit")
	}
	return append([]byte(nil), s.capture...), nil
}

// Write appends p to the client connection under the serialization lock.
// Once detached it reports success without writing, so callers do not need
// per-site detached checks.
func (s *SerializedStreamWriter) Write(p []byte) (int, error) {
	return s.write(p, true)
}

// WriteTransportFrame writes a transport-only frame, such as an SSE comment,
// through the same serialized connection without adding it to semantic wire
// capture. Heartbeats must not change durable result bytes or chunk accounting.
func (s *SerializedStreamWriter) WriteTransportFrame(p []byte) (int, error) {
	return s.write(p, false)
}

func (s *SerializedStreamWriter) write(p []byte, capture bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if capture && s.captureLimit > 0 && !s.captureOverflow {
		if len(s.capture)+len(p) > s.captureLimit {
			s.captureOverflow = true
		} else {
			s.capture = append(s.capture, p...)
		}
	}
	if s.detached {
		return len(p), nil
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.detached = true
		s.detachErr = err
		return n, err
	}
	return n, nil
}

// Flush flushes the underlying connection, serialized with writes. It is a
// no-op when detached or when the writer has no flusher.
func (s *SerializedStreamWriter) Flush() {
	_ = s.FlushError()
}

// FlushError preserves optional net/http flush errors so a closed connection
// detaches immediately instead of being touched by later stream producers.
func (s *SerializedStreamWriter) FlushError() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.detached = true
			s.detachErr = fmt.Errorf("flush panic: %v", recovered)
			err = s.detachErr
		}
	}()
	if s.detached {
		return s.detachErr
	}
	if s.fe != nil {
		if err := s.fe.FlushError(); err != nil {
			s.detached = true
			s.detachErr = err
			return err
		}
		return nil
	}
	if s.f == nil {
		return nil
	}
	s.f.Flush()
	return nil
}

// Detached reports whether the client connection is gone.
func (s *SerializedStreamWriter) Detached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}

// Detach marks the connection gone without a write error (e.g. request
// context canceled). Idempotent.
func (s *SerializedStreamWriter) Detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = true
}
