package streaming

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// shortWriter returns fewer bytes than len(p) with nil error, violating
// io.Writer contract (guards 2026-08-28 audit fix).
type shortWriter struct {
	buf       bytes.Buffer
	shortLen  int
	callCount int
}

func (sw *shortWriter) Write(p []byte) (int, error) {
	sw.callCount++
	if len(p) > sw.shortLen {
		n, _ := sw.buf.Write(p[:sw.shortLen])
		return n, nil
	}
	return sw.buf.Write(p)
}

func (sw *shortWriter) Flush() {}

func TestSerializedStreamWriter_ShortWriteDetached(t *testing.T) {
	w := &shortWriter{shortLen: 5}
	s := NewSerializedStreamWriter(w)

	frame := "0123456789" // 10 bytes
	n, err := s.Write([]byte(frame))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Write returned err=%v, want io.ErrShortWrite", err)
	}
	if n != 5 {
		t.Errorf("Write returned n=%d, want 5 (short write amount)", n)
	}

	// Verify writer is detached after short write.
	n2, err2 := s.Write([]byte("ignored"))
	if err2 != nil {
		t.Errorf("second Write after detach returned err=%v, want nil (silent no-op)", err2)
	}
	if n2 != len("ignored") {
		t.Errorf("second Write returned n=%d, want %d (reported success)", n2, len("ignored"))
	}
	if w.callCount != 1 {
		t.Errorf("underlying writer called %d times, want 1 (detached after first short write)", w.callCount)
	}
}

func TestSerializedStreamWriter_ShortWriteWithError(t *testing.T) {
	w := &shortWriterWithError{shortLen: 3, writeErr: errors.New("network error")}
	s := NewSerializedStreamWriter(w)

	frame := "0123456789"
	n, err := s.Write([]byte(frame))
	if err == nil {
		t.Fatal("Write with error should return error, got nil")
	}
	if !errors.Is(err, w.writeErr) {
		t.Errorf("Write returned err=%v, want %v", err, w.writeErr)
	}
	if n != 3 {
		t.Errorf("Write returned n=%d, want 3", n)
	}

	// Second write should be silent no-op (detached).
	n2, err2 := s.Write([]byte("ignored"))
	if err2 != nil {
		t.Errorf("second Write returned err=%v, want nil", err2)
	}
	if n2 != len("ignored") {
		t.Errorf("second Write returned n=%d, want %d", n2, len("ignored"))
	}
}

type shortWriterWithError struct {
	buf       bytes.Buffer
	shortLen  int
	writeErr  error
	callCount int
}

func (sw *shortWriterWithError) Write(p []byte) (int, error) {
	sw.callCount++
	if len(p) > sw.shortLen {
		n, _ := sw.buf.Write(p[:sw.shortLen])
		return n, sw.writeErr
	}
	n, _ := sw.buf.Write(p)
	return n, sw.writeErr
}

func (sw *shortWriterWithError) Flush() {}
