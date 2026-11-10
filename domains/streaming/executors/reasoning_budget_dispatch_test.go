package executors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestExecutor_ReasoningBurnRetriesUnderProductionDispatchBudget 是 2026-11-10
// 审计 P0-A 的回归门。
//
// ★ 为什么这条门必须用**生产形态**的参数：dispatch_v2 是唯一执行路径
// （executor.go:2803-2814），它给协议内循环传的 retryPerCred=0
// （executor_dispatch.go:445-448）⇒ effectiveOpenAIRetryBudget(0, true) = 0。
// ⇒ 原实现里那条「加倍 max_tokens 同候选免费重试」**拿不到任何迭代**
// ⇒ 加倍后的 body 永不上线，症状（reasoning 烧光 ⇒ empty_response ⇒
// 全候选 503）原样保留，而日志却照打（「重试」只存在于日志里）。
//
// ★ 判据不看日志、只看**上游实际收到的两次请求体**（通用式：日志命中 ≠
// 行为发生）⇒ 必须观察到 max_tokens 阶梯 [300, 2048] 且第二次拿到真实内容。
func TestExecutor_ReasoningBurnRetriesUnderProductionDispatchBudget(t *testing.T) {
	var sentMax []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentMax = append(sentMax, body.MaxTokens)
		w.Header().Set("Content-Type", "application/json")
		if body.MaxTokens <= 300 {
			// Reasoning burns the entire budget: zero visible content.
			_, _ = io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":300,"completion_tokens_details":{"reasoning_tokens":300}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c2","choices":[{"message":{"role":"assistant","content":"visible answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":40}}`)
	}))
	defer srv.Close()

	e := newTestExecutor()
	e.UpstreamTimeout = 5 * time.Second
	params := &ExecParams{
		R:               httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")),
		W:               httptest.NewRecorder(),
		BodyBytes:       []byte(`{"model":"m","max_tokens":300,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:        false,
		ClientModel:     "m",
		OutboundModel:   "m",
		Model:           "m",
		RequestID:       "reasoning-burn-dispatch",
		DispatchAttempt: true, // ★ dispatch_v2 生产的唯一形态
	}
	cand := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "m", BaseURL: srv.URL, Protocol: "openai-completions", APIKey: "k"}

	// maxRetries = 0 ⇒ 与 dctx.retryPerCred 同值（dispatch 路径）。
	res, err := e.executeOpenAI(params, cand, 0, time.Now(), nil)
	if err != nil || res == nil {
		t.Fatalf("dispatch-form reasoning burn must recover via the doubled retry: result=%v err=%v", res, err)
	}
	if len(sentMax) != 2 || sentMax[0] != 300 || sentMax[1] != 2048 {
		t.Fatalf("sent max_tokens ladder = %v, want [300 2048] (the doubled retry must actually reach the upstream)", sentMax)
	}
}

// TestExecutor_ReasoningBurnSecondBurnStillFallsOverUnderDispatch 钉住上界的
// 另一半：加倍后仍烧光 ⇒ 必须按既有 empty 语义 failover（不得无限循环）。
func TestExecutor_ReasoningBurnSecondBurnStillFallsOverUnderDispatch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":32000,"completion_tokens_details":{"reasoning_tokens":32000}}}`)
	}))
	defer srv.Close()

	e := newTestExecutor()
	e.UpstreamTimeout = 5 * time.Second
	params := &ExecParams{
		R:               httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")),
		W:               httptest.NewRecorder(),
		BodyBytes:       []byte(`{"model":"m","max_tokens":300,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:        false,
		ClientModel:     "m",
		OutboundModel:   "m",
		Model:           "m",
		RequestID:       "reasoning-burn-dispatch-second",
		DispatchAttempt: true,
	}
	cand := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "m", BaseURL: srv.URL, Protocol: "openai-completions", APIKey: "k"}

	_, err := e.executeOpenAI(params, cand, 0, time.Now(), nil)
	if err == nil {
		t.Fatal("second burn must fall over to the existing empty-response semantics, not succeed")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2 (one original + one doubled, then stop)", calls)
	}
}
