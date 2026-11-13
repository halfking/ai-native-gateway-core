package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestDispatchErrToExecuteError_QuotaPermanent validates the full dispatch
// error conversion path: upstream.Error → dispatchErrToExecuteError → ExecuteError.LastKind
// This integration test ensures KindQuotaPermanent survives the conversion.
func TestDispatchErrToExecuteError_QuotaPermanent(t *testing.T) {
	upstreamErr := &upstreampkg.Error{
		Kind:       errorsx.KindQuotaPermanent,
		StatusCode: 400,
		Body:       []byte(`{"error":{"message":"no active limited quota"}}`),
	}

	execErr := dispatchErrToExecuteError(upstreamErr)

	if execErr == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if execErr.LastKind != errorsx.KindQuotaPermanent {
		t.Errorf("LastKind = %v, want %v (upstream kind was lost)",
			execErr.LastKind, errorsx.KindQuotaPermanent)
	}
	if !execErr.Exhausted {
		t.Error("Exhausted = false, want true for credential-fatal error")
	}
}
