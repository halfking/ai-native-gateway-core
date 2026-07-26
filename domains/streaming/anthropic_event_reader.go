package streaming

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

type anthropicSSEReadResult struct {
	eventType string
	data      []byte
	raw       []byte
	err       error
}

func readAnthropicSSEEventWithTimeout(ctx context.Context, reader io.Reader, closer io.Closer, timeout time.Duration) (string, []byte, error) {
	eventType, data, _, err := readAnthropicSSEEventWithTimeoutRaw(ctx, reader, closer, timeout)
	return eventType, data, err
}

func readAnthropicSSEEventWithTimeoutRaw(ctx context.Context, reader io.Reader, closer io.Closer, timeout time.Duration) (string, []byte, []byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan anthropicSSEReadResult, 1)
	go func() {
		eventType, data, raw, err := readAnthropicSSEEventRaw(readCtx, reader)
		resultCh <- anthropicSSEReadResult{eventType: eventType, data: data, raw: raw, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.eventType, result.data, result.raw, result.err
	case <-readCtx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		if readCtx.Err() == context.DeadlineExceeded {
			return "", nil, nil, fmt.Errorf("stream read timeout: %w", readCtx.Err())
		}
		return "", nil, nil, readCtx.Err()
	}
}

func readAnthropicSSEEventRaw(ctx context.Context, reader io.Reader) (eventType string, data, raw []byte, err error) {
	br, ok := reader.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(reader)
	}

	var dataLines []string
	var rawEvent strings.Builder
	for {
		select {
		case <-ctx.Done():
			return eventType, nil, []byte(rawEvent.String()), ctx.Err()
		default:
		}

		line, rerr := br.ReadString('\n')
		rawEvent.WriteString(line)
		trimmedLine := strings.TrimRight(line, "\r\n")
		if trimmedLine == "" {
			if len(dataLines) == 0 {
				if rerr != nil {
					return eventType, nil, []byte(rawEvent.String()), rerr
				}
				continue
			}
			return eventType, []byte(strings.Join(dataLines, "\n")), []byte(rawEvent.String()), nil
		}

		switch {
		case strings.HasPrefix(trimmedLine, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "event:"))
		case strings.HasPrefix(trimmedLine, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:")))
		case strings.HasPrefix(trimmedLine, ":"): //nolint:staticcheck // matches SSE comment lines (heartbeat)
		}

		if rerr != nil {
			if len(dataLines) > 0 {
				return eventType, []byte(strings.Join(dataLines, "\n")), []byte(rawEvent.String()), nil
			}
			return eventType, nil, []byte(rawEvent.String()), io.EOF
		}
	}
}
