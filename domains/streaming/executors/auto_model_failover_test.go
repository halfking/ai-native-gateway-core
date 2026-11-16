package executors

// auto_model_failover_test.go — auto 模式的**模型级回退**端到端钉测
// （2026-11-16 auto 模式可用性轮）。
//
// ★ 这条测试守的是用户报障的直接形态：
//
//	「auto 模式经常中间就断掉，表现为原来的模型不可用」
//
// 机理：auto 请求选定模型 A 后，A 名下的凭据全部失败时，网关**应当换一个模型
// 继续**（magpie 的同款语义：「同请求内失败即换下一个候选，客户端不感知」——
// magpie internal/gateway/gateway.go:4968-4984）。而本仓此前：
//
//   · dispatch 流水线的换模梯子被两个开关挡死——`qr.AllowModelChange`
//     （auto 请求专属）与 `p.modelChangeEnabled()`
//     （= dispatch.IsModelChangeEnabled，默认 **false**）；
//   · handler 层的 D5 旧路（handler.go:5546）进入条件互斥，**从未执行过**。
//
// ⇒ 211 真库 30 天 87 笔 auto 请求 `fallback_used` 全为 false，一次都没换过模；
// 13 笔失败里有 7 笔集中在同一条坏凭据（credential 62）上，会话表现为
// 「原来的模型不可用」。
//
// 本测试**不显式打开**任何开关：它断言的是**默认行为**——即 2026-11-16 把
// dispatch_v2.allow_model_change 默认值改为 true 之后，auto 请求在同模型
// 凭据耗尽时会换模型并把请求做完。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// autoFailoverProvider serves the requested model verbatim, so the dispatch
// pipeline can re-resolve candidates for the switched-to model.
type autoFailoverProvider struct {
	baseURL string
}

func (p *autoFailoverProvider) Enabled() bool { return true }

func (p *autoFailoverProvider) ModelKnown(context.Context, string) bool { return true }

func (p *autoFailoverProvider) GetCandidates(_ context.Context, model, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	candidates := make([]provider.Candidate, 0, 2)
	for i := 0; i < 2; i++ {
		id := i + 1
		candidate := overloadTestCandidate(p.baseURL)
		candidate.ProviderID = id
		candidate.CredentialID = id
		candidate.RawModel = model
		candidate.OfferRawModel = model
		candidate.StandardizedName = model
		candidate.APIKey = "key-" + strconv.Itoa(id)
		candidates = append(candidates, candidate)
	}
	return candidates, &provider.Policy{
		TierFallbackMax:    DefaultUpstreamAttemptLimit,
		RetryPerCredential: dispatch.MaxNodeFailures - 1,
	}, nil
}

// autoFailoverRecommender returns the request-scoped preferred models — the
// same shape the autoroute decider produces for an auto request
// (Decision.TierFailoverModels / CandidatesTop3).
type autoFailoverRecommender struct{}

func (autoFailoverRecommender) RecommendModelAlternatives(_ context.Context, req autoroute.ModelAlternativeRequest) ([]string, error) {
	return append([]string(nil), req.PreferredModels...), nil
}

// TestExecuteDispatchAutoRequestSwitchesModelWhenAllCredentialsFail is the
// regression test for the reported symptom: an auto request whose selected
// model has no working credential must be carried over to the next model
// instead of failing with "the original model is unavailable".
func TestExecuteDispatchAutoRequestSwitchesModelWhenAllCredentialsFail(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    = map[string]int{}
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode upstream request: %v", err)
			}
			mu.Lock()
			calls[body.Model]++
			mu.Unlock()
			if body.Model == "auto-model-b" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"auto-model-b",` +
					`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
					`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
				return
			}
			// The originally selected model is broken: every credential fails.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"model unavailable"}}`))
		}))
	)
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	exec := NewExecutor(
		NewRouter(NewStickyCache(), limiter), newCircuitManagerForTest(), limiter,
		pool.NewPoolManager(nil), nil, nil, nil, nil,
	)
	wireDispatchPipelineForTest(t, exec)

	exec.Provider = &autoFailoverProvider{baseURL: upstream.URL}
	exec.SetDispatchModelRecommender(autoFailoverRecommender{})

	// NOTE: dispatch.SetModelChangeEnabled is deliberately NOT called — the
	// default must already allow model-level failover for auto requests.
	if !dispatch.IsModelChangeEnabled() {
		t.Fatal("model-level failover must be enabled by default (auto mode resilience)")
	}

	const initialModel = "auto-model-a"
	result, err := exec.Execute(&ExecParams{
		W:                           httptest.NewRecorder(),
		R:                           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:                   []byte(`{"model":"auto-model-a","messages":[{"role":"user","content":"hi"}]}`),
		ClientProtocol:              "openai-completions",
		ClientModel:                 initialModel,
		Model:                       initialModel,
		RequestID:                   "auto-model-failover",
		Candidates:                  autoFailoverCandidates(upstream.URL, initialModel),
		Policy:                      &provider.Policy{TierFallbackMax: DefaultUpstreamAttemptLimit, RetryPerCredential: dispatch.MaxNodeFailures - 1},
		DispatchAllowProviderChange: true,
		// An auto request: the handler sets DispatchAllowModelChange only for
		// model="auto" traffic, and passes the router's fallback chain.
		DispatchAllowModelChange:  true,
		DispatchModelAlternatives: []string{"auto-model-b"},
		UpstreamAttempts:          NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	})
	if err != nil {
		t.Fatalf("auto request must survive a dead model by switching models, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result after the model switch")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["auto-model-b"] == 0 {
		t.Fatalf("the fallback model was never called; calls=%v", calls)
	}
}

// autoFailoverCandidates builds the candidate list for the initially selected
// model (both credentials point at the broken upstream).
func autoFailoverCandidates(baseURL, model string) []provider.Candidate {
	candidates := make([]provider.Candidate, 0, 2)
	for i := 0; i < 2; i++ {
		id := i + 1
		candidate := overloadTestCandidate(baseURL)
		candidate.ProviderID = id
		candidate.CredentialID = id
		candidate.RawModel = model
		candidate.OfferRawModel = model
		candidate.StandardizedName = model
		candidate.APIKey = "key-" + strconv.Itoa(id)
		candidates = append(candidates, candidate)
	}
	return candidates
}
