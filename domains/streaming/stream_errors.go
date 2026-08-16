package streaming

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/kaixuan/llm-gateway-go/errorsx"
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
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return streamReadFailed
	}
	if errors.Is(err, io.EOF) {
		return streamReadEOF
	}
	if ctx != nil && ctx.Err() == context.Canceled {
		return streamReadCanceled
	}
	if strings.Contains(strings.ToLower(err.Error()), "context canceled") {
		return streamReadCanceled
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return streamReadTimeout
	}
	return streamReadFailed
}

func streamReadFailureOutcome(err error, chunkCount int) StreamOutcome {
	kind := streamReadFailureKind(err)
	reason := "read_error"
	if kind == errorsx.KindNetwork {
		reason = "network_error"
	}
	return StreamOutcome{
		Interrupted: true,
		Reason:      reason,
		Kind:        kind,
		Resumable:   true,
		ChunkCount:  chunkCount,
	}
}

func streamReadFailureKind(err error) errorsx.ErrorKind {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return errorsx.KindNetwork
	}
	if errorsx.ClassifyError(err, nil) == errorsx.KindNetwork {
		return errorsx.KindNetwork
	}
	return errorsx.KindUpstreamDown
}
