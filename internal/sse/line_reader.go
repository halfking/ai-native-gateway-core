// Package sse provides bounded readers for server-sent event wire data.
package sse

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrLineTooLong is returned when a physical SSE line exceeds the configured
// byte limit.
var ErrLineTooLong = errors.New("sse physical line exceeds configured byte limit")

// LineTooLongError describes a physical line that exceeded its byte limit.
type LineTooLongError struct {
	Limit    int
	Observed int
}

func (e *LineTooLongError) Error() string {
	return fmt.Sprintf("%s: %d bytes (limit %d)", ErrLineTooLong.Error(), e.Observed, e.Limit)
}

func (e *LineTooLongError) Unwrap() error { return ErrLineTooLong }

// LineReader reads complete physical SSE lines with bounded accumulation. The
// limit includes the physical line terminator when one is present.
type LineReader struct {
	reader *bufio.Reader
	limit  int
}

// NewLineReader creates a bounded physical-line reader. A non-positive limit
// makes every read fail closed with LineTooLongError.
func NewLineReader(reader io.Reader, limit int) *LineReader {
	if br, ok := reader.(*bufio.Reader); ok {
		return &LineReader{reader: br, limit: limit}
	}
	return &LineReader{reader: bufio.NewReader(reader), limit: limit}
}

// ReadLine returns one physical line. An unterminated final line is returned
// with a nil error; the next call returns io.EOF.
func (r *LineReader) ReadLine() (string, error) {
	if r == nil || r.reader == nil {
		return "", io.EOF
	}
	if r.limit <= 0 {
		return "", &LineTooLongError{Limit: r.limit, Observed: 1}
	}

	line := make([]byte, 0, minInt(r.limit, 4096))
	for {
		fragment, err := r.reader.ReadSlice('\n')
		if len(fragment) > r.limit-len(line) {
			size := len(line) + len(fragment)
			// Discard the remainder of this physical line. This leaves the
			// reader positioned at the next line and bounds memory usage even
			// when the upstream sends an effectively infinite line.
			for !hasLF(fragment) && (err == bufio.ErrBufferFull || err == nil) {
				fragment, err = r.reader.ReadSlice('\n')
				size += len(fragment)
			}
			if size <= r.limit {
				size = r.limit + 1
			}
			return "", &LineTooLongError{Limit: r.limit, Observed: size}
		}
		line = append(line, fragment...)
		if hasLF(fragment) {
			return string(line), nil
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return string(line), nil
			}
			return string(line), err
		}
	}
}

// ReadLineWithContext reads one physical line while enforcing timeout and
// cancellation. On cancellation or timeout, closer is closed when non-nil to
// unblock a blocked underlying network read. The closer may be nil for tests
// or readers that already honor context cancellation.
func (r *LineReader) ReadLineWithContext(ctx context.Context, timeout time.Duration, closer io.Closer) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	readCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		readCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	type result struct {
		line string
		err  error
	}
	results := make(chan result, 1)
	go func() {
		line, err := r.ReadLine()
		results <- result{line: line, err: err}
	}()

	select {
	case got := <-results:
		return got.line, got.err
	case <-readCtx.Done():
		if closer != nil {
			_ = closer.Close()
			// A buffered channel lets the reader goroutine finish without
			// blocking. Drain when a closer was supplied so its lifecycle is
			// complete before returning.
			<-results
		}
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("sse read timeout: %w", readCtx.Err())
		}
		return "", readCtx.Err()
	}
}

func hasLF(b []byte) bool { return len(b) > 0 && b[len(b)-1] == '\n' }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
