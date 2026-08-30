package routing

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

func TestCandidateFailureRowRecoveryProjection(t *testing.T) {
	w := &CandidateFailureWriter{}
	for _, kind := range []errorsx.ErrorKind{errorsx.KindEmptyResponse, errorsx.KindNoAvailableChannel} {
		row := w.buildRow("req", "tenant", 1, 2, "model", 0,
			&upstreampkg.Error{Kind: kind, Message: "failure"}, nil, nil,
			map[string]any{"source": "test"})
		if row.Retryable == nil || *row.Retryable {
			t.Fatalf("%s retryable = %v, want false", kind, row.Retryable)
		}
		if row.Context["generic_retryable"] != false || row.Context["candidate_failover"] != true {
			t.Fatalf("%s recovery context = %#v", kind, row.Context)
		}
		if row.Context["effective_action"] != errorsx.RecoveryActionCandidateFailover || row.Context["transparent_resume"] != true {
			t.Fatalf("%s action context = %#v", kind, row.Context)
		}
		if row.Context["source"] != "test" {
			t.Fatalf("caller context was not preserved: %#v", row.Context)
		}
	}
}
