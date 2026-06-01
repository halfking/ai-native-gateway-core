package relay

import (
	"context"
	"io"
	"strings"
)

type streamReadState int

const (
	streamReadNext streamReadState = iota
	streamReadEOF
	streamReadCanceled
	streamReadTimeout
	streamReadFailed
)

func classifyStreamReadError(ctx context.Context, err error) streamReadState {
	if err == nil {
		return streamReadNext
	}
	if err == io.EOF || strings.Contains(err.Error(), "EOF") {
		return streamReadEOF
	}
	if ctx != nil && ctx.Err() == context.Canceled {
		return streamReadCanceled
	}
	if strings.Contains(err.Error(), "context canceled") {
		return streamReadCanceled
	}
	if strings.Contains(err.Error(), "timeout") {
		return streamReadTimeout
	}
	return streamReadFailed
}
