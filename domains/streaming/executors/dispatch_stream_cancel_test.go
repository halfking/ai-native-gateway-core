package executors

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestDispatchCanceledErrorPreservesAttemptCount(t *testing.T) {
	qr := dispatch.NewQueuedRequest("req", "tenant", "glm-5.2", context.Background(), nil)
	qr.AttemptCount = 1

	ee := dispatchErrToExecuteError(context.Canceled)
	copyDispatchAttemptMetadata(ee, qr)

	if ee.Exhausted {
		t.Fatal("client cancellation must not be reported as candidate exhaustion")
	}
	if ee.LastKind != errorsx.KindCanceled {
		t.Fatalf("LastKind = %q, want %q", ee.LastKind, errorsx.KindCanceled)
	}
	if ee.Tried != 1 {
		t.Fatalf("Tried = %d, want 1", ee.Tried)
	}
	if got := ee.Error(); got == "all 0 candidates failed: context canceled" {
		t.Fatalf("misleading dispatch error retained: %q", got)
	}
}

func TestDispatchExecutionContextDetachesStreamingClientCancel(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(clientCtx)
	ctx, cancel := dispatchExecutionContext(&ExecParams{R: req, IsStream: true})
	defer cancel()

	cancelClient()
	select {
	case <-ctx.Done():
		t.Fatalf("streaming dispatch context canceled with client: %v", ctx.Err())
	default:
	}
}

func TestDispatchExecutionContextKeepsNonStreamingCancel(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(clientCtx)
	ctx, cancel := dispatchExecutionContext(&ExecParams{R: req})
	defer cancel()

	cancelClient()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("non-streaming dispatch context error = %v, want context.Canceled", ctx.Err())
	}
}

func TestIsClientStreamInterruption(t *testing.T) {
	tests := []struct {
		name   string
		kind   errorsx.ErrorKind
		reason string
		want   bool
	}{
		{name: "structured canceled", kind: errorsx.KindCanceled, reason: "future_client_reason", want: true},
		{name: "legacy cancel", reason: "client_cancel", want: true},
		{name: "write failed", reason: "client_write_failed", want: true},
		{name: "fully captured disconnect", reason: "client_disconnected", want: true},
		{name: "network error", kind: errorsx.KindNetwork, reason: "network_error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClientStreamInterruption(tc.kind, tc.reason); got != tc.want {
				t.Fatalf("isClientStreamInterruption(%q, %q) = %v, want %v", tc.kind, tc.reason, got, tc.want)
			}
		})
	}
}

func TestMayRetryInterruptedStreamRejectsSurvivalOwner(t *testing.T) {
	sie := &streamInterruptedError{resumable: true, reason: "stream_timeout"}
	if mayRetryInterruptedStream(&ExecParams{SurvivalAttempt: true}, sie) {
		t.Fatal("survival attempt must return interruption to coordinator instead of switching candidates internally")
	}
}

func TestMayRetryInterruptedStreamRejectsCommittedNetworkOutput(t *testing.T) {
	capture := audit.NewStreamCapture()
	capture.RecordChunkSent()
	sie := &streamInterruptedError{resumable: true, reason: "network_error", kind: errorsx.KindNetwork}
	if mayRetryInterruptedStream(&ExecParams{Capture: capture}, sie) {
		t.Fatal("committed network interruption must not switch candidates")
	}
}

func TestMayRetryInterruptedStreamAllowsPreCommitNetworkFailure(t *testing.T) {
	sie := &streamInterruptedError{resumable: true, reason: "network_error", kind: errorsx.KindNetwork}
	if !mayRetryInterruptedStream(&ExecParams{}, sie) {
		t.Fatal("pre-commit network interruption should allow candidate failover")
	}
}

func TestMayRetryInterruptedStreamRejectsCommittedOutput(t *testing.T) {
	capture := audit.NewStreamCapture()
	capture.RecordChunkSent()
	sie := &streamInterruptedError{resumable: true, reason: "stream_timeout"}
	if mayRetryInterruptedStream(&ExecParams{Capture: capture}, sie) {
		t.Fatal("committed assistant output must not be followed by internal candidate retry")
	}
}

func TestMayRetryInterruptedStreamAllowsPreCommitResumable(t *testing.T) {
	sie := &streamInterruptedError{resumable: true, reason: "first_byte_timeout"}
	if !mayRetryInterruptedStream(&ExecParams{}, sie) {
		t.Fatal("pre-commit resumable interruption should still allow candidate failover")
	}
}

func TestDispatchExecutionContext_StreamDetaches(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	clientCtx, cancelClient := context.WithCancel(r.Context())
	defer cancelClient()

	got, cancelDispatch := dispatchExecutionContext(&ExecParams{R: r.WithContext(clientCtx), IsStream: true})
	defer cancelDispatch()

	cancelClient()
	if errors.Is(got.Err(), context.Canceled) {
		t.Fatal("streaming dispatch wait must remain alive after client disconnect")
	}
}

func TestDispatchExecutionContext_NonStreamDoesNotDetach(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	clientCtx, cancelClient := context.WithCancel(r.Context())
	defer cancelClient()

	// non-stream request with a session id - must still inherit client ctx so
	// the client disconnect is observable and downstream cancel propagates.
	got, cancelDispatch := dispatchExecutionContext(&ExecParams{R: r.WithContext(clientCtx), IsStream: false, SessionID: "sess-1"})
	defer cancelDispatch()

	cancelClient()
	if !errors.Is(got.Err(), context.Canceled) {
		t.Fatalf("non-stream dispatch wait should inherit client cancel; got %v", got.Err())
	}
}
