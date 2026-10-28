package streaming

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type r1034DualResolver struct{ candidates []provider.Candidate }

func (r r1034DualResolver) Enabled() bool                           { return true }
func (r r1034DualResolver) ModelKnown(context.Context, string) bool { return true }

func (r r1034DualResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	// 默认：不是模态不匹配。既有测试的语义是「零候选」，不应被新的
	// 模态诊断分支改变行为；需要覆盖模态分支的测试单独提供自己的替身。
	return nil, nil
}
func (r r1034DualResolver) GetCandidates(ctx context.Context, m, p, t string) ([]provider.Candidate, *provider.Policy, error) {
	return r.candidates, provider.DefaultPolicy(), nil
}
func (r r1034DualResolver) GetCandidatesByModality(ctx context.Context, m, p, t, x string) ([]provider.Candidate, *provider.Policy, error) {
	return r.candidates, provider.DefaultPolicy(), nil
}

type r1034CandidateBSuccess struct{ writeErr error }

func (e *r1034CandidateBSuccess) Execute(p *executors.ExecParams) (*executors.ExecuteResult, error) {
	_, e.writeErr = p.W.Write(nativeComplianceDeltaFrame("openai-chat", "provider content"))
	return &executors.ExecuteResult{Candidate: p.Candidates[1]}, e.writeErr
}
func TestR1034HeldPolicyBlockDoesNotGuessCandidate(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "5000")
	a := &r1034CandidateBSuccess{}
	hook := &nativeNonStreamInterceptor{blocked: true}
	_, entries := runChatOutputCommitWith(t, response.NewInterceptorChain(hook), nil, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, func(_ *executors.Executor, h *ChatHandler, _ *http.Request) {
		original := h.provider.(nativeOutputAuditResolver).candidate
		second := original
		second.ProviderID = 99
		second.CredentialID = 88
		h.provider = r1034DualResolver{[]provider.Candidate{original, second}}
		h.survivalAttemptExec = a
		h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{Deadline: 5 * time.Second, RetryBase: time.Millisecond, MaxRetries: 1})
	})
	if a.writeErr != nil {
		t.Fatalf("write should remain held until final commit: %v", a.writeErr)
	}
	if hook.seenCalls == 0 {
		t.Fatal("never reached output guard")
	}
	for _, e := range entries {
		if e.ErrorKind != nil && *e.ErrorKind == "output_policy_blocked" {
			if e.ProviderID != nil || e.CredentialID != nil {
				t.Fatal("held rejection guessed candidate attribution")
			}
			return
		}
	}
	t.Fatal("no policy terminal")
}
