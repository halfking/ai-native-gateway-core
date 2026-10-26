package sse

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// TestLineReaderSpansBufferBoundaries pins the 2026-10-16 fix: a physical
// SSE line at or beyond the underlying bufio window must accumulate across
// buffer boundaries instead of failing with "bufio: buffer full". The
// streaming read path feeds a 64 KiB bufio.Reader; any upstream frame with a
// larger single line (huge content delta, giant tool-call arguments) used to
// surface as a spurious read_error and kill the stream.
func TestLineReaderSpansBufferBoundaries(t *testing.T) {
	for _, size := range []int{1, 4095, 4096, 65535, 65536, 65537, 128 * 1024} {
		payload := strings.Repeat("x", size)
		line := `data: {"choices":[{"delta":{"content":"` + payload + `"}}]}` + "\n\n"
		reader := bufio.NewReaderSize(strings.NewReader(line), 64*1024)
		lr := NewLineReader(reader, 16<<20)

		got, err := lr.ReadLine()
		if err != nil {
			t.Fatalf("size=%d ReadLine: %v", size, err)
		}
		if want := len(line) - 1; len(got) != want {
			t.Fatalf("size=%d line length = %d, want %d (full physical line minus \\n)", size, len(got), want)
		}
		// The trailing blank line then EOF.
		if _, err := lr.ReadLine(); err != nil {
			t.Fatalf("size=%d blank line read: %v", size, err)
		}
		if _, err := lr.ReadLine(); err != io.EOF {
			t.Fatalf("size=%d want EOF after blank line, got %v", size, err)
		}
	}
}

// TestLineReaderStillRejectsOverLimitLines keeps the bounded-memory contract:
// a line beyond the configured limit must fail with LineTooLongError even
// though buffer-boundary accumulation now continues.
func TestLineReaderStillRejectsOverLimitLines(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	line := "data: " + payload + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(line+"\n"+line), 64)
	lr := NewLineReader(reader, 128)
	if _, err := lr.ReadLine(); err == nil {
		t.Fatal("over-limit line must fail")
	}
}
