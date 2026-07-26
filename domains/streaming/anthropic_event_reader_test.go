package streaming

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadAnthropicSSEEventRaw_PreservesOriginalFrame(t *testing.T) {
	rawFrame := "event: content_block_delta\r\n: upstream heartbeat\r\ndata: first\r\ndata: second\r\n\r\n"

	eventType, data, raw, err := readAnthropicSSEEventRaw(context.Background(), strings.NewReader(rawFrame))

	require.NoError(t, err)
	require.Equal(t, "content_block_delta", eventType)
	require.Equal(t, []byte("first\nsecond"), data)
	require.Equal(t, []byte(rawFrame), raw)
}
