package response

import (
	"context"
	"testing"
)

type chainTestInterceptor struct {
	nonStream func(*InterceptRequest) *InterceptResult
	chunk     func([]byte) *ChunkResult
}

func (i chainTestInterceptor) InterceptNonStream(_ context.Context, req *InterceptRequest) (*InterceptResult, error) {
	if i.nonStream == nil {
		return nil, nil
	}
	return i.nonStream(req), nil
}

func (i chainTestInterceptor) InterceptStreamChunk(_ context.Context, chunk []byte, _ *StreamMeta) (*ChunkResult, error) {
	if i.chunk == nil {
		return nil, nil
	}
	return i.chunk(chunk), nil
}

func (chainTestInterceptor) InterceptStreamEnd(context.Context, *StreamMeta) (*EndResult, error) {
	return nil, nil
}

func TestInterceptorChainPropagatesFirstNonStreamModification(t *testing.T) {
	original := &InterceptRequest{ResponseBody: []byte("original"), GoalModeHeader: "managed"}
	chain := NewInterceptorChain(
		chainTestInterceptor{nonStream: func(req *InterceptRequest) *InterceptResult {
			if string(req.ResponseBody) != "original" {
				t.Fatalf("first input = %q", req.ResponseBody)
			}
			return &InterceptResult{ModifiedBody: []byte("redacted"), Metadata: map[string]interface{}{"first": true}}
		}},
		chainTestInterceptor{nonStream: func(req *InterceptRequest) *InterceptResult {
			if string(req.ResponseBody) != "redacted" || req.GoalModeHeader != "managed" {
				t.Fatalf("second input = %+v", req)
			}
			return &InterceptResult{ModifiedBody: []byte("restored"), Metadata: map[string]interface{}{"second": true}}
		}},
	)
	result, err := chain.InterceptNonStream(context.Background(), original)
	if err != nil || result == nil || string(result.ModifiedBody) != "restored" ||
		result.Metadata["first"] != true || result.Metadata["second"] != true {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if string(original.ResponseBody) != "original" {
		t.Fatalf("caller request mutated: %q", original.ResponseBody)
	}
}

func TestInterceptorChainPropagatesFirstStreamModification(t *testing.T) {
	chain := NewInterceptorChain(
		chainTestInterceptor{chunk: func(chunk []byte) *ChunkResult {
			return &ChunkResult{ModifiedChunk: append(chunk, []byte("-safe")...)}
		}},
		chainTestInterceptor{chunk: func(chunk []byte) *ChunkResult {
			if string(chunk) != "raw-safe" {
				t.Fatalf("second chunk = %q", chunk)
			}
			return &ChunkResult{ModifiedChunk: []byte("final")}
		}},
	)
	result, err := chain.InterceptStreamChunk(context.Background(), []byte("raw"), &StreamMeta{})
	if err != nil || result == nil || string(result.ModifiedChunk) != "final" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
