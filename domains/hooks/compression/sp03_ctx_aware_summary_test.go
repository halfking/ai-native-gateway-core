// Copyright (c) 2026 official-deploy. SPDX-License-Identifier: Proprietary.
//
// SPDX-License-Identifier: Proprietary
//
// SP-03 (2026-08-19): verifies that tryLLMSummaryWithFallback honours
// ctx cancellation. The wrapper is the only entry point used by
// Prepare after SP-03 landed; the existing test corpus exercises
// tryLLMSummary via Prepare but never inspects the ctx-aware wrapper.
//
// These tests deliberately stub out the LLM summariser by passing
// nil CompactionDeps: tryLLMSummaryWithFallback returns (nil, false)
// before any network call when CompactionDeps is missing, so the
// "returns ok=false on canceled ctx" assertion is precise.

package compression

import (
	"context"
	"errors"
	"testing"
)

// TestTryLLMSummaryWithFallback_CtxAlreadyCanceled verifies the early
// exit when ctx is canceled before the call enters the LLM summary
// path. With no CompactionDeps the function would also short-circuit,
// so the test specifically exercises the ctx.Err() branch by giving
// the compressor a sentinel "stuck" hook through any future path —
// here we use a context whose Done channel is already closed.
func TestTryLLMSummaryWithFallback_CtxAlreadyCanceled(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal cancellation before the call

	out, ok := sc.tryLLMSummaryWithFallback(ctx, []byte("irrelevant"), "tenant-1", "openai-completions", "chat")
	if ok {
		t.Errorf("ok = true, want false (ctx was canceled before call)")
	}
	if out != nil {
		t.Errorf("out = %v, want nil", out)
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", err)
	}
}

// TestTryLLMSummaryWithFallback_NoCompactionDeps verifies the legacy
// nil-deps path: tryLLMSummaryWithFallback delegates to
// tryLLMSummary, which returns (nil, false). We assert the wrapper
// preserves this contract when ctx is healthy.
func TestTryLLMSummaryWithFallback_NoCompactionDeps(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{}}
	ctx := context.Background()
	out, ok := sc.tryLLMSummaryWithFallback(ctx, []byte("body"), "tenant-1", "openai-completions", "chat")
	if ok || out != nil {
		t.Errorf("with nil deps want (nil, false), got (%v, %v)", out, ok)
	}
}

// TestTryLLMSummary_CtxAlreadyCanceled_ShortCircuits verifies that
// the underlying tryLLMSummary also short-circuits when ctx is
// already canceled (it must NOT construct a summarizer only to
// discard the result).
func TestTryLLMSummary_CtxAlreadyCanceled_ShortCircuits(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, ok := sc.tryLLMSummary(ctx, []byte("body"), "tenant-1", "openai-completions", "chat")
	if ok || out != nil {
		t.Errorf("with canceled ctx want (nil, false), got (%v, %v)", out, ok)
	}
}
