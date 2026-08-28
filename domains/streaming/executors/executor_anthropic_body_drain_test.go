package executors

import (
	"io"
	"strings"
	"testing"
)

// shortReader returns at most chunkSize bytes per Read, forcing the caller
// to loop (tests that readAndDrainErrorBody handles short reads correctly).
type shortReader struct {
	r         io.Reader
	chunkSize int
}

func (sr *shortReader) Read(p []byte) (int, error) {
	if len(p) > sr.chunkSize {
		p = p[:sr.chunkSize]
	}
	return sr.r.Read(p)
}

// closedTracker wraps an io.ReadCloser and records whether Close was called.
type closedTracker struct {
	io.Reader
	closed bool
}

func (ct *closedTracker) Close() error {
	ct.closed = true
	return nil
}

func TestReadAndDrainErrorBody_ShortReads(t *testing.T) {
	// 8 KiB body, but reader returns at most 1 KiB per Read.
	body := strings.Repeat("x", 8<<10)
	tracker := &closedTracker{Reader: &shortReader{r: strings.NewReader(body), chunkSize: 1024}}

	captured, err := readAndDrainErrorBody(tracker)
	if err != nil {
		t.Fatalf("readAndDrainErrorBody: %v", err)
	}
	if len(captured) != 8<<10 {
		t.Errorf("captured %d bytes, want 8192", len(captured))
	}
	if string(captured) != body {
		t.Error("captured body differs from original")
	}
	// Verify short reads don't leave unread tail.
	remaining, _ := io.ReadAll(tracker)
	if len(remaining) != 0 {
		t.Errorf("body not fully drained, %d bytes remain", len(remaining))
	}
}

func TestReadAndDrainErrorBody_ExceedsCap(t *testing.T) {
	// 128 KiB body, cap is 64 KiB.
	body := strings.Repeat("y", 128<<10)
	tracker := &closedTracker{Reader: strings.NewReader(body)}

	captured, err := readAndDrainErrorBody(tracker)
	if err != nil {
		t.Fatalf("readAndDrainErrorBody: %v", err)
	}
	if len(captured) != maxPassthroughErrorBody {
		t.Errorf("captured %d bytes, want %d", len(captured), maxPassthroughErrorBody)
	}
	if string(captured) != body[:maxPassthroughErrorBody] {
		t.Error("captured prefix differs from original")
	}
	// Verify the rest was drained.
	remaining, _ := io.ReadAll(tracker)
	if len(remaining) != 0 {
		t.Errorf("body not fully drained, %d bytes remain", len(remaining))
	}
}

func TestReadAndDrainErrorBody_NilBody(t *testing.T) {
	captured, err := readAndDrainErrorBody(nil)
	if err != nil {
		t.Fatalf("readAndDrainErrorBody(nil): %v", err)
	}
	if captured != nil {
		t.Errorf("captured = %v, want nil", captured)
	}
}

func TestReadAndDrainErrorBody_EmptyBody(t *testing.T) {
	tracker := &closedTracker{Reader: strings.NewReader("")}
	captured, err := readAndDrainErrorBody(tracker)
	if err != nil {
		t.Fatalf("readAndDrainErrorBody(empty): %v", err)
	}
	if len(captured) != 0 {
		t.Errorf("captured %d bytes from empty body, want 0", len(captured))
	}
}
