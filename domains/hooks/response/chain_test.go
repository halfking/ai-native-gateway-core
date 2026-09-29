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

// R24-C (round 26, corrected 2026-09-30): the suppress branch REPLACE, not
// clear. The original always-true guard (`!finalResult.SuppressChunk ||
// result.SuppressChunk`) was misjudged as dead logic: a suppressing
// interceptor that simultaneously RELEASES an earlier held frame carries
// that payload in the same result's ModifiedChunk — never the withheld
// current frame (production shape: outputcompliance/stream_compliance.go
// terminal release `{SuppressChunk:true, ModifiedChunk: prior+terminal}`),
// and interceptingStreamWriter.writeFrame writes exactly that payload.
// ee101fa68 cleared both fields unconditionally and dropped the release,
// emptying the wire for every governed stream (red: both cross-frame
// compliance integration tests in domains/streaming returned "").
//
// Three invariants pinned here:
//  1. same-result release survives (and stale earlier replacements do not);
//  2. a suppressing result without a release leaves the fields empty;
//  3. a stale replacement from an earlier interceptor is dropped once a
//     later interceptor suppresses without releasing.
func TestInterceptorChainSuppressResultCarriesOnlySameResultRelease(t *testing.T) {
	t.Run("same_result_release_survives", func(t *testing.T) {
		chain := NewInterceptorChain(
			chainTestInterceptor{chunk: func([]byte) *ChunkResult {
				return &ChunkResult{SuppressChunk: true, ModifiedChunk: []byte("released-prior")}
			}},
		)
		result, err := chain.InterceptStreamChunk(context.Background(), []byte("raw-frame"), &StreamMeta{})
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if result == nil || !result.SuppressChunk {
			t.Fatalf("result = %+v, want suppression", result)
		}
		if string(result.ModifiedChunk) != "released-prior" {
			t.Fatalf("same-result release dropped: modified=%q", result.ModifiedChunk)
		}
	})

	t.Run("suppress_without_release_keeps_fields_empty", func(t *testing.T) {
		chain := NewInterceptorChain(
			chainTestInterceptor{chunk: func([]byte) *ChunkResult {
				return &ChunkResult{SuppressChunk: true}
			}},
		)
		result, err := chain.InterceptStreamChunk(context.Background(), []byte("raw-frame"), &StreamMeta{})
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if result == nil || !result.SuppressChunk {
			t.Fatalf("result = %+v, want suppression", result)
		}
		if len(result.ModifiedChunk) > 0 || len(result.InjectAfter) > 0 {
			t.Fatalf("suppressed result leaked withheld content: modified=%q inject=%q",
				result.ModifiedChunk, result.InjectAfter)
		}
	})

	t.Run("stale_earlier_replacement_dropped_on_suppress", func(t *testing.T) {
		chain := NewInterceptorChain(
			chainTestInterceptor{chunk: func([]byte) *ChunkResult {
				return &ChunkResult{ModifiedChunk: []byte("stale-earlier")}
			}},
			chainTestInterceptor{chunk: func([]byte) *ChunkResult {
				return &ChunkResult{SuppressChunk: true}
			}},
		)
		result, err := chain.InterceptStreamChunk(context.Background(), []byte("raw-frame"), &StreamMeta{})
		if err != nil {
			t.Fatalf("InterceptStreamChunk: %v", err)
		}
		if result == nil || !result.SuppressChunk {
			t.Fatalf("result = %+v, want suppression", result)
		}
		if len(result.ModifiedChunk) > 0 || len(result.InjectAfter) > 0 {
			t.Fatalf("stale replacement survived suppression: modified=%q inject=%q",
				result.ModifiedChunk, result.InjectAfter)
		}
	})
}
