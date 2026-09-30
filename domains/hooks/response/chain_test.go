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
	// The release payload is produced by a frame-HOLDING interceptor: the only
	// production producer of {SuppressChunk:true, ModifiedChunk:prior+terminal}
	// is OutputComplianceInterceptor (stream_compliance.go:127), which also
	// implements StreamPendingFlusher (:159). Modelling the same shape with a
	// plain non-holder interceptor asserted a state the chain never sees, and
	// forced the implementation to forward withheld content from arbitrary
	// suppressors — the R24-C leak. The holder is used here so this test and
	// TestInterceptorChainSuppressResultCarriesNoReplacementWithoutHolder assert
	// complementary contracts on the same branch.
	t.Run("same_result_release_survives", func(t *testing.T) {
		chain := NewInterceptorChain(holdingTestInterceptor{
			chainTestInterceptor: chainTestInterceptor{chunk: func([]byte) *ChunkResult {
				return &ChunkResult{SuppressChunk: true, ModifiedChunk: []byte("released-prior")}
			}},
		})
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
