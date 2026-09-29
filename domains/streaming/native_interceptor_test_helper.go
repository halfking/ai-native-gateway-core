package streaming

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// nativeNonStreamInterceptor is a minimal test double for
// ResponseInterceptor used across native non-stream coverage tests. Each
// field maps to a different control decision so individual tests can
// compose behaviour (block, modify body, or return an error from
// InterceptNonStream). Zero value is compliant passthrough.
//
// seen / seenMeta are populated with the most recent *StreamMeta observed
// on InterceptStreamChunk so tests can inspect CallerOwner / SessionID
// propagated by the writer.
type nativeNonStreamInterceptor struct {
	blocked   bool
	modified  []byte
	err       error
	seen      *response.StreamMeta // most recent StreamMeta passed to InterceptStreamChunk
	seenCalls int
}

func (i *nativeNonStreamInterceptor) InterceptNonStream(_ context.Context, _ *response.InterceptRequest) (*response.InterceptResult, error) {
	if i.err != nil {
		return nil, i.err
	}
	if i.blocked {
		return &response.InterceptResult{ShouldBlock: true}, nil
	}
	if i.modified != nil {
		return &response.InterceptResult{ModifiedBody: i.modified}, nil
	}
	return nil, nil
}

func (i *nativeNonStreamInterceptor) InterceptStreamChunk(_ context.Context, _ []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	i.seenCalls++
	i.seen = meta
	if i.blocked {
		return &response.ChunkResult{ShouldBlock: true}, nil
	}
	return nil, nil
}

func (i *nativeNonStreamInterceptor) InterceptStreamEnd(_ context.Context, _ *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}