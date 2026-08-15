package streaming

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// SR-12 production runner tests: VerifyByID re-authorization, dynamic
// candidate rebuild (no persisted candidates), detached ExecParams shape,
// and outcome folding for the recovery worker.

type durableRunnerExecSpy struct {
	captured *executors.ExecParams
	result   *executors.ExecuteResult
	execErr  error
}

func (s *durableRunnerExecSpy) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	copied := *params
	s.captured = &copied
	return s.result, s.execErr
}

type durableRunnerVerifierFake struct {
	info *authentication.KeyInfo
	err  error
	last int
}

func (f *durableRunnerVerifierFake) VerifyByID(_ context.Context, id int) (*authentication.KeyInfo, error) {
	f.last = id
	return f.info, f.err
}

type durableRunnerResolverFake struct {
	cands                              []provider.Candidate
	lastModel, lastProfile, lastTenant string
	lastBody                           []byte
}

func (f *durableRunnerResolverFake) Enabled() bool { return true }

func (f *durableRunnerResolverFake) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error) {
	return f.GetCandidatesByModality(ctx, model, profile, tenantID, "text")
}

func (f *durableRunnerResolverFake) GetCandidatesByModality(_ context.Context, model, profile, tenantID, _ string) ([]provider.Candidate, *provider.Policy, error) {
	f.lastModel, f.lastProfile, f.lastTenant = model, profile, tenantID
	return f.cands, provider.DefaultPolicy(), nil
}

func (f *durableRunnerResolverFake) ModelKnown(context.Context, string) bool { return true }

func durableRunnerSnapshot(t *testing.T) *durable.Snapshot {
	t.Helper()
	snap := DurableRequestSnapshotV1{
		Version:            DurableSnapshotVersionV1,
		Endpoint:           "/v1/chat/completions",
		ClientProtocol:     "openai-completions",
		ClientModel:        "gpt-4o",
		NormalizedBody:     []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		RequestHash:        "hash-1",
		APIKeyID:           42,
		TenantID:           "tenant-1",
		ApplicationID:      7,
		SessionID:          "sess-1",
		ClientIdentityHash: "deadbeef",
		PolicyVersion:      DurablePolicyVersionV1,
		RequestID:          "req-9",
		TaskCorrelationID:  "req-9",
	}
	body, err := MarshalDurableSnapshotV1(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return &durable.Snapshot{TaskID: "task-7", Body: body}
}

func TestDurableAttemptRunnerRebuildsAndExecutes(t *testing.T) {
	exec := &durableRunnerExecSpy{result: &executors.ExecuteResult{
		ResponseBody: []byte(`{"choices":[{"message":{"content":"hi"}}]}`),
		Response:     &http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}},
	}}
	profile := "p1"
	verifier := &durableRunnerVerifierFake{info: &authentication.KeyInfo{
		ID: 42, TenantID: "tenant-1", ApplicationID: 7, DefaultClientProfile: &profile,
	}}
	resolver := &durableRunnerResolverFake{cands: []provider.Candidate{{ProviderID: 3}}}
	runner := NewDurableAttemptRunner(exec, resolver, verifier)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt, err := runner.Run(ctx, &durable.Task{ID: "task-7", AttemptCount: 2}, durableRunnerSnapshot(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if attempt == nil || attempt.Result == nil || !attempt.Result.Success {
		t.Fatalf("attempt not successful: %+v", attempt)
	}
	if string(attempt.Body) == "" || attempt.ContentType != "application/json" {
		t.Fatalf("body/content-type not extracted: %q %q", attempt.Body, attempt.ContentType)
	}

	// Re-authorization used the snapshot's key id, not any credential.
	if verifier.last != 42 {
		t.Fatalf("VerifyByID id = %d, want 42", verifier.last)
	}
	// Candidates are recomputed dynamically from the tenant/model — never
	// persisted ones.
	if resolver.lastModel != "gpt-4o" || resolver.lastTenant != "tenant-1" || resolver.lastProfile != "p1" {
		t.Fatalf("resolver got model=%q profile=%q tenant=%q", resolver.lastModel, resolver.lastProfile, resolver.lastTenant)
	}
	// Detached attempt shape: no writer, no callbacks, no stream.
	p := exec.captured
	if p.W != nil || !p.SuppressSuccessWrite || p.IsStream {
		t.Fatalf("detached ExecParams wrong: W=%v suppress=%v stream=%v", p.W, p.SuppressSuccessWrite, p.IsStream)
	}
	if string(p.BodyBytes) != `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}` ||
		p.ClientProtocol != "openai-completions" || p.ClientModel != "gpt-4o" ||
		p.SessionID != "sess-1" || p.RequestID != "req-9" || p.TenantID != "tenant-1" {
		t.Fatalf("ExecParams rebuilt wrong: %+v", p)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].ProviderID != 3 {
		t.Fatalf("candidates not injected: %+v", p.Candidates)
	}
	if p.R == nil || p.R.Context() != ctx {
		t.Fatal("synthetic request must carry the run context")
	}
	if p.R.Header.Get("X-Gw-Session-Id") != "sess-1" {
		t.Fatalf("synthetic request missing session header: %+v", p.R.Header)
	}
}

// A revoked/expired key is a permanent terminal: the runner folds it as
// AuthRevoked so AggregateTaskOutcome fails the task instead of retrying.
func TestDurableAttemptRunnerRevokedKeyIsTerminal(t *testing.T) {
	exec := &durableRunnerExecSpy{}
	verifier := &durableRunnerVerifierFake{err: &authentication.InvalidKeyError{Message: "revoked"}}
	runner := NewDurableAttemptRunner(exec, &durableRunnerResolverFake{}, verifier)

	attempt, err := runner.Run(context.Background(), &durable.Task{ID: "task-7"}, durableRunnerSnapshot(t))
	if err != nil {
		t.Fatalf("Run: %v (want folded outcome, not runner error)", err)
	}
	decision := AggregateTaskOutcome(attempt.Result)
	if decision.Action != TaskActionFailTerminal {
		t.Fatalf("decision = %v, want fail_terminal", decision.Action)
	}
	if exec.captured != nil {
		t.Fatal("revoked key must never reach the executor")
	}
}

// Verifier infrastructure errors stay transient (worker reschedules; the
// deadline reaper owns the terminal).
func TestDurableAttemptRunnerVerifierDBErrorIsTransient(t *testing.T) {
	verifier := &durableRunnerVerifierFake{err: errors.New("db down")}
	runner := NewDurableAttemptRunner(&durableRunnerExecSpy{}, &durableRunnerResolverFake{}, verifier)
	if _, err := runner.Run(context.Background(), &durable.Task{ID: "task-7"}, durableRunnerSnapshot(t)); err == nil {
		t.Fatal("transient verifier error must surface as runner error")
	}
}

// No available candidates folds to the wait-recovery window.
func TestDurableAttemptRunnerNoCandidatesWaits(t *testing.T) {
	profile := "p1"
	verifier := &durableRunnerVerifierFake{info: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", DefaultClientProfile: &profile}}
	runner := NewDurableAttemptRunner(&durableRunnerExecSpy{}, &durableRunnerResolverFake{}, verifier)

	attempt, err := runner.Run(context.Background(), &durable.Task{ID: "task-7"}, durableRunnerSnapshot(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	decision := AggregateTaskOutcome(attempt.Result)
	if decision.Action != TaskActionWaitRecovery {
		t.Fatalf("decision = %v, want wait_recovery", decision.Action)
	}
}

// A failed upstream attempt keeps its structured error kinds for the
// worker's aggregator (transient kinds → retry).
func TestDurableAttemptRunnerFoldsExecuteErrorKinds(t *testing.T) {
	profile := "p1"
	verifier := &durableRunnerVerifierFake{info: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", DefaultClientProfile: &profile}}
	execErr := &executors.ExecuteError{LastKind: errorsx.KindRateLimit, LastErr: errors.New("429")}
	exec := &durableRunnerExecSpy{execErr: execErr}
	exec.result = nil
	runner := NewDurableAttemptRunner(exec, &durableRunnerResolverFake{cands: []provider.Candidate{{ProviderID: 1}}}, verifier)

	attempt, err := runner.Run(context.Background(), &durable.Task{ID: "task-7", AttemptCount: 1}, durableRunnerSnapshot(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	decision := AggregateTaskOutcome(attempt.Result)
	if decision.Action != TaskActionWaitRecovery {
		t.Fatalf("decision = %v, want wait_recovery", decision.Action)
	}
	if attempt.ErrorKind != string(errorsx.KindRateLimit) {
		t.Fatalf("ErrorKind = %q, want rate_limit", attempt.ErrorKind)
	}
}
