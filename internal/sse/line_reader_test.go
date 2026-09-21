package sse

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLineReaderFragmentsAndEOF(t *testing.T) {
	r := NewLineReader(strings.NewReader("a\r\nb\nfinal"), 16)
	for _, want := range []string{"a\r\n", "b\n", "final"} {
		got, err := r.ReadLine()
		if err != nil || got != want {
			t.Fatalf("ReadLine() = %q, %v; want %q, nil", got, err, want)
		}
	}
	if got, err := r.ReadLine(); got != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("terminal ReadLine() = %q, %v; want EOF", got, err)
	}
}

func TestLineReaderExactBoundaryAndOversize(t *testing.T) {
	if got, err := NewLineReader(strings.NewReader("1234\n"), 5).ReadLine(); got != "1234\n" || err != nil {
		t.Fatalf("exact boundary = %q, %v", got, err)
	}
	_, err := NewLineReader(strings.NewReader("12345\nnext\n"), 5).ReadLine()
	var tooLong *LineTooLongError
	if !errors.As(err, &tooLong) || tooLong.Limit != 5 || tooLong.Observed <= 5 || !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("oversize error = %T %v, want typed ErrLineTooLong", err, err)
	}
	if got, err := NewLineReader(strings.NewReader("12345\nnext\n"), 6).ReadLine(); got != "12345\n" || err != nil {
		t.Fatalf("terminator-inclusive boundary = %q, %v", got, err)
	}
}

type blockingReader struct{ unblock chan struct{} }

func (r *blockingReader) Read([]byte) (int, error) { <-r.unblock; return 0, io.EOF }

type closeSignal struct {
	closed chan struct{}
	body   *blockingReader
}

func (c *closeSignal) Close() error { close(c.closed); close(c.body.unblock); return nil }

func TestReadLineWithContextTimeoutCloses(t *testing.T) {
	body := &blockingReader{unblock: make(chan struct{})}
	closer := &closeSignal{closed: make(chan struct{}), body: body}
	r := NewLineReader(body, 16)
	started := time.Now()
	_, err := r.ReadLineWithContext(context.Background(), 10*time.Millisecond, closer)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("timeout error = %v", err)
	}
	select {
	case <-closer.closed:
	default:
		t.Fatal("closer was not closed")
	}
}
