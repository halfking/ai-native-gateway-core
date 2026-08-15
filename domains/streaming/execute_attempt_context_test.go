package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

func TestExecuteAttemptBindsContextWithoutMutatingCallerRequest(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeAttemptExecutor{result: &executors.ExecuteResult{}}
	params := &executors.ExecParams{R: original}

	res := ExecuteAttempt(ctx, fake, newAttemptGateForTest(), params)
	if !res.Success {
		t.Fatalf("expected success: %v", res.FinalError)
	}
	if fake.lastR == nil || fake.lastR.Context() != ctx {
		t.Fatal("executor must receive the coordinator context")
	}
	if params.R != original {
		t.Fatal("ExecuteAttempt must not mutate caller request")
	}
}
