package streaming

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestWrapAttemptWriterReusesPreGatedWriterBehindInterceptor reproduces the
// production bug observed on 2026-09-23: when StreamResponsesSSE wraps a
// coordinator-gated writer in interceptingStreamWriter before calling
// wrapAttemptWriter, the unwrap loop fails to see through the interceptor
// (it doesn't implement Unwrap), the bridge stacks a SECOND gate, and the
// §11.6 TerminalRendered latch lands on the throwaway gate while
// SurvivalCoordinator.renderTerminal consults its own — two terminal frames.
func TestWrapAttemptWriterReusesPreGatedWriterBehindInterceptor(t *testing.T) {
	restore := setAttemptGateForTest(true, GateModeBuffered)
	defer restore()

	inner := httptest.NewRecorder()
	sw := NewSerializedStreamWriter(inner)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIResponses, sw, GateOptions{Mode: GateModeBuffered})
	gw := NewGateWriterWithResponse(gate, nil)

	// interceptingStreamWriter wraps gw without implementing Unwrap.
	interceptor := &interceptingStreamWriter{w: gw, ctx: context.Background()}

	w2, gate2 := wrapAttemptWriter(context.Background(), interceptor, ProtocolOpenAIResponses)
	if gate2 != gate {
		t.Fatalf("bridge must reuse the coordinator's gate through the interceptor wrapper, not create a second one (got different *AttemptCommitGate)")
	}
	_ = w2
}
