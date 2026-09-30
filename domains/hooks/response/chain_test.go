package response

import (
	"bytes"
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

// holdingTestInterceptor is a StreamPendingFlusher: it models the F02
// output-compliance interceptor that withholds frames across events and
// releases every checked frame at the terminal event as
// SuppressChunk(dropped raw frame) + ModifiedChunk(checked wire).
type holdingTestInterceptor struct {
	chainTestInterceptor
	release []byte
}

func (h holdingTestInterceptor) FlushStreamPending(context.Context, *StreamMeta) ([]byte, error) {
	return nil, nil
}

// F02: a frame-holding interceptor's terminal release must stay visible
// through the chain result. R24-C cleared ModifiedChunk on ANY suppressing
// result, which also destroyed this legitimate release payload — the client
// then received nothing for the whole withheld stream.
func TestInterceptorChainSuppressResultKeepsHolderReleaseVisible(t *testing.T) {
	chain := NewInterceptorChain(holdingTestInterceptor{
		chainTestInterceptor: chainTestInterceptor{chunk: func([]byte) *ChunkResult {
			return &ChunkResult{SuppressChunk: true, ModifiedChunk: []byte("event: done\ndata: {\"checked\":true}\n\n")}
		}},
		release: []byte("event: done\n"),
	})
	result, err := chain.InterceptStreamChunk(context.Background(), []byte("raw-terminal"), &StreamMeta{})
	if err != nil {
		t.Fatalf("InterceptStreamChunk: %v", err)
	}
	if result == nil || !result.SuppressChunk {
		t.Fatalf("result = %+v, want suppression", result)
	}
	if len(result.ModifiedChunk) == 0 || !bytes.Contains(result.ModifiedChunk, []byte("\"checked\":true")) {
		t.Fatalf("holder release lost: modified=%q", result.ModifiedChunk)
	}
}

// R24-C regression twin: the same result shape from an interceptor WITHOUT
// StreamPendingFlusher is the leak shape and must stay hidden.
func TestInterceptorChainSuppressResultCarriesNoReplacementWithoutHolder(t *testing.T) {
	chain := NewInterceptorChain(
		chainTestInterceptor{chunk: func([]byte) *ChunkResult {
			return &ChunkResult{
				SuppressChunk: true,
				ModifiedChunk: []byte("withheld-replacement"),
				InjectAfter:   []byte("withheld-injection"),
			}
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

// R24-C (round 26): a withholding result that also carries its own
// replacement must leave the chain result's ModifiedChunk/InjectAfter
// empty — the suppressed frame's content may not escape through the result
// even though the writer drops it anyway. The historical always-true guard
// (`!finalResult.SuppressChunk || result.SuppressChunk`) re-populated the
// fields right after the suppress branch cleared them.
func TestInterceptorChainSuppressResultCarriesNoReplacementOrInjection(t *testing.T) {
	chain := NewInterceptorChain(
		chainTestInterceptor{chunk: func([]byte) *ChunkResult {
			return &ChunkResult{
				SuppressChunk: true,
				ModifiedChunk: []byte("withheld-replacement"),
				InjectAfter:   []byte("withheld-injection"),
			}
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
}
