package executors

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestModelNotFoundError_UnwrapBareSurfaceUpstreamError guards the 2026-07-17
// P2 fix: modelNotFoundError now implements Unwrap() returning a
// *upstreampkg.Error with the captured status/body, so it participates in the
// typed error chain like its siblings (retryableError /
// contextLengthHTTPError / contextLengthExhaustedError).
//
// A bare modelNotFoundError (the shape returned by executor_anthropic.go:833
// and executor_chat.go:540) must be reachable via errors.As so consumers
// using the standard error chain (handler.extractUpstreamError, telemetry)
// can recover the upstream (Kind, StatusCode, Body) triple.
func TestModelNotFoundError_UnwrapBareSurfaceUpstreamError(t *testing.T) {
	mnf := &modelNotFoundError{
		credentialID: 42,
		rawModel:     "gpt-test",
		body:         `{"error":{"message":"model does not exist"}}`,
		status:       404,
	}

	var ue *upstreampkg.Error
	if !errors.As(mnf, &ue) {
		t.Fatalf("errors.As(modelNotFoundError) did not surface *upstreampkg.Error")
	}
	if ue.Kind != errorsx.KindModelNotFound {
		t.Errorf("Kind = %q, want %q", ue.Kind, errorsx.KindModelNotFound)
	}
	if ue.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", ue.StatusCode)
	}
	if string(ue.Body) != `{"error":{"message":"model does not exist"}}` {
		t.Errorf("Body = %q, want the upstream JSON body", string(ue.Body))
	}
}

// TestModelNotFoundError_UnwrapThroughRetryableWrapper covers the construction
// shape used by executor_chat.go:519, where the modelNotFoundError is wrapped
// in a retryableError to trigger a same-credential retry. The two-level chain
// (retryableError.Unwrap → modelNotFoundError.Unwrap → *upstreampkg.Error)
// must still let errors.As reach the upstream error so exhausted retries keep
// the upstream context on the terminal ExecuteError.
func TestModelNotFoundError_UnwrapThroughRetryableWrapper(t *testing.T) {
	wrapped := &retryableError{err: &modelNotFoundError{
		credentialID: 7,
		rawModel:     "claude-test",
		body:         "not found body",
		status:       404,
	}}

	var ue *upstreampkg.Error
	if !errors.As(wrapped, &ue) {
		t.Fatalf("errors.As(retryableError→modelNotFoundError) did not surface *upstreampkg.Error")
	}
	if ue.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", ue.StatusCode)
	}
	if ue.Kind != errorsx.KindModelNotFound {
		t.Errorf("Kind = %q, want %q", ue.Kind, errorsx.KindModelNotFound)
	}
}

// TestModelNotFoundError_UnwrapNilSafe ensures the nil receiver guard matches
// the sibling implementations (retryableError.Unwrap, contextLength*Error).
// A nil *modelNotFoundError must return nil rather than panic — the executor
// can hit this path via defensively-typed error variables.
func TestModelNotFoundError_UnwrapNilSafe(t *testing.T) {
	var mnf *modelNotFoundError
	if got := mnf.Unwrap(); got != nil {
		t.Errorf("nil modelNotFoundError.Unwrap() = %v, want nil", got)
	}
}
