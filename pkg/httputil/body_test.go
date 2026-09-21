package httputil

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type closeTracker struct {
	io.Reader
	closed   bool
	closeErr error
}

func (c *closeTracker) Close() error {
	c.closed = true
	return c.closeErr
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type trackingReader struct {
	data []byte
	read int
}

func (r *trackingReader) Read(p []byte) (int, error) {
	if r.read == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.read:])
	r.read += n
	return n, nil
}

func TestDrainAndClose(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		// Should not panic
		DrainAndClose(nil)
	})

	t.Run("small body", func(t *testing.T) {
		body := &closeTracker{Reader: strings.NewReader("hello world")}
		DrainAndClose(body)
		if !body.closed {
			t.Error("expected body to be closed")
		}
	})

	t.Run("large body is fully drained after prefix", func(t *testing.T) {
		largeBody := bytes.Repeat([]byte("x"), 128*1024)
		reader := &trackingReader{data: largeBody}
		body := &closeTracker{Reader: reader}
		DrainAndClose(body)
		if !body.closed {
			t.Error("expected body to be closed")
		}
		if reader.read != len(largeBody) {
			t.Fatalf("expected all body bytes drained, read %d of %d", reader.read, len(largeBody))
		}
	})

	t.Run("reports read and close errors", func(t *testing.T) {
		body := &closeTracker{Reader: errReader{}, closeErr: errors.New("close failed")}
		_, err := ReadPrefixAndDrain(body, 16)
		if err == nil || !strings.Contains(err.Error(), "read failed") || !strings.Contains(err.Error(), "close failed") {
			t.Fatalf("expected read and close errors, got %v", err)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		body := &closeTracker{Reader: strings.NewReader("")}
		DrainAndClose(body)
		if !body.closed {
			t.Error("expected body to be closed")
		}
	})
}

func TestDrainAndCloseUnlimited(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		// Should not panic
		DrainAndCloseUnlimited(nil)
	})

	t.Run("small body", func(t *testing.T) {
		body := &closeTracker{Reader: strings.NewReader("hello world")}
		DrainAndCloseUnlimited(body)
		if !body.closed {
			t.Error("expected body to be closed")
		}
	})

	t.Run("large body is fully drained", func(t *testing.T) {
		// 128KB body, should drain all of it
		largeBody := bytes.Repeat([]byte("x"), 128*1024)
		body := &closeTracker{Reader: bytes.NewReader(largeBody)}
		DrainAndCloseUnlimited(body)
		if !body.closed {
			t.Error("expected body to be closed")
		}
	})
}

// Benchmark the overhead of draining
func BenchmarkDrainAndClose(b *testing.B) {
	b.Run("small-1KB", func(b *testing.B) {
		body := bytes.Repeat([]byte("x"), 1024)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := &closeTracker{Reader: bytes.NewReader(body)}
			DrainAndClose(r)
		}
	})

	b.Run("medium-64KB", func(b *testing.B) {
		body := bytes.Repeat([]byte("x"), 64*1024)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := &closeTracker{Reader: bytes.NewReader(body)}
			DrainAndClose(r)
		}
	})

	b.Run("large-128KB-limited", func(b *testing.B) {
		body := bytes.Repeat([]byte("x"), 128*1024)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := &closeTracker{Reader: bytes.NewReader(body)}
			DrainAndClose(r)
		}
	})
}
