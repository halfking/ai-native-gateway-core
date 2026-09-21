package streaming

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/sse"
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

	// Audit-2026-08-29 (§5.2 hardening, peer to native_responses_stream.go:57):
	// if readAnthropicSSEEventRaw panics, the channel send is skipped and the
	// reader goroutine dies. The caller in this function blocks on the select
	// until readCtx times out, leaving StreamAnthropicSSEToOpenAI hung for the
	// entire stream chunk timeout. LineReader + strings.Builder are known safe
	// today, but contract surface includes attacker-controlled SSE bytes — guard
	// the goroutine so a panic becomes an error, never a silent reader death.
	resultCh := make(chan anthropicSSEReadResult, 1)
	go func() {
		eventType, data, raw, err := func() (eventType string, data, raw []byte, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("anthropic SSE read panic: %v", r)
				}
			}()
			return readAnthropicSSEEventRaw(readCtx, reader)
		}()
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
	lineReader := sse.NewLineReader(reader, currentStreamRuntimeConfig().sseMaxLineBytes)

	var dataLines []string
	var rawEvent strings.Builder
	for {
		select {
		case <-ctx.Done():
			return eventType, nil, []byte(rawEvent.String()), ctx.Err()
		default:
		}

		line, rerr := lineReader.ReadLine()
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
