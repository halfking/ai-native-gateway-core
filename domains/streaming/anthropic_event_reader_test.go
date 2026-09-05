package streaming

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadAnthropicSSEEventWithTimeoutReturnsWhenReaderCannotBeClosed(t *testing.T) {
	reader := &blockingReader{release: make(chan struct{})}
	start := time.Now()

	_, _, _, err := readAnthropicSSEEventWithTimeoutRaw(context.Background(), reader, nil, 20*time.Millisecond)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	close(reader.release)
}

type blockingReader struct {
	release chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func TestReadAnthropicSSEEventRaw_PreservesOriginalFrame(t *testing.T) {
	rawFrame := "event: content_block_delta\r\n: upstream heartbeat\r\ndata: first\r\ndata: second\r\n\r\n"

	eventType, data, raw, err := readAnthropicSSEEventRaw(context.Background(), strings.NewReader(rawFrame))

	require.NoError(t, err)
	require.Equal(t, "content_block_delta", eventType)
	require.Equal(t, []byte("first\nsecond"), data)
	require.Equal(t, []byte(rawFrame), raw)
}

// anthropicPanickingReader returns a synthetic panic on the first Read so we can
// exercise the goroutine panic-recover guard added in anthropic_event_reader.go
// (audit-2026-08-29 §5.2). Without recover() the reader goroutine dies, the
// channel send is skipped, and the caller blocks until the chunk timeout fires
// — turning a recoverable panic into a full stream timeout for the client.
type anthropicPanickingReader struct{}

func (anthropicPanickingReader) Read([]byte) (int, error) {
	panic("synthetic panic inside anthropic SSE reader")
}

func TestReadAnthropicSSEEventPanicBecomesError(t *testing.T) {
	// Use a tight timeout so that, even if the panic-recover is missing and
	// the goroutine silently dies, the test fails fast with a clear timeout
	// signal rather than the 30s default.
	start := time.Now()
	_, _, _, err := readAnthropicSSEEventWithTimeoutRaw(context.Background(), anthropicPanickingReader{}, nil, 500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("readAnthropicSSEEventWithTimeoutRaw after panic = nil error, want recovered panic error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error = %v, want recovered panic", err)
	}
	// Sanity check: recovery must surface promptly, not wait for ctx timeout.
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("blocked %v after panic; recover() likely missing", elapsed)
	}
}
