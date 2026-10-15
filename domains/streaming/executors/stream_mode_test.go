package executors

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestFilterCandidatesByStreamMode(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, StreamMode: provider.StreamModeBoth},
		{CredentialID: 2, StreamMode: provider.StreamModeStreamOnly},
		{CredentialID: 3, StreamMode: provider.StreamModeNonStreamOnly},
		{CredentialID: 4, StreamMode: ""},
		{CredentialID: 5, StreamMode: provider.StreamMode("invalid")},
	}

	stream, blockedStream := filterCandidatesByStreamMode(candidates, true)
	assertCredentialIDs(t, stream, []int{1, 2, 4})
	assertCredentialIDs(t, blockedStream, []int{3, 5})

	nonStream, blockedNonStream := filterCandidatesByStreamMode(candidates, false)
	assertCredentialIDs(t, nonStream, []int{1, 3, 4})
	assertCredentialIDs(t, blockedNonStream, []int{2, 5})
}

func TestExecutorRejectsMismatchedStreamModeBeforeRouting(t *testing.T) {
	probeCalls := 0
	executor := &Executor{
		Router:               &Router{},
		SyncNoCandidateProbe: true,
		ProbeSync: func(_ context.Context, _ []credentialstate.NoCandidatesCandidate, _, _ string) bool {
			probeCalls++
			return false
		},
	}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{
		R:           request,
		IsStream:    true,
		ClientModel: "claude-haiku-5-5",
		RequestID:   "req-stream-mode",
		Candidates: []provider.Candidate{{
			CredentialID: 63,
			ProviderID:   36,
			RawModel:     "claude-haiku-5-5",
			StreamMode:   provider.StreamModeNonStreamOnly,
		}},
	}

	_, err := executor.Execute(params)
	var execErr *ExecuteError
	if !errors.As(err, &execErr) {
		t.Fatalf("error = %T %v, want *ExecuteError", err, err)
	}
	if execErr.LastKind != errorsx.KindUnsupportedFeature {
		t.Fatalf("LastKind = %q, want %q", execErr.LastKind, errorsx.KindUnsupportedFeature)
	}
	if execErr.Trace == nil || execErr.Trace.FailureReason != "stream_mode_mismatch" {
		t.Fatalf("trace = %+v, want stream_mode_mismatch", execErr.Trace)
	}
	if execErr.Tried != 0 {
		t.Fatalf("Tried = %d, want zero upstream attempts", execErr.Tried)
	}
	if probeCalls != 0 {
		t.Fatalf("ProbeSync calls = %d, want zero for an operator policy mismatch", probeCalls)
	}
}

type streamModeProvider struct {
	candidates []provider.Candidate
}

func (p *streamModeProvider) Enabled() bool                           { return true }
func (p *streamModeProvider) ModelKnown(context.Context, string) bool { return true }
func (p *streamModeProvider) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return append([]provider.Candidate(nil), p.candidates...), provider.DefaultPolicy(), nil
}

func TestDispatchAlternateModelFiltersStreamMode(t *testing.T) {
	resolver := &streamModeProvider{candidates: []provider.Candidate{
		{CredentialID: 10, StreamMode: provider.StreamModeNonStreamOnly},
		{CredentialID: 20, StreamMode: provider.StreamModeBoth},
	}}
	executor := &Executor{Provider: resolver}
	dctx := &dispatchCtx{
		initialModel: "initial",
		params: &ExecParams{
			R:           httptest.NewRequest("POST", "/v1/chat/completions", nil),
			IsStream:    true,
			ClientModel: "initial",
		},
	}

	got := executor.dispatchCandidatesForModel(context.Background(), dctx, "alternate")
	assertCredentialIDs(t, got, []int{20})
	assertCredentialIDs(t, dctx.byModel["alternate"], []int{20})
}

func assertCredentialIDs(t *testing.T, candidates []provider.Candidate, want []int) {
	t.Helper()
	if len(candidates) != len(want) {
		t.Fatalf("credential count = %d, want %d: %+v", len(candidates), len(want), candidates)
	}
	for i, candidate := range candidates {
		if candidate.CredentialID != want[i] {
			t.Fatalf("credential[%d] = %d, want %d", i, candidate.CredentialID, want[i])
		}
	}
}
