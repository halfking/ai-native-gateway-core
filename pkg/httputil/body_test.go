package httputil

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
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

	t.Run("large body is limited", func(t *testing.T) {
		// 128KB body, should only drain 64KB
		largeBody := bytes.Repeat([]byte("x"), 128*1024)
		body := &closeTracker{Reader: bytes.NewReader(largeBody)}
		DrainAndClose(body)
		if !body.closed {
			t.Error("expected body to be closed")
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
