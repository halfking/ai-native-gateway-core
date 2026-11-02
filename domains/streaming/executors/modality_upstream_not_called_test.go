package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ★★ 2026-11-02 第十五轮（F-14）：「上游未被调用」的可观测版本。
//
// ── 这道门补的是哪一条缺口 ────────────────────────────────────────────
//
// handler 级那道门（domains/streaming/modality_embedding_chat_closed_loop_test.go）
// 钉的是「请求在候选层就被拒 ⇒ 没有进派发阶段」——那是从**网关自己**看。
// 但「上游未被调用」这句话真正的对象是**上游** ⇒ 它必须从**上游那边**看。
//
// ★ 本门就架在上游那边：两个 httptest 假上游服务器，各自数自己收到几次请求。
//   · 初始模型的绑定 ⇒ 上游 A；
//   · 替代模型的绑定 ⇒ 上游 B（★ 主臂里它是**登记为 embedding** 的绑定）。
//
// ── 场景（与 2026-11-02 事故链同族）─────────────────────────────────
//
//	一次**文本**请求打初始模型（登记为 text）⇒ 上游 A 一直回 502
//	⇒ dispatch 换替代模型（登记为 embedding）⇒ ★ 这里就是危险点：
//	   替代模型的候选是**重新解析**出来的 ⇒ 如果那次解析绕过了模态门，
//	   embedding 绑定就会被塞回本次文本请求 ⇒ 上游收到 chat 形态 ⇒ 400
//	   ⇒ 该绑定被翻红 ⇒ /v1/embeddings 对所有租户 503。
//
// ⇒ 主臂断言：**上游 B 收到 0 次请求**。
// ⇒ 阳性对照断言：同样的装置，把替代模型换成**登记为 text** 的绑定
//   ⇒ 上游 B' **必须**收到请求 ⇒ 证明这个计数器真的能观测到
//   「替代路径上的上游调用」⇒ 主臂的「0 次」不是空洞断言。

// upstreamNotCalledProvider 是 provider 的一副替身：每个模型各有一份候选池，
// 池里绑定的**登记模态**由 registered 给出（权威源：models_canonical.modality）。
type upstreamNotCalledProvider struct {
	// pools 按模型名给出候选。
	pools map[string][]provider.Candidate
	// registered 按模型名给出登记模态。
	registered map[string]string

	// plainCalls 统计**无过滤**查询（事故链的那一步）。
	plainCalls int
	// modalityCalls 记录带模态查询的实参。
	modalityCalls []string
}

func (p *upstreamNotCalledProvider) Enabled() bool { return true }

func (p *upstreamNotCalledProvider) ModelKnown(context.Context, string) bool { return true }

func (p *upstreamNotCalledProvider) GetCandidates(_ context.Context, model, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls++
	return p.pools[model], &provider.Policy{
		TierFallbackMax:    DefaultUpstreamAttemptLimit,
		RetryPerCredential: dispatch.MaxNodeFailures - 1,
	}, nil
}

func (p *upstreamNotCalledProvider) GetCandidatesByModality(_ context.Context, model, _, _, modality string) ([]provider.Candidate, *provider.Policy, error) {
	p.modalityCalls = append(p.modalityCalls, modality)
	var out []provider.Candidate
	for _, c := range p.pools[model] {
		if provider.ModalityAccepts(p.registered[model], modality) {
			out = append(out, c)
		}
	}
	return out, &provider.Policy{
		TierFallbackMax:    DefaultUpstreamAttemptLimit,
		RetryPerCredential: dispatch.MaxNodeFailures - 1,
	}, nil
}

// modalityUpstreamRecommender 把「替代模型」原样还给 dispatch
// （与 executors 侧既有的 attemptCapRecommender 同形）。
type modalityUpstreamRecommender struct{}

func (modalityUpstreamRecommender) RecommendModelAlternatives(_ context.Context, req autoroute.ModelAlternativeRequest) ([]string, error) {
	return append([]string(nil), req.PreferredModels...), nil
}

// upstreamNotCalledCandidate 造一条完整的候选（指向上游 baseURL）。
func upstreamNotCalledCandidate(baseURL, model string, credentialID int) provider.Candidate {
	c := overloadTestCandidate(baseURL)
	c.ProviderID = credentialID
	c.CredentialID = credentialID
	c.RawModel = model
	c.OfferRawModel = model
	c.StandardizedName = model
	c.APIKey = "key-" + model
	return c
}

// failingUpstream 造一个「一律回 502」的假上游，并给它一个计数器。
func failingUpstream(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"persistent upstream failure"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestExecute_AlternateModelMustNotBypassModalityGate 钉住「换替代模型时，
// 登记为 embedding 的绑定不得被送到上游」。
//
// ★ 两条臂：主臂（替代模型登记为 embedding ⇒ 上游零请求）与
//
//	阳性对照（替代模型登记为 text ⇒ 上游**必须**有请求）。
//	没有阳性对照，「0 次」可能只是因为装置没跑起来。
func TestExecute_AlternateModelMustNotBypassModalityGate(t *testing.T) {
	cases := []struct {
		name string
		// alternateModel 是 dispatch 要换过去的那个模型名。
		alternateModel string
		// alternateRegistered 是它的**登记模态**。
		alternateRegistered string
		// wantAlternateCalls 是替代模型那个上游应当收到的请求数下界。
		// 0 = 一个都不许收到。
		wantAlternateCalls int64
	}{
		{
			name:                "替代模型登记为 embedding ⇒ 上游零请求（主臂）",
			alternateModel:      "embed-model",
			alternateRegistered: "embedding",
			wantAlternateCalls:  0,
		},
		{
			name:                "替代模型登记为 text ⇒ 上游有请求（阳性对照）",
			alternateModel:      "text-model-b",
			alternateRegistered: "text",
			wantAlternateCalls:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var initialCalls, alternateCalls atomic.Int64
			initialUpstream := failingUpstream(t, &initialCalls)
			alternateUpstream := failingUpstream(t, &alternateCalls)

			prov := &upstreamNotCalledProvider{
				pools: map[string][]provider.Candidate{
					"text-model":      {upstreamNotCalledCandidate(initialUpstream.URL, "text-model", 11)},
					tc.alternateModel: {upstreamNotCalledCandidate(alternateUpstream.URL, tc.alternateModel, 99)},
				},
				registered: map[string]string{
					"text-model":      "text",
					tc.alternateModel: tc.alternateRegistered,
				},
			}

			limiter := newLimiterForTest()
			defer limiter.Stop()
			exec := NewExecutor(
				NewRouter(NewStickyCache(), limiter), newCircuitManagerForTest(), limiter,
				pool.NewPoolManager(nil), nil, nil, nil, nil,
			)
			wireDispatchPipelineForTest(t, exec)
			exec.Provider = prov
			exec.SetDispatchModelRecommender(modalityUpstreamRecommender{})
			dispatch.SetModelChangeEnabled(true)
			defer dispatch.SetModelChangeEnabled(false)

			budget := NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit)
			_, err := exec.Execute(&ExecParams{
				W:              httptest.NewRecorder(),
				R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				BodyBytes:      []byte(`{"model":"text-model","messages":[{"role":"user","content":"hi"}]}`),
				ClientProtocol: "openai-completions",
				ClientModel:    "text-model",
				Model:          "text-model",
				RequestID:      "modality-upstream-not-called",
				// ★ 候选池是**门控之后**的那一份（handler 已经带模态解析过了）。
				Candidates: prov.pools["text-model"],
				Policy: &provider.Policy{
					TierFallbackMax:    DefaultUpstreamAttemptLimit,
					RetryPerCredential: dispatch.MaxNodeFailures - 1,
				},
				// ★ 本次请求是**文本**请求 ⇒ 替代模型的候选解析必须过 text 模态门。
				RequestModality:             "text",
				DispatchAllowProviderChange: true,
				DispatchAllowModelChange:    true,
				DispatchModelAlternatives:   []string{tc.alternateModel},
				UpstreamAttempts:            budget,
			})
			if err == nil {
				t.Fatal("上游一直回 502 ⇒ 执行必须以错误收尾，want err != nil")
			}

			// 恒绿护栏（对两条臂都成立）：初始模型确实被调过 ⇒ 场景真的跑起来了。
			if initialCalls.Load() == 0 {
				t.Fatalf("初始模型的上游一次都没被调用 ⇒ 场景没有跑起来 ⇒ 下面那个计数没有意义")
			}
			if got := alternateCalls.Load(); got < tc.wantAlternateCalls {
				t.Fatalf("替代模型的上游收到 %d 次请求, want ≥ %d", got, tc.wantAlternateCalls)
			}
			if tc.wantAlternateCalls == 0 && alternateCalls.Load() != 0 {
				t.Fatalf("★ 登记为 embedding 的绑定被送到了上游（%d 次）⇒ 换替代模型的候选解析绕过了模态门 ⇒ "+
					"上游会收到 chat 形态 ⇒ 400 ⇒ 该绑定被翻红 ⇒ 事故链复活", alternateCalls.Load())
			}
			// 换替代模型时的候选解析必须**过模态门**（不许退回无过滤查询）。
			if prov.plainCalls != 0 {
				t.Errorf("GetCandidates（无模态过滤）被调用 %d 次 ⇒ 换替代模型时绕过了模态门", prov.plainCalls)
			}
			found := false
			for _, m := range prov.modalityCalls {
				if m == "text" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("GetCandidatesByModality 的实参 = %v，want 含 %q", prov.modalityCalls, "text")
			}
		})
	}
}
