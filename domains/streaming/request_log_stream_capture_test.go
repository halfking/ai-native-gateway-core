package streaming

import (
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// TestRequestLogContext_StreamCountersMatchCapture covers
// 2026-07-28 §5.5: the request_logs row's stream_chunks_sent /
// stream_chunk_errors must match audit.StreamCapture after
// MarkDone. Concurrent RecordChunkSent / RecordChunkError from
// many goroutines must aggregate correctly without atomics
// drift.
func TestRequestLogContext_StreamCountersMatchCapture(t *testing.T) {
	cap := audit.NewStreamCapture()
	logCtx := &RequestLogContext{StreamCapture: cap}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 7; j++ {
				cap.RecordChunkSent()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 2; j++ {
				cap.RecordChunkError()
			}
		}()
	}
	wg.Wait()
	cap.MarkDone()

	sent, errs := streamCountersFromContext(logCtx)
	if sent != 35 {
		t.Errorf("sent=%d want 35", sent)
	}
	if errs != 10 {
		t.Errorf("errs=%d want 10", errs)
	}
	// The atomics on the log context must also be refreshed so
	// legacy readers see the same values.
	if logCtx.StreamChunksSentValue() != 35 {
		t.Errorf("atomics sent=%d want 35", logCtx.StreamChunksSentValue())
	}
	if logCtx.StreamChunkErrorsValue() != 10 {
		t.Errorf("atomics errs=%d want 10", logCtx.StreamChunkErrorsValue())
	}
}

// TestRequestLogContext_StreamCountersFallback covers the legacy
// path where no StreamCapture is attached.
func TestRequestLogContext_StreamCountersFallback(t *testing.T) {
	logCtx := &RequestLogContext{}
	logCtx.SetStreamChunkCounters(4, 17)
	sent, errs := streamCountersFromContext(logCtx)
	if sent != 17 || errs != 4 {
		t.Errorf("sent=%d errs=%d want 17/4", sent, errs)
	}
}

// TestRequestLogContext_StreamCountersNil covers the defensive
// guarantee for callers that pass a nil logCtx.
func TestRequestLogContext_StreamCountersNil(t *testing.T) {
	sent, errs := streamCountersFromContext(nil)
	if sent != 0 || errs != 0 {
		t.Errorf("sent=%d errs=%d want 0/0", sent, errs)
	}
}