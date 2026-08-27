package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// SR-05 (doc 18 §5.1 ExecuteAttempt): one bounded attempt through the
// executor, folded into the AttemptResult contract the SurvivalCoordinator
// aggregates. The executor's internal retry ladder is suppressed via
// ExecParams.SurvivalAttempt — ExecuteAttempt guarantees single-pass
// semantics regardless of how the caller built params.

type fakeAttemptExecutor struct {
	result *executors.ExecuteResult
	err    error
	calls  int
	lastW  interface{}
	lastR  *http.Request
}

func (f *fakeAttemptExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	f.calls++
	f.lastW = params.W
	f.lastR = params.R
	return f.result, f.err
}

func newAttemptGateForTest() *AttemptCommitGate {
	return NewAttemptCommitGate(context.Background(), ProtocolAnthropic,
		NewSerializedStreamWriter(io.Discard), GateOptions{Mode: GateModeBuffered})
}

func TestExecuteAttemptSuccessFoldsIntoAttemptResult(t *testing.T) {
	fake := &fakeAttemptExecutor{result: &executors.ExecuteResult{}}
	gate := newAttemptGateForTest()
	params := &executors.ExecParams{IsStream: true}

	res := ExecuteAttempt(context.Background(), fake, gate, params)

	if !res.Success {
		t.Fatalf("expected success, FinalError=%v", res.FinalError)
	}
	if res.ResponseType != ResponseTypeStream {
		t.Fatalf("response type = %v, want stream", res.ResponseType)
	}
	if res.SafeRetry {
		t.Fatal("successful attempt must not be marked SafeRetry")
	}
	if fake.calls != 1 {
		t.Fatalf("execute called %d times, want exactly 1", fake.calls)
	}
}

func TestExecuteAttemptFoldsCandidateOutcomesAndSafeRetry(t *testing.T) {
	execErr := &executors.ExecuteError{
		LastErr: errors.New("upstream 429"),
		Tried:   2,
		Attempts: []executors.AttemptRecord{
			{ProviderID: 1, CredentialID: 11, RawModel: "m", Kind: errorsx.KindRateLimit, Reason: "429"},
			{ProviderID: 2, CredentialID: 22, RawModel: "m", Kind: errorsx.KindTransient, Reason: "500"},
		},
		LastKind: errorsx.KindTransient,
	}
	fake := &fakeAttemptExecutor{err: execErr}
	gate := newAttemptGateForTest() // untouched: state none, not committed

	res := ExecuteAttempt(context.Background(), fake, gate, &executors.ExecParams{})

	if res.Success {
		t.Fatal("failed execution must not fold to success")
	}
	if len(res.CandidateOutcomes) != 2 {
		t.Fatalf("candidate outcomes = %d, want 2", len(res.CandidateOutcomes))
	}
	if res.CandidateOutcomes[0].Kind != errorsx.KindRateLimit ||
		res.CandidateOutcomes[0].CredentialID != "11" {
		t.Fatalf("outcome[0] = %+v", res.CandidateOutcomes[0])
	}
	if res.CommitState != CommitStateNone {
		t.Fatalf("commit state = %v, want none", res.CommitState)
	}
	if !res.SafeRetry {
		t.Fatal("uncommitted attempt must allow transparent retry")
	}
	if res.LastKind() != errorsx.KindTransient {
		t.Fatalf("last kind = %v", res.LastKind())
	}
}

func TestFailureAttributionUsesLastExecutedAttempt(t *testing.T) {
	execErr := &executors.ExecuteError{
		Attempts: []executors.AttemptRecord{
			{ProviderID: 5917, CredentialID: 32},
			{ProviderID: 12763, CredentialID: 36},
		},
	}

	providerID, credentialID := failureAttribution(execErr, nil)

	if providerID == nil || *providerID != 12763 {
		t.Fatalf("provider attribution = %v, want 12763", providerID)
	}
	if credentialID == nil || *credentialID != 36 {
		t.Fatalf("credential attribution = %v, want 36", credentialID)
	}
}

func TestFailureAttributionFallsBackToTopCandidate(t *testing.T) {
	candidates := []provider.Candidate{{ProviderID: 5917, CredentialID: 32}}

	providerID, credentialID := failureAttribution(errors.New("context canceled"), candidates)

	if providerID == nil || *providerID != 5917 {
		t.Fatalf("provider attribution = %v, want 5917", providerID)
	}
	if credentialID == nil || *credentialID != 32 {
		t.Fatalf("credential attribution = %v, want 32", credentialID)
	}
}

func TestExecuteAttemptNoCandidatesSynthesizesOutcome(t *testing.T) {
	execErr := &executors.ExecuteError{Tried: 0, Exhausted: true}
	fake := &fakeAttemptExecutor{err: execErr}

	res := ExecuteAttempt(context.Background(), fake, newAttemptGateForTest(), &executors.ExecParams{})

	if len(res.CandidateOutcomes) != 1 ||
		res.CandidateOutcomes[0].Kind != errorsx.KindNoAvailableChannel {
		t.Fatalf("expected single no_available_channel outcome, got %+v", res.CandidateOutcomes)
	}
}

func TestFoldCandidateOutcomesPreservesTypedRetryableKinds(t *testing.T) {
	err := &executors.ExecuteError{LastKind: errorsx.KindEmptyResponse, LastErr: errors.New("empty upstream response")}
	outcomes := foldCandidateOutcomes(err)
	if len(outcomes) != 1 || outcomes[0].Kind != errorsx.KindEmptyResponse {
		t.Fatalf("outcomes = %+v, want one empty_response outcome", outcomes)
	}
}

func TestExecuteAttemptCommittedGateBlocksSafeRetry(t *testing.T) {
	fake := &fakeAttemptExecutor{err: &executors.ExecuteError{LastKind: errorsx.KindTransient}}
	gate := newAttemptGateForTest()
	// Simulate the attempt having committed content before failing.
	if err := gate.WriteFrame("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"); err != nil {
		t.Fatal(err)
	}

	res := ExecuteAttempt(context.Background(), fake, gate, &executors.ExecParams{})

	if res.CommitState < CommitStateContent {
		t.Fatalf("commit state = %v, want >= content", res.CommitState)
	}
	if res.SafeRetry {
		t.Fatal("committed attempt must never allow transparent retry (doc 18 §10.3)")
	}
}

func TestExecuteAttemptForcesSurvivalFlag(t *testing.T) {
	fake := &fakeAttemptExecutor{result: &executors.ExecuteResult{}}
	saw := false
	fake2 := &flagCapturingExecutor{inner: fake, saw: &saw}
	ExecuteAttempt(context.Background(), fake2, newAttemptGateForTest(), &executors.ExecParams{})
	if !saw {
		t.Fatal("ExecuteAttempt must force SurvivalAttempt=true on params")
	}
}

type flagCapturingExecutor struct {
	inner *fakeAttemptExecutor
	saw   *bool
}

func (f *flagCapturingExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	*f.saw = params.SurvivalAttempt
	return f.inner.Execute(params)
}
