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
