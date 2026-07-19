package streaming

import (
	"context"
	"fmt"
	"io"
	"time"
)

type anthropicSSEReadResult struct {
	eventType string
	data      []byte
	err       error
}

func readAnthropicSSEEventWithTimeout(ctx context.Context, reader io.Reader, closer io.Closer, timeout time.Duration) (string, []byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan anthropicSSEReadResult, 1)
	go func() {
		eventType, data, err := readAnthropicSSEEvent(readCtx, reader)
		resultCh <- anthropicSSEReadResult{eventType: eventType, data: data, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.eventType, result.data, result.err
	case <-readCtx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		<-resultCh
		if readCtx.Err() == context.DeadlineExceeded {
			return "", nil, fmt.Errorf("stream read timeout: %w", readCtx.Err())
		}
		return "", nil, readCtx.Err()
	}
}
